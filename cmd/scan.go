package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

func newMessagesScanCmd() *cobra.Command {
	var query, since string
	var limit int
	var unread bool
	cmd := &cobra.Command{
		Use:         "scan <mailbox> [mailbox...]",
		Short:       "Read explicit mailboxes live, retaining membership and coverage",
		Args:        cobra.MinimumNArgs(1),
		Annotations: map[string]string{annotationAgentNotes: "Always live and serial. Returns messages once per account/local ID with matching mailboxes, and coverage for every requested mailbox. Exit 5 and complete:false on a failed or truncated scope. --limit is per mailbox; 0 is unlimited. An empty query lists messages. Observations are not an atomic snapshot; rerun after mutations."},
		RunE: func(cmd *cobra.Command, args []string) error {
			if mailboxExplicit() {
				return clierr.Usage("scan takes positional mailboxes; do not also pass --mailbox")
			}
			if limit < 0 {
				return clierr.Usage("--limit must be non-negative")
			}
			account, err := requireAccount()
			if err != nil {
				return err
			}
			scopes := make([]mail.SearchMailbox, len(args))
			for i, mailbox := range args {
				scopes[i] = mail.SearchMailbox{Account: account, Mailbox: mailbox}
			}
			result, err := mailClient.ScanMessages(mail.ScanRequest{Scopes: scopes, Query: query, Since: since, Limit: limit, Unread: unread})
			if err != nil {
				return err
			}
			var failure *clierr.Error
			if !result.Complete {
				failure = clierr.New(clierr.CodePartial, "mailbox scan incomplete; inspect coverage for failures or increase --limit for truncated scopes")
			}
			return writer.Write(output.Result{Data: result, Summary: fmt.Sprintf("%d messages across %d mailboxes (complete: %v)", len(result.Messages), len(result.Coverage), result.Complete), Err: failure, Plain: func(p *output.Printer) {
				for _, coverage := range result.Coverage {
					p.Line("%s/%s: %d messages (truncated: %v)", coverage.Account, coverage.Mailbox, coverage.Count, coverage.Truncated)
					if coverage.Error != "" {
						p.Line("  %s", p.Red(coverage.Error))
					}
				}
				for _, message := range result.Messages {
					p.Line("%s  %s  %v", message.ID, message.Subject, message.Mailboxes)
				}
			}})
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "Match terms in subject, sender, and indexed summary")
	cmd.Flags().StringVar(&since, "since", "", "Only messages received since a date or timestamp")
	cmd.Flags().IntVar(&limit, "limit", 200, "Maximum messages per mailbox; 0 is unlimited")
	cmd.Flags().BoolVar(&unread, "unread", false, "Only unread messages")
	return cmd
}

func init() { messagesCmd.AddCommand(newMessagesScanCmd()) }
