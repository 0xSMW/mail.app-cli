package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/config"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

const version = "2.1.2"

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
	// annotationMutation marks commands that can change Mail.app or local
	// state. Their receipts must never be hidden by a post-action --jq error.
	annotationMutation = "mutation"
	// annotationFileOutputMutation marks commands that only change local state
	// when their parsed output target is a file rather than stdout.
	annotationFileOutputMutation = "fileOutputMutation"
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

	// Retain a failed config load for fallback error formatting, so a malformed
	// or inaccessible config file is never read twice in one invocation.
	loadedConfig        config.Config
	configLoadAttempted bool
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
	loadedConfig = config.Config{}
	configLoadAttempted = false
	rootCmd.SetArgs(args)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	setCommandContext(rootCmd, ctx)
	defer setCommandContext(rootCmd, context.Background())
	err := rootCmd.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	cerr := classifyCommandError(err)
	if cerr.Reported {
		return clierr.ExitCode(cerr.Code)
	}
	w := writer
	if w == nil {
		// prepare never ran (for example, Cobra rejected the arguments). Resolve
		// output independently so that this error follows the same flag > env >
		// config > auto precedence as a command that reached prepare. A broken
		// config must not replace the original Cobra error.
		format := fallbackOutputFormat(args, stdout)
		w, _ = output.New(format, stdout, stderr, false, "", "", mail.SchemaVersion)
	}
	w.Error(cerr)
	return clierr.ExitCode(cerr.Code)
}

func setCommandContext(cmd *cobra.Command, ctx context.Context) {
	cmd.SetContext(ctx)
	for _, child := range cmd.Commands() {
		setCommandContext(child, ctx)
	}
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

func fallbackOutputFormat(args []string, stdout io.Writer) output.Format {
	// Cobra has not necessarily parsed persistent flags when it reports an
	// argument error. Read only output selectors here; they are sufficient for
	// output.Resolve and avoid treating an unknown command or malformed argument
	// as a second error.
	flags := outputFlagsFromArgs(args)
	loaded := loadedConfig
	if !configLoadAttempted {
		var err error
		loaded, err = config.Load()
		if err != nil {
			loaded = config.Config{}
		}
	}
	configured := config.Resolve(nil, loaded, os.Getenv).Output.Value
	format, err := output.Resolve(flags, configured, output.IsTerminal(stdout))
	if err == nil {
		return format
	}

	// Keep the original parse or argument error authoritative if the raw flags
	// themselves conflict. JSON is the safest fallback for an explicit JSON
	// selector; otherwise preserve an explicit plain selector or auto output.
	if flags.JSON || flags.JQ != "" || flags.Agent || flags.Quiet || flags.IDsOnly || flags.Count {
		return output.FormatJSON
	}
	if flags.Plain {
		return output.FormatPlain
	}
	if output.IsTerminal(stdout) {
		return output.FormatPlain
	}
	return output.FormatJSON
}

func outputFlagsFromArgs(args []string) output.Flags {
	var flags output.Flags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		if arg == "-q" {
			flags.Quiet = true
			continue
		}
		if arg == "--jq" {
			if i+1 < len(args) {
				flags.JQ = args[i+1]
				i++
			}
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--jq="); ok {
			flags.JQ = value
			continue
		}
		setOutputBoolFlag(&flags, arg)
	}
	return flags
}

func setOutputBoolFlag(flags *output.Flags, arg string) {
	name, value, hasValue := strings.Cut(arg, "=")
	set := !hasValue || !strings.EqualFold(value, "false")
	switch name {
	case "--json":
		flags.JSON = set
	case "--plain":
		flags.Plain = set
	case "--quiet":
		flags.Quiet = set
	case "--ids-only":
		flags.IDsOnly = set
	case "--count":
		flags.Count = set
	case "--agent":
		flags.Agent = set
	}
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
	loadedConfig = loaded
	configLoadAttempted = true
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
	if outFlags.JQ != "" && jqWouldHideMutationReceipt(cmd) {
		return clierr.Usage("--jq cannot be combined with a command that changes state").
			WithHint("run the mutation without --jq so its receipt remains available")
	}
	color := output.ColorEnabled(format, tty, outFlags.NoColor, os.Getenv)
	writer, err = output.New(format, cmd.OutOrStdout(), cmd.ErrOrStderr(), color, outFlags.JQ, commandPath(cmd), mail.SchemaVersion)
	if err != nil {
		return err
	}
	mailClient = mail.NewClient().WithContext(cmd.Context())
	mailClient.SetWarn(writer.AddNotice)
	return nil
}

// jqWouldHideMutationReceipt reports whether this invocation can change state
// before the writer evaluates --jq. Exporting messages to stdout is read-only;
// selecting a file makes it a local mutation and therefore needs the same
// preflight protection as other receipt-bearing commands.
func jqWouldHideMutationReceipt(cmd *cobra.Command) bool {
	if cmd.Annotations[annotationMutation] == "true" {
		return true
	}
	return cmd.Annotations[annotationFileOutputMutation] == "true" && exportMessagesWritesFile()
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

// markMutation tags commands that can make a state change. prepare uses the
// annotation before RunE so an otherwise-valid jq expression cannot fail only
// after a receipt-bearing action has succeeded.
func markMutation(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[annotationMutation] = "true"
	}
}

// markFileOutputMutation tags commands whose output target determines whether
// they change local state. The command's predicate is evaluated in prepare
// after Cobra has parsed its flags.
func markFileOutputMutation(cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[annotationFileOutputMutation] = "true"
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
		tuiCmd, inboxCmd, unreadCmd, showCmd, searchCmd,
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
