package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	vipLimit int
)

var messagesVIPCmd = &cobra.Command{
	Use:   "vip",
	Short: "List messages from VIP mailboxes when Mail.app exposes them",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		mailboxes, err := client.GetMailboxesJSON("")
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
			return fmt.Errorf("no VIP mailbox exposed by Mail.app")
		}
		messages, err := client.GetMessagesFromMultipleMailboxes(requests)
		if err != nil {
			return err
		}
		return printJSON(sortAndSliceMessages(messages, 0, vipLimit), "vip messages")
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
