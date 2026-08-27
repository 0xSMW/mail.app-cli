package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/config"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const version = "2.0.0"

const (
	groupMail   = "mail"
	groupManage = "manage"
	groupTools  = "tools"
)

// Annotation keys read by `commands --json` and agent help.
const (
	annotationAgentNotes    = "agentNotes"
	annotationCompatibility = "compatibility"
	annotationHelpTopic     = "helpTopic"
	// annotationList marks commands whose data is a list, which accept --count.
	annotationList = "list"
	// annotationIDList marks list commands whose items carry an id, which also
	// accept --ids-only.
	annotationIDList = "idList"
)

var (
	flagAccount string
	flagMailbox string
	outFlags    output.Flags

	// Per-invocation state built in prepare.
	writer     *output.Writer
	cfg        config.Config
	resolved   config.Resolved
	mailClient *mail.Client
)

var rootCmd = &cobra.Command{
	Use:   "mail-app-cli",
	Short: "Drive macOS Mail.app from the terminal",
	Long: `mail-app-cli reads and acts on Mail.app accounts, mailboxes, and messages.

Output is a table on a terminal and a JSON envelope when piped. See
'mail-app-cli help output' for the envelope, 'help exit-codes' for exit
statuses, and 'help agents' for how to drive it from a script or an agent.`,
	Version:       version,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the CLI and exits with the documented status.
func Execute() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

// Run executes args against the root command and returns the exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	// Cobra commands and their flags are package globals. Clear the previous
	// invocation before parsing, and again on every return, so callers that use
	// Run more than once get the same behavior as separate CLI processes.
	resetCommandFlags(rootCmd)
	defer resetCommandFlags(rootCmd)

	writer = nil
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	err := rootCmd.Execute()
	if err == nil {
		return 0
	}
	cerr := classifyCommandError(err)
	if cerr.Reported {
		return clierr.ExitCode(cerr.Code)
	}
	w := writer
	if w == nil {
		// prepare never ran (parse error, bad config). Honor an explicit JSON
		// request in the raw args so agents still get a parseable error.
		format := output.FormatJSON
		if output.IsTerminal(stdout) && !wantsJSONArgs(args) {
			format = output.FormatPlain
		}
		w, _ = output.New(format, stdout, stderr, false, "", "", mail.SchemaVersion)
	}
	w.Error(cerr)
	return clierr.ExitCode(cerr.Code)
}

// resetCommandFlags restores every registered flag value and Changed bit on a
// command tree. Cobra otherwise retains both across Execute calls.
func resetCommandFlags(cmd *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if slice, ok := f.Value.(pflag.SliceValue); ok {
			_ = slice.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	cmd.PersistentFlags().VisitAll(reset)
	cmd.Flags().VisitAll(reset)
	for _, sub := range cmd.Commands() {
		resetCommandFlags(sub)
	}
}

func wantsJSONArgs(args []string) bool {
	for _, arg := range args {
		switch {
		case arg == "--json", arg == "--agent", arg == "--quiet", arg == "-q", arg == "--jq",
			strings.HasPrefix(arg, "--jq="), arg == "--ids-only", arg == "--count":
			return true
		}
	}
	return os.Getenv(config.EnvOutput) == "json"
}

// classifyCommandError turns cobra's own parse failures into usage errors and
// everything else into a coded error.
func classifyCommandError(err error) *clierr.Error {
	cerr := clierr.Classify(err)
	if cerr.Code != clierr.CodeInternal {
		return cerr
	}
	lower := strings.ToLower(err.Error())
	for _, marker := range []string{
		"unknown flag", "unknown shorthand", "unknown command", "accepts ", "requires at least",
		"required flag", "invalid argument", "flag needs an argument", "bad flag syntax",
	} {
		if strings.Contains(lower, marker) {
			return clierr.Wrap(clierr.CodeUsage, err, err.Error()).WithHint("run 'mail-app-cli --help' or 'mail-app-cli <command> --help'")
		}
	}
	return cerr
}

func prepare(cmd *cobra.Command, args []string) error {
	loaded, err := config.Load()
	if err != nil && !isConfigRecoveryCommand(cmd) {
		return clierr.Wrap(clierr.CodeUsage, err, err.Error()).WithHint("fix or delete the config file, see 'mail-app-cli config path'")
	}
	cfg = loaded

	flags := map[string]string{}
	if cmd.Flags().Changed("account") {
		flags[config.KeyAccount] = flagAccount
	}
	if cmd.Flags().Changed("mailbox") {
		flags[config.KeyMailbox] = flagMailbox
	}
	resolved = config.Resolve(flags, cfg, os.Getenv)

	tty := output.IsTerminal(cmd.OutOrStdout())
	format, err := output.Resolve(outFlags, resolved.Output.Value, tty)
	if err != nil {
		return err
	}
	if outFlags.Count && cmd.Annotations[annotationList] != "true" && !isMetaCommand(cmd) {
		return clierr.Usage("--count only applies to commands that return a list").
			WithHint("use --jq to pick fields from this command's data")
	}
	if outFlags.IDsOnly && cmd.Annotations[annotationIDList] != "true" && !isMetaCommand(cmd) {
		return clierr.Usage("--ids-only only applies to lists whose items carry an id").
			WithHint("use --count to count this list, or --jq to pick fields from its data")
	}
	color := output.ColorEnabled(format, tty, outFlags.NoColor, os.Getenv)
	writer, err = output.New(format, cmd.OutOrStdout(), cmd.ErrOrStderr(), color, outFlags.JQ, commandPath(cmd), mail.SchemaVersion)
	if err != nil {
		return err
	}
	mail.Warn = writer.AddNotice
	mailClient = mail.NewClient()
	return nil
}

// isConfigRecoveryCommand reports commands that can identify the broken
// configuration without consuming any of its values.
func isConfigRecoveryCommand(cmd *cobra.Command) bool {
	if isMetaCommand(cmd) {
		return true
	}
	return cmd == configPathCmd
}

// isMetaCommand reports cobra's own help and completion commands, which
// must work even when the config file is broken.
func isMetaCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "help", "completion", "__complete", "__completeNoDesc":
			return true
		}
	}
	return false
}

