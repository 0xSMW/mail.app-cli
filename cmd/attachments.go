package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	attAccount  string
	attMailbox  string
	attSavePath string
)

var attachmentsCmd = &cobra.Command{
	Use:   "attachments",
	Short: "Manage message attachments",
	Long:  `View and save email attachments.`,
}

var attachmentsListCmd = &cobra.Command{
	Use:   "list [message-id]",
	Short: "List attachments in a message",
	Long:  `List all attachments for a specific message. Output is JSON format.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		if err := requireAccountAndMailbox(attAccount, attMailbox); err != nil {
			return err
		}

		client := mail.NewClient()
		attachments, err := client.GetAttachmentsJSON(attAccount, attMailbox, messageID)
		if err != nil {
			return fmt.Errorf("failed to get attachments: %w", err)
		}

		return printJSON(attachments, "attachments")
	},
}

var attachmentsSaveCmd = &cobra.Command{
	Use:   "save [message-id] [attachment-name]",
	Short: "Save an attachment to disk",
	Long:  `Save a specific attachment from a message to a file.`,
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		attachmentName := args[1]
		if err := requireAccountAndMailbox(attAccount, attMailbox); err != nil {
			return err
		}

		if attSavePath == "" {
			attSavePath = attachmentName
		}

		client := mail.NewClient()
		err := client.SaveAttachment(attAccount, attMailbox, messageID, attachmentName, attSavePath)
		if err != nil {
			return fmt.Errorf("failed to save attachment: %w", err)
		}

		fmt.Printf("Attachment saved to: %s\n", attSavePath)
		return nil
	},
}

func init() {
	attachmentsCmd.AddCommand(attachmentsListCmd)
	attachmentsCmd.AddCommand(attachmentsSaveCmd)

	// Common flags
	attachmentsCmd.PersistentFlags().StringVarP(&attAccount, "account", "a", "", "Account name (required)")
	attachmentsCmd.PersistentFlags().StringVarP(&attMailbox, "mailbox", "m", "", "Mailbox name (required)")

	// Save-specific flags
	attachmentsSaveCmd.Flags().StringVarP(&attSavePath, "output", "o", "", "Output file path (defaults to attachment name)")
}
