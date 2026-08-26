package cmd

import (
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var recentLimit int

var recentCmd = &cobra.Command{
	Use:   "recent",
	Short: "Reopen recently handled messages",
	Annotations: map[string]string{
		annotationAgentNotes: "A local journal of messages touched by show, search, archive, and move. Use it to get back to a message without a broad search. --account and --mailbox narrow it.",
	},
}

var recentSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search the recent-message journal",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mailbox := ""
		if mailboxExplicit() {
			mailbox = mailboxInScope()
		}
		messages, err := mail.SearchRecentMessages(args[0], resolved.Account.Value, mailbox, recentLimit, "")
		if err != nil {
			return fmt.Errorf("search recent messages: %w", err)
		}
		return writer.Write(output.Result{
			Data:    messages,
			Summary: fmt.Sprintf("%s in the recent journal", plural(len(messages), "match")),
			Meta:    map[string]any{"source": "recent"},
			Plain:   renderMessages(messages, true),
		})
	},
}

var recentShowCmd = &cobra.Command{
	Use:   "show <message-id-or-query>",
	Short: "Show a recently handled message by ID or query",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mailbox := ""
		if mailboxExplicit() {
			mailbox = mailboxInScope()
		}
		entry, err := mail.ResolveRecentMessage(args[0], resolved.Account.Value, mailbox)
		if err != nil {
			return err
		}
		message, err := getRecentMessageDetails(mailClient, entry)
		if err != nil {
			return fmt.Errorf("get recent message: %w", err)
		}
		_ = mail.RecordRecentMessage(*message, "recent-show")
		return writer.Write(output.Result{
			Data:    message,
			Summary: fmt.Sprintf("%s from %s", output.Truncate(message.Subject, 60), displaySender(message.Sender)),
			Meta:    map[string]any{"account": message.Account, "mailbox": message.Mailbox},
			Plain:   renderMessage(message, false),
		})
	},
}

var recentClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear the recent-message journal",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := mail.ClearRecentMessages(); err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    map[string]any{"cleared": true},
			Summary: "Cleared the recent-message journal",
			Plain:   renderLine("Cleared the recent-message journal"),
		})
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
	return nil, clierr.New(clierr.CodeNotFound, "message not found: "+entry.ID)
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
	recentCmd.AddCommand(recentSearchCmd, recentShowCmd, recentClearCmd)
	recentSearchCmd.Flags().IntVarP(&recentLimit, "limit", "l", 10, "Maximum recent messages")
}
