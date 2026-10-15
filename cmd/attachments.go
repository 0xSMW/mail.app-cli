package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/spf13/cobra"
)

func (a *application) buildAttachmentsCommands() {
	var attSavePath string

	a.attachmentsCmd = &cobra.Command{
		Use:   "attachments",
		Short: "List and save message attachments",
	}
	a.attachmentsListCmd = &cobra.Command{
		Use:   "list <message-id>",
		Short: "List attachments on a message",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			refs, notices, err := a.locateMessages(args)
			if err != nil {
				return err
			}
			ref := refs[0]
			attachments, err := a.mailClient.GetAttachmentsJSON(ref.Account, ref.Mailbox, ref.ID)
			if err != nil {
				return fmt.Errorf("get attachments: %w", err)
			}
			return a.writer.Write(output.Result{
				Data:    attachments,
				Summary: fmt.Sprintf("%s on message %s", plural(len(attachments), "attachment"), ref.ID),
				Notices: notices,
				Meta:    map[string]any{"account": ref.Account, "mailbox": ref.Mailbox, "messageId": ref.ID},
				Plain:   renderAttachments(attachments),
			})
		},
	}
	a.attachmentsSaveCmd = &cobra.Command{
		Use:   "save <message-id> <attachment-name>",
		Short: "Save one attachment to disk",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			refs, notices, err := a.locateMessages(args[:1])
			if err != nil {
				return err
			}
			ref := refs[0]
			name := args[1]
			path := attSavePath
			if path == "" {
				path = name
			}
			if err := a.mailClient.SaveAttachment(ref.Account, ref.Mailbox, ref.ID, name, path); err != nil {
				return fmt.Errorf("save attachment: %w", err)
			}
			return a.writer.Write(output.Result{
				Data:    map[string]any{"messageId": ref.ID, "name": name, "path": path, "saved": true},
				Summary: "Saved " + name + " to " + path,
				Notices: notices,
				Plain:   renderLine("Saved %s to %s", name, path),
			})
		},
	}

	a.attachmentsCmd.AddCommand(a.attachmentsListCmd, a.attachmentsSaveCmd)
	a.attachmentsSaveCmd.Flags().StringVarP(&attSavePath, "output", "o", "", "Output file path (defaults to the attachment name)")
}