// markList tags commands whose data is a list and therefore accepts --count.
func markList(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[annotationList] = "true"
	}
}

// markIDList tags list commands whose items carry an id and therefore also
// accept --ids-only.
func markIDList(cmds ...*cobra.Command) {
	markList(cmds...)
	for _, cmd := range cmds {
		cmd.Annotations[annotationIDList] = "true"
	}
}

// helpCommand replaces cobra's default so an unknown topic is a usage error
// instead of the root help with exit 0.
func helpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command or topic]",
		Short: "Help about any command or topic",
		RunE: func(c *cobra.Command, args []string) error {
			target, _, err := rootCmd.Find(args)
			if err != nil || (len(args) > 0 && target == rootCmd) {
				return clierr.Usagef("unknown help topic %q", strings.Join(args, " ")).
					WithHint("topics: output, exit-codes, environment, agents; or a command name")
			}
			return target.Help()
		},
	}
}

func commandPath(cmd *cobra.Command) string {
	return strings.TrimSpace(strings.TrimPrefix(cmd.CommandPath(), cmd.Root().Name()))
}

// agentAwareHelp prints JSON help when --agent is set, otherwise cobra's.
func agentAwareHelp(defaultHelp func(*cobra.Command, []string)) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, args []string) {
		if !outFlags.Agent {
			defaultHelp(cmd, args)
			return
		}
		record := describeCommand(cmd, true)
		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			defaultHelp(cmd, args)
			return
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
	}
}

func init() {
	cobra.EnableCommandSorting = false
	rootCmd.PersistentPreRunE = prepare
	rootCmd.SetVersionTemplate("mail-app-cli {{.Version}}\n")
	rootCmd.SetHelpFunc(agentAwareHelp(rootCmd.HelpFunc()))

	pf := rootCmd.PersistentFlags()
	pf.StringVarP(&flagAccount, "account", "a", "", "Mail.app account name (default: config, or the only account)")
	pf.StringVarP(&flagMailbox, "mailbox", "m", "", "Mailbox name (default: config, or INBOX)")
	pf.BoolVar(&outFlags.JSON, "json", false, "Emit the JSON envelope even on a terminal")
	pf.BoolVar(&outFlags.Plain, "plain", false, "Emit the human view even when piped")
	pf.BoolVarP(&outFlags.Quiet, "quiet", "q", false, "Emit bare JSON data without the envelope")
	pf.BoolVar(&outFlags.IDsOnly, "ids-only", false, "Print one ID per line from list output")
	pf.BoolVar(&outFlags.Count, "count", false, "Print only the number of items in list output")
	pf.StringVar(&outFlags.JQ, "jq", "", "Filter JSON output with a jq expression (implies --json)")
	pf.BoolVar(&outFlags.NoColor, "no-color", false, "Disable ANSI color in the human view")
	pf.BoolVar(&outFlags.Agent, "agent", false, "JSON output and JSON --help for agents")
	_ = pf.MarkHidden("agent")

	rootCmd.AddGroup(
		&cobra.Group{ID: groupMail, Title: "Mail:"},
		&cobra.Group{ID: groupManage, Title: "Manage:"},
		&cobra.Group{ID: groupTools, Title: "Tooling:"},
	)

	for _, cmd := range []*cobra.Command{
		inboxCmd, unreadCmd, showCmd, searchCmd,
		seenCmd, unseenCmd, flagCmd, unflagCmd, archiveCmd, deleteCmd, moveCmd, sendCmd,
	} {
		cmd.GroupID = groupMail
		rootCmd.AddCommand(cmd)
	}
	for _, cmd := range []*cobra.Command{
		accountsCmd, mailboxesCmd, messagesCmd, draftsCmd, attachmentsCmd, threadsCmd,
		rulesCmd, smartCmd, signaturesCmd, recentCmd, exportCmd, importCmd, syncCmd,
	} {
		cmd.GroupID = groupManage
		rootCmd.AddCommand(cmd)
	}
	for _, cmd := range []*cobra.Command{
		configCmd, doctorCmd, commandsCmd, skillCmd, versionCmd,
	} {
		cmd.GroupID = groupTools
		rootCmd.AddCommand(cmd)
	}
	rootCmd.AddCommand(helpTopicCommands()...)
	rootCmd.SetHelpCommand(helpCommand())

	markIDList(
		inboxCmd, unreadCmd, searchCmd,
		accountsListCmd, messagesListCmd,
		messagesInboxCmd, messagesUnreadCmd, messagesSentCmd, messagesDraftsCmd,
		messagesFlaggedCmd, messagesTrashCmd, messagesJunkCmd, messagesVIPCmd,
		draftsListCmd, smartQueryCmd, threadsListCmd, recentSearchCmd,
	)
	markList(
		mailboxesListCmd, attachmentsListCmd, rulesListCmd, smartListCmd,
		signaturesListCmd, exportAttachmentsCmd,
	)
}
