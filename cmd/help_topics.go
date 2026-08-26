package cmd

import (
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/spf13/cobra"
)

// helpTopicCommands returns non-runnable commands cobra lists under
// "Additional help topics" and prints with 'mail-app-cli help <topic>'.
func helpTopicCommands() []*cobra.Command {
	return []*cobra.Command{
		helpTopic("output", "How output is chosen and shaped", outputTopic),
		helpTopic("exit-codes", "Exit statuses and their JSON codes", exitCodesTopic()),
		helpTopic("environment", "Environment variables and files", environmentTopic),
		helpTopic("agents", "Driving mail-app-cli from a script or an agent", agentsTopic),
	}
}

func helpTopic(name, short, long string) *cobra.Command {
	return &cobra.Command{
		Use:         name,
		Short:       short,
		Long:        long,
		Annotations: map[string]string{annotationHelpTopic: "true"},
	}
}

const outputTopic = `Output

On a terminal you get tables and one-line results. When stdout is a pipe or a
file you get a JSON envelope. Force either with --plain or --json, or set a
default with 'config set output json|plain'.

Envelope (stdout, exit 0):

  {
    "ok": true,
    "schemaVersion": 1,
    "data": ...,
    "summary": "12 messages in Work/INBOX",
    "notices": ["..."],
    "meta": {"command": "messages list", "count": 12, "durationMs": 130, ...}
  }

Error (stderr, non-zero exit):

  {"ok": false, "schemaVersion": 1, "error": "...", "code": "not_found", "exitCode": 2, "hint": "..."}

Shortcuts:

  --quiet       bare data with no envelope (the 1.x shape, with camelCase keys)
  --ids-only    one id per line from lists whose items carry an id
  --count       just the number of items
  --jq EXPR     run a jq expression over the envelope (over data with --quiet);
                strings print raw, like jq -r
  --no-color    disable ANSI color; NO_COLOR=1 does the same

Field names are camelCase everywhere: id, subject, sender, dateReceived, read,
flagged, mailbox, account, toRecipients. Lists are never null.`

func exitCodesTopic() string {
	var b strings.Builder
	b.WriteString("Exit codes\n\n  0  ok\n")
	for _, row := range clierr.Table() {
		fmt.Fprintf(&b, "  %d  %-16s %s\n", row.Exit, row.Code, row.Meaning)
	}
	b.WriteString("\nThe same code appears as \"code\" in the JSON error envelope on stderr.\nMutation receipts and doctor results are written to stdout with ok:false and\nthe same code fields, so data.items[].error is still readable after exit 6.")
	return b.String()
}

const environmentTopic = `Environment

  MAIL_APP_CLI_ACCOUNT                  default --account
  MAIL_APP_CLI_MAILBOX                  default --mailbox (INBOX when unset)
  MAIL_APP_CLI_OUTPUT                   auto | json | plain
  MAIL_APP_CLI_CONFIG                   config file path
  MAIL_APP_CLI_SKILL_DIR                where 'skill install' writes
  MAIL_APP_CLI_SEARCH_TIMEOUT           seconds for slow automation search (8)
  MAIL_APP_CLI_CONTENT_TIMEOUT          seconds for --with-content fetching (45)
  MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX   set to force Mail.app automation for reads
  MAIL_APP_CLI_AUTOMATION_LOCK_PATH     cross-process lock file
  NO_COLOR                              disable color
  XDG_CONFIG_HOME                       config directory root

Files

  ~/.config/mail-app-cli/config.json    settings ('config show' prints sources)
  ~/.cache/mail-app-cli/                account and message-list cache, recent journal
  ~/Library/Mail/V10/MailData/Envelope Index
                                        read-only SQLite for fast lists and search;
                                        needs Full Disk Access for the app running mail-app-cli

Shell completion

  mail-app-cli completion zsh > "${fpath[1]}/_mail-app-cli"
  mail-app-cli completion bash > /usr/local/etc/bash_completion.d/mail-app-cli
  mail-app-cli completion fish > ~/.config/fish/completions/mail-app-cli.fish`

const agentsTopic = `Agents

  1. Always pass --json (or pipe). "ok" is false whenever the exit code is
     non-zero, including receipts with failed items and an unhealthy doctor,
     which still carry "data".
  2. Check the exit code; the JSON error on stderr has "code" and "hint".
     Warnings arrive as "notices" in the envelope, not as loose stderr text.
  3. IDs are numeric and come from list, search, inbox, and show output. A
     message ID alone is enough for show, seen, unseen, flag, unflag, archive,
     delete, move, and attachments: the mailbox is resolved through the
     Envelope Index. Pass --account and --mailbox only to override.
  4. Every message mutation (seen, unseen, flag, unflag, archive, delete,
     move, messages *, messages batch, threads archive) accepts --dry-run
     and --verify and returns one receipt shape:
     {action, dryRun, matched, attempted, succeeded, failed, skipped, items[]}.
     send, drafts, and rules accept --dry-run and return their own shapes.
     Preview selector-driven batch operations before adding --yes.
  5. 'search' fails closed (exit 5) when a mailbox could not be searched; add
     --allow-partial only when incomplete results are acceptable.
  6. 'send' cannot be undone. Use --dry-run, or 'drafts create' for review.
  7. 'commands --json' lists every command with flags and agentNotes;
     '--agent --help' on any command prints the same record for that command.
  8. 'mail-app-cli skill' prints a SKILL.md with the full workflow; 'skill install'
     puts it in ~/.claude/skills.`
