package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	vipDryRun bool
	vipLimit  int
)

var vipCmd = &cobra.Command{
	Use:   "vip",
	Short: "Inspect VIP mail support",
}

var vipListCmd = &cobra.Command{
	Use:   "list",
	Short: "List VIP senders",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("vip list is unsupported because Mail.app does not expose deterministic VIP sender records through this local scriptability layer")
	},
}

var vipAddCmd = &cobra.Command{
	Use:   "add [email]",
	Short: "Add a VIP sender",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if vipDryRun {
			return printJSON(map[string]any{"dryRun": true, "email": args[0], "action": "add"}, "vip add dry-run")
		}
		return fmt.Errorf("vip add is unsupported because Mail.app VIP mutation is not exposed through this local scriptability layer")
	},
}

var vipRemoveCmd = &cobra.Command{
	Use:   "remove [email]",
	Short: "Remove a VIP sender",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if vipDryRun {
			return printJSON(map[string]any{"dryRun": true, "email": args[0], "action": "remove"}, "vip remove dry-run")
		}
		return fmt.Errorf("vip remove is unsupported because Mail.app VIP mutation is not exposed through this local scriptability layer")
	},
}

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
	vipCmd.AddCommand(vipListCmd, vipAddCmd, vipRemoveCmd)
	for _, cmd := range []*cobra.Command{vipAddCmd, vipRemoveCmd} {
		cmd.Flags().BoolVar(&vipDryRun, "dry-run", false, "Show mutation without applying it")
	}
	messagesVIPCmd.Flags().IntVarP(&vipLimit, "limit", "l", 25, "Maximum messages")
}
