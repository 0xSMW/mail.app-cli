package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/pkg/cache"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

const messageCacheTTL = 5 * time.Minute

var (
	msgAccount       string
	msgMailbox       string
	msgLimit         int
	msgOffset        int
	msgUnread        bool
	msgFlaggedFilter bool
	msgWithContent   bool
	msgRead          bool
	msgFlaggedSet    bool
	msgSince         string
	msgNoCache       bool
	msgForceRefresh  bool
)

// sanitizeCacheKey replaces non-alphanumeric chars so the key is safe as a filename component.
func sanitizeCacheKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// invalidateMailboxCache removes all message-list cache entries for the given mailbox.
// Call this after any mutation so subsequent list commands see fresh data.
func invalidateMailboxCache(account, mailbox string) {
	if c, err := cache.New(); err == nil {
		prefix := fmt.Sprintf("msgs-%s-%s-", sanitizeCacheKey(account), sanitizeCacheKey(mailbox))
		c.DeletePrefix(prefix)
	}
}

var messagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "Manage Mail.app messages",
	Long:  `View and manage email messages in Mail.app.`,
}

var messagesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List messages",
	Long:  `List messages from a specific mailbox. Output is JSON format. Use jq for pretty printing: mail-app-cli messages list -a Account -m INBOX | jq`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAccountAndMailbox(msgAccount, msgMailbox); err != nil {
			return err
		}
		var c *cache.Cache
		var cacheErr error
		if !msgNoCache {
			c, cacheErr = cache.New()
		}

		// Build a cache key that encodes all query parameters so different queries
		// get separate cache entries.
		cacheKey := fmt.Sprintf("msgs-%s-%s-%d-%d-%v-%v-%s-%v",
			sanitizeCacheKey(msgAccount),
			sanitizeCacheKey(msgMailbox),
			msgLimit, msgOffset,
			msgUnread, msgFlaggedFilter,
			sanitizeCacheKey(msgSince),
			msgWithContent,
		)

		// Try cache first (skip if content requested — content is per-user and typically large)
		if !msgNoCache && !msgForceRefresh && !msgWithContent && cacheErr == nil {
			c.SetTTL(messageCacheTTL)
			var cached []mail.Message
			found, err := c.Get(cacheKey, &cached)
			if err == nil && found {
				return printJSON(cached, "messages")
			}
		}

		client := mail.NewClient()
		messages, err := client.GetMessagesJSON(msgAccount, msgMailbox, msgLimit, msgOffset, msgUnread, msgFlaggedFilter, msgWithContent, msgSince)
		if err != nil {
			return fmt.Errorf("failed to get messages: %w", err)
		}

		// Populate cache (skip content results because content enrichment can be partial)
		if !msgNoCache && !msgWithContent && cacheErr == nil {
			c.SetTTL(messageCacheTTL)
			c.Set(cacheKey, messages)
		}

		return printJSON(messages, "messages")
	},
}

var messagesShowCmd = &cobra.Command{
	Use:   "show [message-id]",
	Short: "Show message details",
	Long:  `Show full details of a specific message. Output is JSON format.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		if err := requireAccountAndMailbox(msgAccount, msgMailbox); err != nil {
			return err
		}

		client := mail.NewClient()
		message, err := client.GetMessageDetailsJSON(msgAccount, msgMailbox, messageID)
		if err != nil {
			return fmt.Errorf("failed to get message: %w", err)
		}

		return printJSON(message, "message")
	},
}

var messagesMarkCmd = &cobra.Command{
	Use:   "mark [message-id]",
	Short: "Mark message as read/unread",
	Long:  `Mark a message as read or unread.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		if err := requireAccountAndMailbox(msgAccount, msgMailbox); err != nil {
			return err
		}

		client := mail.NewClient()
		err := client.MarkMessageAsRead(msgAccount, msgMailbox, messageID, msgRead)
		if err != nil {
			return fmt.Errorf("failed to mark message: %w", err)
		}
		invalidateMailboxCache(msgAccount, msgMailbox)

		status := "unread"
		if msgRead {
			status = "read"
		}
		fmt.Printf("Message marked as %s\n", status)
		return nil
	},
}

