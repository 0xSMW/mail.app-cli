package cmd

import (
	"sort"
	"strings"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var vipLimit int

var messagesVIPCmd = &cobra.Command{
	Use:   "vip",
	Short: "List messages from VIP mailboxes when Mail.app exposes them",
	RunE: func(cmd *cobra.Command, args []string) error {
		mailboxes, err := mailClient.GetMailboxesJSON("")
		if err != nil {
			return err
		}
		type req = struct {
			AccountName string
			MailboxName string
			Limit       int
			Offset      int
			UnreadOnly  bool
			FlaggedOnly bool
			WithContent bool
			Since       string
		}
		var requests []req
		for _, mailbox := range mailboxes {
			if strings.EqualFold(mailbox.Name, "VIP") || strings.EqualFold(mailbox.Name, "VIPs") {
				requests = append(requests, req{AccountName: mailbox.Account, MailboxName: mailbox.Name, Limit: vipLimit})
			}
		}
		if len(requests) == 0 {
			return clierr.New(clierr.CodeNotFound, "no VIP mailbox exposed by Mail.app")
		}
		messages, err := mailClient.GetMessagesFromMultipleMailboxes(requests)
		if err != nil {
			return err
		}
		messages = sortAndSliceMessages(messages, 0, vipLimit)
		return writer.Write(output.Result{
			Data:    messages,
			Summary: plural(len(messages), "VIP message"),
			Plain:   renderMessages(messages, true),
		})
	},
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

func init() {
	messagesVIPCmd.Flags().IntVarP(&vipLimit, "limit", "l", 25, "Maximum messages")
}
