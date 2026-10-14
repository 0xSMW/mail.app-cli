package cmd

import (
	"errors"
	"fmt"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

func (a *application) newMessagesScanCmd() *cobra.Command {
	var query, since, cursor, checkpoint string
	var limit int
	var unread, restartCheckpoint bool
	cmd := &cobra.Command{
		Use:         "scan <mailbox> [mailbox...]",
		Short:       "Read explicit mailboxes live, retaining membership and coverage",
		Args:        cobra.MinimumNArgs(1),
		Annotations: map[string]string{annotationAgentNotes: "Always live and serial. Returns messages once per account/local ID with matching mailboxes, and coverage for every requested mailbox. Exit 5 and complete:false on a failed or truncated scope. --limit is per mailbox and page; 0 is unlimited. Coverage gives exhausted and remaining per mailbox. While nextCursor is present, repeat the same arguments with --cursor to read older messages; the traversal is finished when nextCursor is absent. Each mailbox page runs newest first from its own saved receipt-time/local-ID position; sequential mailbox reads have no shared snapshot or global arrival cutoff. --since filters receipt time, so it cannot find an older message whose read state or labels changed. An empty query lists messages. With --checkpoint, repeat the same scopes and filters to resume cumulative messages, memberships and coverage; --limit may change. The private checkpoint is saved before output. Its provenance distinguishes current-page timestamps from traversal observation bounds; accumulated messages describe earlier observations as well as the current page. Completed checkpoints require --restart-checkpoint for a fresh traversal. Observations are not an atomic snapshot; rerun after mutations."},
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.mailboxExplicit() {
				return clierr.Usage("scan takes positional mailboxes; do not also pass --mailbox")
			}
			if limit < 0 {
				return clierr.Usage("--limit must be non-negative")
			}
			preflightScopes := make([]mail.SearchMailbox, len(args))
			for i, box := range args {
				preflightScopes[i] = mail.SearchMailbox{Account: "validation", Mailbox: box}
			}
			if err := mail.ValidateScanRequest(mail.ScanRequest{Scopes: preflightScopes, Query: query, Since: since, Limit: limit, Unread: unread, Cursor: cursor, Checkpoint: checkpoint, RestartCheckpoint: restartCheckpoint}); err != nil {
				return clierr.Usage(err.Error())
			}
			account, err := a.requireAccount()
			if err != nil {
				return err
			}
			scopes := make([]mail.SearchMailbox, len(args))
			for i, mailbox := range args {
				scopes[i] = mail.SearchMailbox{Account: account, Mailbox: mailbox}
			}
			result, err := a.mailClient.ScanMessages(mail.ScanRequest{Scopes: scopes, Query: query, Since: since, Limit: limit, Unread: unread, Cursor: cursor, Checkpoint: checkpoint, RestartCheckpoint: restartCheckpoint})
			if errors.Is(err, mail.ErrInvalidScanCursor) || errors.Is(err, mail.ErrInvalidScanCheckpoint) {
				return clierr.Usage(err.Error())
			}
			if err != nil {
				return err
			}
			var failure *clierr.Error
			if !result.Complete {
				failure = clierr.New(clierr.CodePartial, "mailbox scan incomplete; inspect coverage for failures, and pass nextCursor to --cursor to continue truncated scopes")
			}
			if failure != nil && checkpoint != "" {
				failure = clierr.New(clierr.CodePartial, "mailbox scan incomplete; inspect coverage and repeat the same --checkpoint arguments to resume")
			}
			return a.writer.Write(output.Result{Data: result, Summary: fmt.Sprintf("%d messages across %d mailboxes (complete: %v)", len(result.Messages), len(result.Coverage), result.Complete), Err: failure, Plain: func(p *output.Printer) {
				for _, coverage := range result.Coverage {
					p.Line("%s/%s: %d messages (exhausted: %v, remaining: %d)", coverage.Account, coverage.Mailbox, coverage.Count, coverage.Exhausted, coverage.Remaining)
					if coverage.Error != "" {
						p.Line("  %s", p.Red(coverage.Error))
					}
				}
				for _, message := range result.Messages {
					p.Line("%s  %s  %v", message.ID, message.Subject, message.Mailboxes)
				}
				if result.Checkpoint != nil {
					p.Line("checkpoint: %s (resumed: %v, pages: %d; observed: %s to %s)", result.Checkpoint.Path, result.Checkpoint.Resumed, result.Checkpoint.Pages, result.StartedAt, result.EndedAt)
				}
				if result.NextCursor != "" && result.Checkpoint == nil {
					p.Line("next: --cursor %s", result.NextCursor)
				}
			}})
		},
	}
	cmd.Flags().StringVar(&query, "query", "", "Match terms in subject, sender, and indexed summary")
	cmd.Flags().StringVar(&since, "since", "", "Only messages received since a date or timestamp")
	cmd.Flags().IntVar(&limit, "limit", 200, "Maximum messages per mailbox and page; 0 is unlimited")
	cmd.Flags().StringVar(&cursor, "cursor", "", "Continue from the nextCursor of an earlier scan with the same arguments")
	cmd.Flags().StringVar(&checkpoint, "checkpoint", "", "Persist private cumulative traversal state and resume it on the next invocation")
	cmd.Flags().BoolVar(&restartCheckpoint, "restart-checkpoint", false, "Explicitly replace a checkpoint with a fresh traversal")
	cmd.Flags().BoolVar(&unread, "unread", false, "Only unread messages")
	return cmd
}