var messagesFlagCmd = &cobra.Command{
	Use:   "flag [message-id]",
	Short: "Flag or unflag a message",
	Long:  `Set or unset the flagged status of a message.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		if err := requireAccountAndMailbox(msgAccount, msgMailbox); err != nil {
			return err
		}

		client := mail.NewClient()
		err := client.FlagMessage(msgAccount, msgMailbox, messageID, msgFlaggedSet)
		if err != nil {
			return fmt.Errorf("failed to flag message: %w", err)
		}
		invalidateMailboxCache(msgAccount, msgMailbox)

		status := "unflagged"
		if msgFlaggedSet {
			status = "flagged"
		}
		fmt.Printf("Message %s\n", status)
		return nil
	},
}

var messagesDeleteCmd = &cobra.Command{
	Use:   "delete [message-id]",
	Short: "Delete a message",
	Long:  `Move a message to the trash.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		if err := requireAccountAndMailbox(msgAccount, msgMailbox); err != nil {
			return err
		}

		client := mail.NewClient()
		err := client.DeleteMessageResolved(msgAccount, msgMailbox, messageID)
		if err != nil {
			return fmt.Errorf("failed to delete message: %w", err)
		}
		invalidateMailboxCache(msgAccount, msgMailbox)
		invalidateMailboxCache(msgAccount, "Archive")
		invalidateMailboxCache(msgAccount, "All Mail")

		fmt.Println("Message deleted")
		return nil
	},
}

