package cmd

import (
	"sort"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

type vipMailboxRequest = mail.MailboxListRequest

// vipMailboxRequests keeps the unscoped view across all accounts while
// defending an explicitly scoped request from a mailbox response that includes
// another account.
func vipMailboxRequests(mailboxes []mail.Mailbox, account string, limit int) []vipMailboxRequest {
	account = strings.TrimSpace(account)
	var requests []vipMailboxRequest
	for _, mailbox := range mailboxes {
		if !strings.EqualFold(mailbox.Name, "VIP") && !strings.EqualFold(mailbox.Name, "VIPs") {
			continue
		}
		if account != "" && !strings.EqualFold(mailbox.Account, account) {
			continue
		}
		requestAccount := mailbox.Account
		if account != "" {
			requestAccount = account
		}
		requests = append(requests, vipMailboxRequest{AccountName: requestAccount, MailboxName: mailbox.Name, Limit: limit})
	}
	return requests
}

func sortAndSliceMessages(messages []mail.Message, offset, limit int) []mail.Message {
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].DateReceived > messages[j].DateReceived
	})
	if offset > 0 {
		if offset >= len(messages) {
			return []mail.Message{}
		}
		messages = messages[offset:]
	}
	if limit > 0 && len(messages) > limit {
		return messages[:limit]
	}
	return messages
}

func (a *application) buildVIPCommand() {
	var vipLimit int

	a.messagesVIPCmd = &cobra.Command{
		Use:   "vip",
		Short: "List messages from VIP mailboxes when Mail.app exposes them",
		RunE: func(cmd *cobra.Command, args []string) error {
			account := strings.TrimSpace(a.resolved.Account.Value)
			mailboxes, err := a.mailClient.GetMailboxesJSON(account)
			if err != nil {
				return err
			}
			requests := vipMailboxRequests(mailboxes, account, vipLimit)
			if len(requests) == 0 {
				return clierr.New(clierr.CodeNotFound, "no VIP mailbox exposed by Mail.app")
			}
			messages, err := a.mailClient.GetMessagesFromMultipleMailboxes(requests)
			if err != nil {
				return err
			}
			messages = sortAndSliceMessages(messages, 0, vipLimit)
			return a.writer.Write(output.Result{
				Data:    messages,
				Summary: plural(len(messages), "VIP message"),
				Plain:   renderMessages(messages, true),
			})
		},
	}

	a.messagesVIPCmd.Flags().IntVarP(&vipLimit, "limit", "l", 25, "Maximum messages")
}
