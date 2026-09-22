package cmd

import (
	"fmt"
	"time"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

func newMessagesReadCmd() *cobra.Command {
	var timeout, budget time.Duration
	cmd := &cobra.Command{
		Use:         "read <message-id> [message-id...]",
		Short:       "Read selected bodies serially with per-message results",
		Args:        cobra.MinimumNArgs(1),
		Annotations: map[string]string{annotationAgentNotes: "Fetch only IDs chosen from metadata. Reuses a bounded serial bridge, preserves successful reads when another fails, and exits 5 with complete:false on any missing/incomplete body. No automatic retry of a failed ID. Existing show remains unchanged."},
		RunE: func(cmd *cobra.Command, args []string) error {
			if timeout <= 0 || budget <= 0 {
				return clierr.Usage("--timeout and --budget must be positive")
			}
			ids := uniqueStrings(trimAll(args))
			for _, id := range ids {
				if !isNumericID(id) {
					return clierr.Usagef("message ID %q is not numeric", id)
				}
			}
			refs, failed, notices, err := locateSelectedMessages(ids)
			if err != nil {
				return err
			}
			requests := make([]mail.MessageRef, len(refs))
			for i, ref := range refs {
				requests[i] = mail.MessageRef{AccountName: ref.Account, MailboxName: ref.Mailbox, MessageID: ref.ID}
			}
			result := mailClient.ReadSelectedMessages(requests, timeout, budget)
			if len(failed) > 0 {
				byID := make(map[string]mail.MessageReadResult)
				for _, item := range result.Items {
					byID[item.ID] = item
				}
				for _, item := range failed {
					byID[item.ID] = item
				}
				result.Items = make([]mail.MessageReadResult, 0, len(ids))
				for _, id := range ids {
					result.Items = append(result.Items, byID[id])
				}
				result.Complete = false
			}
			var failure *clierr.Error
			if !result.Complete {
				failure = clierr.New(clierr.CodePartial, "selected body read incomplete; inspect per-message errors")
			}
			return writer.Write(output.Result{Data: result, Summary: fmt.Sprintf("Read %d selected messages (complete: %v)", len(result.Items), result.Complete), Notices: notices, Err: failure, Plain: func(p *output.Printer) {
				for _, item := range result.Items {
					if item.Error != "" {
						p.Line("%s: %s", item.ID, p.Red(item.Error))
					}
					if item.Message != nil {
						renderMessage(item.Message, false)(p)
					}
				}
			}})
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "Maximum bridge execution time per message")
	cmd.Flags().DurationVar(&budget, "budget", 45*time.Second, "Total body-read budget including bridge queueing")
	return cmd
}

// Resolve the selection once, preserving missing/mismatched IDs as individual
// failures instead of discarding readable messages in the same request.
func locateSelectedMessages(ids []string) ([]messageRef, []mail.MessageReadResult, []string, error) {
	if mailboxExplicit() {
		refs, notices, err := locateMessages(ids)
		return refs, nil, notices, err
	}
	located, indexErr := mailClient.LocateMessages(ids)
	var refs []messageRef
	var failed []mail.MessageReadResult
	var notices []string
	for _, id := range ids {
		found, warnings, err := resolveLocatedMessages([]string{id}, located, indexErr)
		if err != nil {
			failed = append(failed, mail.MessageReadResult{ID: id, Error: err.Error()})
		} else {
			refs = append(refs, found...)
		}
		notices = append(notices, warnings...)
	}
	return refs, failed, uniqueStrings(notices), nil
}

func init() { messagesCmd.AddCommand(newMessagesReadCmd()) }
