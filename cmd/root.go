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

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

const version = "3.1.0"

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

// Execute runs the CLI and exits with the documented status.
func Execute() {
	os.Exit(Run(os.Args[1:], os.Stdout, os.Stderr))
}

// Run executes args against the root command and returns the exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return newApplication().run(args, stdout, stderr)
}

func (a *application) run(args []string, stdout, stderr io.Writer) int {
	defer a.writeBackendMetrics(stderr)
	a.rootCmd.SetArgs(args)
	a.rootCmd.SetOut(stdout)
	a.rootCmd.SetErr(stderr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	setCommandContext(a.rootCmd, ctx)
	err := a.rootCmd.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	cerr := classifyCommandError(err)
	if cerr.Reported {
		return clierr.ExitCode(cerr.Code)
	}
	w := a.writer
	if w == nil {
		// prepare never ran (for example, Cobra rejected the arguments). Resolve
		// output independently so that this error follows the same flag > env >
		// config > auto precedence as a command that reached prepare. A broken
		// config must not replace the original Cobra error.
		format := a.fallbackOutputFormat(args, stdout)
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

func (a *application) fallbackOutputFormat(args []string, stdout io.Writer) output.Format {
	// Cobra has not necessarily parsed persistent flags when it reports an
	// argument error. Read only output selectors here; they are sufficient for
	// output.Resolve and avoid treating an unknown command or malformed argument
	// as a second error.
	flags := outputFlagsFromArgs(args)
	loaded := a.cfg
	if !a.configLoadAttempted {
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

func (a *application) prepare(cmd *cobra.Command, args []string) error {
	loaded, err := config.Load()
	a.cfg = loaded
	a.configLoadAttempted = true
	if err != nil && !a.isConfigRecoveryCommand(cmd) {
		return clierr.Wrap(clierr.CodeUsage, err, err.Error()).WithHint("fix or delete the config file, see 'mail-app-cli config path'")
	}

	flags := map[string]string{}
	if cmd.Flags().Changed("account") {
		flags[config.KeyAccount] = a.flagAccount
	}
	if cmd.Flags().Changed("mailbox") {
		flags[config.KeyMailbox] = a.flagMailbox
	}
	a.resolved = config.Resolve(flags, a.cfg, os.Getenv)

	tty := output.IsTerminal(cmd.OutOrStdout())
	format, err := output.Resolve(a.outFlags, a.resolved.Output.Value, tty)
	if err != nil {
		return err
	}
	if a.outFlags.Count && cmd.Annotations[annotationList] != "true" && !isMetaCommand(cmd) {
		return clierr.Usage("--count only applies to commands that return a list").
			WithHint("use --jq to pick fields from this command's data")
	}
	if a.outFlags.IDsOnly && cmd.Annotations[annotationIDList] != "true" && !isMetaCommand(cmd) {
		return clierr.Usage("--ids-only only applies to lists whose items carry an id").
			WithHint("use --count to count this list, or --jq to pick fields from its data")
	}
	if a.outFlags.JQ != "" && a.jqWouldHideMutationReceipt(cmd) {
		return clierr.Usage("--jq cannot be combined with a command that changes state").
			WithHint("run the mutation without --jq so its receipt remains available")
	}
	color := output.ColorEnabled(format, tty, a.outFlags.NoColor, os.Getenv)
	a.writer, err = output.New(format, cmd.OutOrStdout(), cmd.ErrOrStderr(), color, a.outFlags.JQ, commandPath(cmd), mail.SchemaVersion)
	if err != nil {
		return err
	}
	a.mailClient = mail.NewClient().WithContext(cmd.Context())
	if err := a.mailClient.SetBackend(a.flagBackend); err != nil {
		return clierr.Usage(err.Error())
	}
	a.mailClient.SetWarn(a.writer.AddNotice)
	return nil
}

// jqWouldHideMutationReceipt reports whether this invocation can change state
// before the writer evaluates --jq. Exporting messages to stdout is read-only;
// selecting a file makes it a local mutation and therefore needs the same
// preflight protection as other receipt-bearing commands.
func (a *application) jqWouldHideMutationReceipt(cmd *cobra.Command) bool {
	if cmd.Annotations[annotationMutation] == "true" {
		return true
	}
	return cmd.Annotations[annotationFileOutputMutation] == "true" && a.exportMessagesWritesFile()
}

// isConfigRecoveryCommand reports commands that can identify the broken
// configuration without consuming any of its values.
func (a *application) isConfigRecoveryCommand(cmd *cobra.Command) bool {
	if isMetaCommand(cmd) {
		return true
	}
	return cmd == a.configPathCmd
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
	markCommands(annotationList, cmds...)
}

// markIDList tags list commands whose items carry an id.
func markIDList(cmds ...*cobra.Command) {
	markList(cmds...)
	markCommands(annotationIDList, cmds...)
}

// markMutation protects state-changing receipts from a post-action jq failure.
func markMutation(cmds ...*cobra.Command) {
	markCommands(annotationMutation, cmds...)
}

// markFileOutputMutation protects receipts when the output target is a file.
func markFileOutputMutation(cmds ...*cobra.Command) {
	markCommands(annotationFileOutputMutation, cmds...)
}

func markCommands(key string, cmds ...*cobra.Command) {
	for _, cmd := range cmds {
		if cmd.Annotations == nil {
			cmd.Annotations = map[string]string{}
		}
		cmd.Annotations[key] = "true"
	}
}

// helpCommand replaces cobra's default so an unknown topic is a usage error
// instead of the root help with exit 0.
func (a *application) helpCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "help [command or topic]",
		Short: "Help about any command or topic",
		RunE: func(c *cobra.Command, args []string) error {
			target, _, err := a.rootCmd.Find(args)
			if err != nil || (len(args) > 0 && target == a.rootCmd) {
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
func (a *application) agentAwareHelp(defaultHelp func(*cobra.Command, []string)) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, args []string) {
		if !a.outFlags.Agent {
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

func (a *application) buildRootCommands() {
	a.rootCmd = &cobra.Command{
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

	a.rootCmd.PersistentPreRunE = a.prepare
	a.rootCmd.SetVersionTemplate("mail-app-cli {{.Version}}\n")
	a.rootCmd.SetHelpFunc(a.agentAwareHelp(a.rootCmd.HelpFunc()))

	pf := a.rootCmd.PersistentFlags()
	pf.StringVar(&a.flagBackend, "backend", "", "Mail transport: native, legacy, or auto (default: native when available)")
	pf.StringVarP(&a.flagAccount, "account", "a", "", "Mail.app account name (default: config, or the only account)")
	pf.StringVarP(&a.flagMailbox, "mailbox", "m", "", "Mailbox name (default: config, or INBOX)")
	pf.BoolVar(&a.outFlags.JSON, "json", false, "Emit the JSON envelope even on a terminal")
	pf.BoolVar(&a.outFlags.Plain, "plain", false, "Emit the human view even when piped")
	pf.BoolVarP(&a.outFlags.Quiet, "quiet", "q", false, "Emit bare JSON data without the envelope")
	pf.BoolVar(&a.outFlags.IDsOnly, "ids-only", false, "Print one ID per line from list output")
	pf.BoolVar(&a.outFlags.Count, "count", false, "Print only the number of items in list output")
	pf.StringVar(&a.outFlags.JQ, "jq", "", "Filter JSON output with a jq expression (implies --json)")
	pf.BoolVar(&a.outFlags.NoColor, "no-color", false, "Disable ANSI color in the human view")
	pf.BoolVar(&a.outFlags.Agent, "agent", false, "JSON output and JSON --help for agents")
	_ = pf.MarkHidden("agent")

	a.rootCmd.AddGroup(
		&cobra.Group{ID: groupMail, Title: "Mail:"},
		&cobra.Group{ID: groupManage, Title: "Manage:"},
		&cobra.Group{ID: groupTools, Title: "Tooling:"},
	)

	for _, cmd := range []*cobra.Command{
		a.tuiCmd, a.inboxCmd, a.unreadCmd, a.showCmd, a.linksCmd, a.searchCmd,
		a.seenCmd, a.unseenCmd, a.flagCmd, a.unflagCmd, a.archiveCmd, a.deleteCmd, a.moveCmd, a.sendCmd,
	} {
		cmd.GroupID = groupMail
		a.rootCmd.AddCommand(cmd)
	}
	for _, cmd := range []*cobra.Command{
		a.accountsCmd, a.mailboxesCmd, a.messagesCmd, a.draftsCmd, a.attachmentsCmd, a.threadsCmd,
		a.rulesCmd, a.smartCmd, a.signaturesCmd, a.recentCmd, a.exportCmd, a.importCmd, a.syncCmd,
	} {
		cmd.GroupID = groupManage
		a.rootCmd.AddCommand(cmd)
	}
	for _, cmd := range []*cobra.Command{
		a.configCmd, a.doctorCmd, a.commandsCmd, a.skillCmd, a.versionCmd,
	} {
		cmd.GroupID = groupTools
		a.rootCmd.AddCommand(cmd)
	}
	a.rootCmd.AddCommand(helpTopicCommands()...)
	a.rootCmd.SetHelpCommand(a.helpCommand())

	markIDList(
		a.inboxCmd, a.unreadCmd, a.searchCmd,
		a.accountsListCmd, a.messagesListCmd,
		a.messagesInboxCmd, a.messagesUnreadCmd, a.messagesSentCmd, a.messagesDraftsCmd,
		a.messagesFlaggedCmd, a.messagesTrashCmd, a.messagesJunkCmd, a.messagesVIPCmd,
		a.draftsListCmd, a.smartQueryCmd, a.threadsListCmd, a.recentSearchCmd,
	)
	markList(
		a.mailboxesListCmd, a.attachmentsListCmd, a.rulesListCmd, a.smartListCmd,
		a.signaturesListCmd, a.exportAttachmentsCmd,
	)
}

func init() { cobra.EnableCommandSorting = false }
