package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/robertmeta/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	attAccount   string
	attMailbox   string
	attMessageID string
	attSavePath  string
)

var attachmentsCmd = &cobra.Command{
	Use:   "attachments",
	Short: "Manage message attachments",
	Long:  `View and save email attachments.`,
}

var attachmentsListCmd = &cobra.Command{
	Use:   "list [message-id]",
	Short: "List attachments in a message",
	Long:  `List all attachments for a specific message.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messageID := args[0]
		if attAccount == "" || attMailbox == "" {
			return fmt.Errorf("both --account and --mailbox are required")
		}

		client := mail.NewClient()
		attachments, err := client.GetAttachmentsJSON(attAccount, attMailbox, messageID)
		if err != nil {
			return fmt.Errorf("failed to get attachments: %w", err)
		}

		if len(attachments) == 0 {
			fmt.Println("No attachments found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tSIZE\tTYPE")
		fmt.Fprintln(w, "----\t----\t----")
		for _, att := range attachments {
			fmt.Fprintf(w, "%s\t%d\t%s\n", att.Name, att.FileSize, att.MimeType)
		}
		w.Flush()

		return nil
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
		if attAccount == "" || attMailbox == "" {
			return fmt.Errorf("both --account and --mailbox are required")
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
