package cmd

import (
	"os"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/config"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/internal/tui"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

var tuiMessage string

var tuiCmd = &cobra.Command{
	Use:   "tui",
	Short: "Open the interactive terminal mail client",
	Long: `Open the terminal mail client. Lists come from the Envelope Index; bodies
and every action go through Mail.app one call at a time.

Keys: j/k move, enter read, space select, e archive, # trash, m move, u read/unread,
! flag, / search, c compose, r reply, R reply all, f forward, 1-9 mailbox, tab panes,
ctrl+r refresh, ? help, q quit.`,
	Args: cobra.NoArgs,
	Annotations: map[string]string{
		annotationAgentNotes: "Interactive; needs a terminal. Agents should use the other commands.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if !output.IsTerminal(cmd.OutOrStdout()) {
			return clierr.Usage("tui needs a terminal on stdout")
		}
		if tuiMessage != "" && !isNumericID(tuiMessage) {
			return clierr.Usagef("message ID %q is not numeric", tuiMessage)
		}
		opts := tui.Options{
			Account:   resolved.Account.Value,
			MessageID: tuiMessage,
			Color:     output.ColorEnabled(output.FormatPlain, true, outFlags.NoColor, os.Getenv),
		}
		if resolved.Mailbox.Source != config.SourceDefault {
			// A mailbox, whether from the flag, the environment, or the config
			// file, only means something within an account.
			account, err := requireAccount()
			if err != nil {
				return err
			}
			opts.Account = account
			opts.Mailbox = mailboxInScope()
		}
		return tui.Run(mail.NewClient(), opts)
	},
}

func init() {
	tuiCmd.Flags().StringVar(&tuiMessage, "message", "", "Open this message ID once its mailbox is loaded")
}
