package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/robertmeta/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	msgAccount      string
	msgMailbox      string
	msgLimit        int
	msgUnread       bool
	msgFlaggedFilter bool
	msgRead         bool
	msgFlaggedSet   bool
	msgMessageID    string
	msgSince        string
)

var messagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "Manage Mail.app messages",
	Long:  `View and manage email messages in Mail.app.`,
}

var messagesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List messages",
	Long:  `List messages from a specific mailbox.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if msgAccount == "" || msgMailbox == "" {
			return fmt.Errorf("both --account and --mailbox are required")
		}

		client := mail.NewClient()
		messages, err := client.GetMessagesJSON(msgAccount, msgMailbox, msgLimit, msgUnread, msgFlaggedFilter, msgSince)
		if err != nil {
			return fmt.Errorf("failed to get messages: %w", err)
		}

		if len(messages) == 0 {
			fmt.Println("No messages found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "ID\tSUBJECT\tFROM\tDATE\tREAD\tFLAGGED")
		fmt.Fprintln(w, "--\t-------\t----\t----\t----\t-------")
		for _, msg := range messages {
			read := " "
			if msg.Read {
				read = "✓"
			}
			flagged := " "
			if msg.Flagged {
				flagged = "⚑"
			}
			// Truncate subject if too long
			subject := msg.Subject
			if len(subject) > 50 {
				subject = subject[:47] + "..."
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", msg.ID, subject, msg.Sender, msg.DateReceived, read, flagged)
		}
		w.Flush()

		return nil
	},
}

var messagesShowCmd = &cobra.Command{
	Use:   "show [message-id]",
	Short: "Show message details",
	Long:  `Show full details of a specific message.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		if msgAccount == "" || msgMailbox == "" {
			return fmt.Errorf("both --account and --mailbox are required")
		}

		client := mail.NewClient()
		message, err := client.GetMessageDetailsJSON(msgAccount, msgMailbox, messageID)
		if err != nil {
			return fmt.Errorf("failed to get message: %w", err)
		}

		fmt.Printf("ID:          %s\n", message.ID)
		fmt.Printf("Subject:     %s\n", message.Subject)
		fmt.Printf("From:        %s\n", message.Sender)
		fmt.Printf("To:          %v\n", message.ToRecipients)
		if len(message.CcRecipients) > 0 {
			fmt.Printf("Cc:          %v\n", message.CcRecipients)
		}
		if len(message.BccRecipients) > 0 {
			fmt.Printf("Bcc:         %v\n", message.BccRecipients)
		}
		fmt.Printf("Date Sent:   %s\n", message.DateSent)
		fmt.Printf("Date Recv:   %s\n", message.DateReceived)
		fmt.Printf("Read:        %t\n", message.Read)
		fmt.Printf("Flagged:     %t\n", message.Flagged)
		fmt.Printf("Size:        %d bytes\n", message.MessageSize)
		fmt.Printf("\n--- Content ---\n%s\n", message.Content)

		return nil
	},
}

var messagesMarkCmd = &cobra.Command{
	Use:   "mark [message-id]",
	Short: "Mark message as read/unread",
	Long:  `Mark a message as read or unread.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		if msgAccount == "" || msgMailbox == "" {
			return fmt.Errorf("both --account and --mailbox are required")
		}

		client := mail.NewClient()
		err := client.MarkMessageAsRead(msgAccount, msgMailbox, messageID, msgRead)
		if err != nil {
			return fmt.Errorf("failed to mark message: %w", err)
		}

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
		if msgAccount == "" || msgMailbox == "" {
			return fmt.Errorf("both --account and --mailbox are required")
		}

		client := mail.NewClient()
		err := client.FlagMessage(msgAccount, msgMailbox, messageID, msgFlaggedSet)
		if err != nil {
			return fmt.Errorf("failed to flag message: %w", err)
		}

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
		if msgAccount == "" || msgMailbox == "" {
			return fmt.Errorf("both --account and --mailbox are required")
		}

		client := mail.NewClient()
		err := client.DeleteMessage(msgAccount, msgMailbox, messageID)
		if err != nil {
			return fmt.Errorf("failed to delete message: %w", err)
		}

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
		if msgAccount == "" || msgMailbox == "" {
			return fmt.Errorf("both --account and --mailbox are required")
		}

		client := mail.NewClient()
		err := client.ArchiveMessage(msgAccount, msgMailbox, messageID)
		if err != nil {
			return fmt.Errorf("failed to archive message: %w", err)
		}

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
		if msgAccount == "" || msgMailbox == "" {
			return fmt.Errorf("both --account and --mailbox are required")
		}

		client := mail.NewClient()
		err := client.MoveMessage(msgAccount, msgMailbox, messageID, targetMailbox)
		if err != nil {
			return fmt.Errorf("failed to move message: %w", err)
		}

		fmt.Printf("Message moved to %s\n", targetMailbox)
		return nil
	},
}

func init() {
	messagesCmd.AddCommand(messagesListCmd)
	messagesCmd.AddCommand(messagesShowCmd)
	messagesCmd.AddCommand(messagesMarkCmd)
	messagesCmd.AddCommand(messagesFlagCmd)
	messagesCmd.AddCommand(messagesDeleteCmd)
	messagesCmd.AddCommand(messagesArchiveCmd)
	messagesCmd.AddCommand(messagesMoveCmd)

	// Common flags for all message commands
	messagesCmd.PersistentFlags().StringVarP(&msgAccount, "account", "a", "", "Account name (required)")
	messagesCmd.PersistentFlags().StringVarP(&msgMailbox, "mailbox", "m", "", "Mailbox name (required)")

	// List-specific flags
	messagesListCmd.Flags().IntVarP(&msgLimit, "limit", "l", 25, "Maximum number of messages to display")
	messagesListCmd.Flags().BoolVarP(&msgUnread, "unread", "u", false, "Show only unread messages")
	messagesListCmd.Flags().BoolVarP(&msgFlaggedFilter, "flagged", "f", false, "Show only flagged messages")
	messagesListCmd.Flags().StringVarP(&msgSince, "since", "s", "", "Show messages since date (format: YYYY-MM-DD or YYYY-MM-DD HH:MM:SS)")

	// Mark-specific flags
	messagesMarkCmd.Flags().BoolVarP(&msgRead, "read", "r", true, "Mark as read (default) or use --read=false for unread")

	// Flag-specific flags
	messagesFlagCmd.Flags().BoolVarP(&msgFlaggedSet, "flagged", "f", true, "Flag message (default) or use --flagged=false to unflag")
}