var messagesArchiveCmd = &cobra.Command{
	Use:   "archive [message-id]",
	Short: "Archive a message",
	Long:  `Move a message to the Archive mailbox.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		if err := requireAccountAndMailbox(msgAccount, msgMailbox); err != nil {
			return err
		}

		client := mail.NewClient()
		err := client.ArchiveMessage(msgAccount, msgMailbox, messageID)
		if err != nil {
			return fmt.Errorf("failed to archive message: %w", err)
		}
		invalidateMailboxCache(msgAccount, msgMailbox)
		// Also invalidate the archive mailbox (provider-dependent name)
		invalidateMailboxCache(msgAccount, "Archive")
		invalidateMailboxCache(msgAccount, "All Mail")

		fmt.Println("Message archived")
		return nil
	},
}

var messagesMoveCmd = &cobra.Command{
	Use:   "move [message-id] [target-mailbox]",
	Short: "Move a message to another mailbox",
	Long:  `Move a message to a different mailbox.`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		targetMailbox := args[1]
		if err := requireAccountAndMailbox(msgAccount, msgMailbox); err != nil {
			return err
		}

		client := mail.NewClient()
		err := client.MoveMessage(msgAccount, msgMailbox, messageID, targetMailbox)
		if err != nil {
			return fmt.Errorf("failed to move message: %w", err)
		}
		invalidateMailboxCache(msgAccount, msgMailbox)
		invalidateMailboxCache(msgAccount, targetMailbox)

		fmt.Printf("Message moved to %s\n", targetMailbox)
		return nil
	},
}

// newUnifiedCmd returns a cobra.Command for a unified mailbox view.
// mailboxType must match one of the types understood by GetUnifiedMessagesJSON.
func newUnifiedCmd(use, short, mailboxType string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			client := mail.NewClient()
			var messages []mail.Message
			var err error
			if msgAccount != "" || msgMailbox != "" {
				if err := requireAccountAndMailbox(msgAccount, msgMailbox); err != nil {
					return err
				}
				unreadOnly, flaggedOnly := scopedUnifiedFilters(mailboxType)
				messages, err = client.GetMessagesJSON(msgAccount, msgMailbox, msgLimit, msgOffset, unreadOnly, flaggedOnly, msgWithContent, "")
			} else {
				messages, err = client.GetUnifiedMessagesJSON(mailboxType, msgLimit, msgOffset, msgWithContent)
			}
			if err != nil {
				return fmt.Errorf("failed to get %s messages: %w", mailboxType, err)
			}

			return printJSON(messages, "messages")
		},
	}
}

func scopedUnifiedFilters(mailboxType string) (bool, bool) {
	return mailboxType == "unread", mailboxType == "flagged"
}

var messagesInboxCmd = newUnifiedCmd(
	"inbox",
	"List inbox messages across all accounts",
	"inbox",
)

var messagesUnreadCmd = newUnifiedCmd(
	"unread",
	"List unread messages across all accounts",
	"unread",
)

var messagesSentCmd = newUnifiedCmd(
	"sent",
	"List sent messages across all accounts",
	"sent",
)

var messagesDraftsCmd = newUnifiedCmd(
	"drafts",
	"List draft messages across all accounts",
	"drafts",
)

var messagesFlaggedCmd = newUnifiedCmd(
	"flagged",
	"List flagged messages across all accounts",
	"flagged",
)

var messagesTrashCmd = newUnifiedCmd(
	"trash",
	"List trash messages across all accounts",
	"trash",
)

var messagesJunkCmd = newUnifiedCmd(
	"junk",
	"List junk/spam messages across all accounts",
	"junk",
)

func init() {
	messagesCmd.AddCommand(messagesListCmd)
	messagesCmd.AddCommand(messagesShowCmd)
	messagesCmd.AddCommand(messagesMarkCmd)
	messagesCmd.AddCommand(messagesFlagCmd)
	messagesCmd.AddCommand(messagesDeleteCmd)
	messagesCmd.AddCommand(messagesArchiveCmd)
	messagesCmd.AddCommand(messagesMoveCmd)
	// Unified view subcommands
	messagesCmd.AddCommand(messagesInboxCmd)
	messagesCmd.AddCommand(messagesUnreadCmd)
	messagesCmd.AddCommand(messagesSentCmd)
	messagesCmd.AddCommand(messagesDraftsCmd)
	messagesCmd.AddCommand(messagesFlaggedCmd)
	messagesCmd.AddCommand(messagesTrashCmd)
	messagesCmd.AddCommand(messagesJunkCmd)
	messagesCmd.AddCommand(messagesBatchCmd)
	messagesCmd.AddCommand(messagesVIPCmd)

	// Common flags for all message commands
	messagesCmd.PersistentFlags().StringVarP(&msgAccount, "account", "a", "", "Account name (required)")
	messagesCmd.PersistentFlags().StringVarP(&msgMailbox, "mailbox", "m", "", "Mailbox name (required)")

	// List-specific flags
	messagesListCmd.Flags().IntVarP(&msgLimit, "limit", "l", 25, "Maximum number of messages to display")
	messagesListCmd.Flags().IntVarP(&msgOffset, "offset", "o", 0, "Number of messages to skip (for pagination)")
	messagesListCmd.Flags().BoolVarP(&msgUnread, "unread", "u", false, "Show only unread messages")
	messagesListCmd.Flags().BoolVarP(&msgFlaggedFilter, "flagged", "f", false, "Show only flagged messages")
	messagesListCmd.Flags().BoolVar(&msgWithContent, "with-content", false, "Include message content (slower but better for accessibility)")
	messagesListCmd.Flags().StringVarP(&msgSince, "since", "s", "", "Show messages since date (format: YYYY-MM-DD or YYYY-MM-DD HH:MM:SS)")
	messagesListCmd.Flags().BoolVar(&msgNoCache, "no-cache", false, "Bypass cache and fetch fresh data")
	messagesListCmd.Flags().BoolVar(&msgForceRefresh, "force-refresh", false, "Force refresh cache with fresh data")

	// Mark-specific flags
	messagesMarkCmd.Flags().BoolVarP(&msgRead, "read", "r", true, "Mark as read (default) or use --read=false for unread")

	// Flag-specific flags
	messagesFlagCmd.Flags().BoolVarP(&msgFlaggedSet, "flagged", "f", true, "Flag message (default) or use --flagged=false to unflag")

	// Unified view flags (shared across all unified subcommands)
	for _, cmd := range []*cobra.Command{
		messagesInboxCmd, messagesUnreadCmd, messagesSentCmd,
		messagesDraftsCmd, messagesFlaggedCmd, messagesTrashCmd, messagesJunkCmd,
	} {
		cmd.Flags().IntVarP(&msgLimit, "limit", "l", 25, "Maximum number of messages to return")
		cmd.Flags().IntVarP(&msgOffset, "offset", "o", 0, "Number of messages to skip (pagination)")
		cmd.Flags().BoolVar(&msgWithContent, "with-content", false, "Include message content")
	}
}
