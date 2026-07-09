package cmd

import (
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	recentAccount string
	recentMailbox string
	recentLimit   int
)

var recentCmd = &cobra.Command{
	Use:   "recent",
	Short: "Inspect recently handled messages",
}

var recentSearchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Search recently handled messages",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messages, err := mail.SearchRecentMessages(args[0], recentAccount, recentMailbox, recentLimit, "")
		if err != nil {
			return fmt.Errorf("failed to search recent messages: %w", err)
		}
		return printJSON(messages, "recent messages")
	},
}

var recentShowCmd = &cobra.Command{
	Use:   "show [message-id-or-query]",
	Short: "Show a recently handled message by id or query",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		entry, err := mail.ResolveRecentMessage(args[0], recentAccount, recentMailbox)
		if err != nil {
			return err
		}
		client := mail.NewClient()
		message, err := getRecentMessageDetails(client, entry)
		if err != nil {
			return fmt.Errorf("failed to get recent message: %w", err)
		}
		_ = mail.RecordRecentMessage(*message, "recent-show")
		return printJSON(message, "message")
	},
}

var recentClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear the recently handled message journal",
	RunE: func(cmd *cobra.Command, args []string) error {
		return mail.ClearRecentMessages()
	},
}

func getRecentMessageDetails(client *mail.Client, entry *mail.RecentMessage) (*mail.Message, error) {
	candidates := recentMailboxCandidates(entry.Mailbox)
	var lastErr error
	for _, mailbox := range candidates {
		message, err := client.GetMessageDetailsJSON(entry.Account, mailbox, entry.ID)
		if err == nil && message != nil && message.ID == entry.ID {
			_ = mail.UpdateRecentMessageLocation(entry.Account, entry.ID, mailbox, "recent-show")
			return message, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("message not found")
}

func recentMailboxCandidates(mailbox string) []string {
	ordered := []string{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range ordered {
			if strings.EqualFold(existing, value) {
				return
			}
		}
		ordered = append(ordered, value)
	}
	add(mailbox)
	add("Archive")
	add("All Mail")
	add("INBOX")
	return ordered
}

func init() {
	recentCmd.AddCommand(recentSearchCmd)
	recentCmd.AddCommand(recentShowCmd)
	recentCmd.AddCommand(recentClearCmd)
	recentCmd.PersistentFlags().StringVarP(&recentAccount, "account", "a", "", "Limit to account")
	recentCmd.PersistentFlags().StringVarP(&recentMailbox, "mailbox", "m", "", "Limit to mailbox")
	recentSearchCmd.Flags().IntVarP(&recentLimit, "limit", "l", 10, "Maximum recent messages")
}
