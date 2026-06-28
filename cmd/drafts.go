package cmd

import (
	"fmt"
	"os"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	draftAccount  string
	draftTo       []string
	draftCc       []string
	draftBcc      []string
	draftSubject  string
	draftBody     string
	draftBodyFile string
	draftDryRun   bool
	draftLimit    int
)

var draftsCmd = &cobra.Command{
	Use:   "drafts",
	Short: "Create and manage Mail.app drafts",
}

var draftsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List draft messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		var messages []mail.Message
		var err error
		if draftAccount != "" {
			messages, err = client.GetMessagesJSON(draftAccount, "Drafts", draftLimit, 0, false, false, true, "")
		} else {
			messages, err = client.GetUnifiedMessagesJSON("drafts", draftLimit, 0, true)
		}
		if err != nil {
			return fmt.Errorf("failed to list drafts: %w", err)
		}
		return printJSON(messages, "drafts")
	},
}

var draftsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a draft without sending it",
	RunE: func(cmd *cobra.Command, args []string) error {
		if draftAccount == "" {
			return fmt.Errorf("--account is required")
		}
		body, err := readBodyValue(draftBody, draftBodyFile)
		if err != nil {
			return err
		}
		input := mail.DraftInput{
			Account: draftAccount,
			Subject: draftSubject,
			Body:    body,
			To:      draftTo,
			Cc:      draftCc,
			Bcc:     draftBcc,
		}
		if draftDryRun {
			return printJSON(map[string]any{"dryRun": true, "draft": input}, "draft dry-run")
		}
		client := mail.NewClient()
		message, err := client.CreateDraft(input)
		if err != nil {
			return fmt.Errorf("failed to create draft: %w", err)
		}
		return printJSON(message, "draft")
	},
}

var draftsShowCmd = &cobra.Command{
	Use:   "show [draft-id]",
	Short: "Show a draft",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		message, err := client.GetDraft(draftAccount, args[0])
		if err != nil {
			return err
		}
		return printJSON(message, "draft")
	},
}

var draftsUpdateCmd = &cobra.Command{
	Use:   "update [draft-id]",
	Short: "Update draft fields",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := readBodyValue(draftBody, draftBodyFile)
		if err != nil {
			return err
		}
		input := mail.DraftInput{Subject: draftSubject, Body: body}
		if draftDryRun {
			return printJSON(map[string]any{"dryRun": true, "draftId": args[0], "updates": input}, "draft update dry-run")
		}
		client := mail.NewClient()
		message, err := client.UpdateDraft(draftAccount, args[0], input)
		if err != nil {
			return fmt.Errorf("failed to update draft: %w", err)
		}
		return printJSON(message, "draft")
	},
}

var draftsSendCmd = &cobra.Command{
	Use:   "send [draft-id]",
	Short: "Send an existing draft",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if draftDryRun {
			return printJSON(map[string]any{"dryRun": true, "draftId": args[0], "action": "send"}, "draft send dry-run")
		}
		client := mail.NewClient()
		if err := client.SendDraft(draftAccount, args[0]); err != nil {
			return fmt.Errorf("failed to send draft: %w", err)
		}
		return printJSON(map[string]any{"draftId": args[0], "sent": true}, "draft send result")
	},
}

var draftsDeleteCmd = &cobra.Command{
	Use:   "delete [draft-id]",
	Short: "Delete a draft",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if draftDryRun {
			return printJSON(map[string]any{"dryRun": true, "draftId": args[0], "action": "delete"}, "draft delete dry-run")
		}
		client := mail.NewClient()
		if err := client.DeleteDraft(draftAccount, args[0]); err != nil {
			return fmt.Errorf("failed to delete draft: %w", err)
		}
		return printJSON(map[string]any{"draftId": args[0], "deleted": true}, "draft delete result")
	},
}

func readBodyValue(inline, file string) (string, error) {
	if file == "" {
		return inline, nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func init() {
	draftsCmd.AddCommand(draftsListCmd, draftsCreateCmd, draftsShowCmd, draftsUpdateCmd, draftsSendCmd, draftsDeleteCmd)
	for _, cmd := range []*cobra.Command{draftsListCmd, draftsCreateCmd, draftsShowCmd, draftsUpdateCmd, draftsSendCmd, draftsDeleteCmd} {
		cmd.Flags().StringVarP(&draftAccount, "account", "a", "", "Account name")
	}
	draftsListCmd.Flags().IntVarP(&draftLimit, "limit", "l", 25, "Maximum drafts")
	for _, cmd := range []*cobra.Command{draftsCreateCmd, draftsUpdateCmd} {
		cmd.Flags().StringVar(&draftSubject, "subject", "", "Draft subject")
		cmd.Flags().StringVar(&draftBody, "body", "", "Draft body")
		cmd.Flags().StringVar(&draftBodyFile, "body-file", "", "Read draft body from file")
		cmd.Flags().BoolVar(&draftDryRun, "dry-run", false, "Show mutation without applying it")
	}
	draftsCreateCmd.Flags().StringSliceVar(&draftTo, "to", []string{}, "To recipients")
	draftsCreateCmd.Flags().StringSliceVar(&draftCc, "cc", []string{}, "Cc recipients")
	draftsCreateCmd.Flags().StringSliceVar(&draftBcc, "bcc", []string{}, "Bcc recipients")
	draftsSendCmd.Flags().BoolVar(&draftDryRun, "dry-run", false, "Show mutation without applying it")
	draftsDeleteCmd.Flags().BoolVar(&draftDryRun, "dry-run", false, "Show mutation without applying it")
}
