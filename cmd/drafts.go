package cmd

import (
	"fmt"
	"os"

	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

var (
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
	Short: "Create and manage drafts",
	Annotations: map[string]string{
		annotationAgentNotes: "Drafts are the review-before-send lane: create, have a person look, then 'drafts send <id>'. Creating a draft takes about five seconds while Mail.app saves it.",
	},
}

var draftsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List drafts across accounts, or one with --account",
	RunE: func(cmd *cobra.Command, args []string) error {
		account := resolved.Account.Value
		var messages []mail.Message
		var err error
		if account != "" {
			messages, err = mailClient.GetMessagesJSON(account, "Drafts", draftLimit, 0, false, false, true, "")
		} else {
			messages, err = mailClient.GetUnifiedMessagesJSON("drafts", draftLimit, 0, true)
		}
		if err != nil {
			return fmt.Errorf("list drafts: %w", err)
		}
		return writer.Write(output.Result{
			Data:    messages,
			Summary: plural(len(messages), "draft"),
			Meta:    map[string]any{"account": accountOrAll(account)},
			Plain:   renderMessages(messages, account == ""),
		})
	},
}

var draftsCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a draft without sending it",
	RunE: func(cmd *cobra.Command, args []string) error {
		account, err := requireAccount()
		if err != nil {
			return err
		}
		body, err := readBodyValue(draftBody, draftBodyFile)
		if err != nil {
			return err
		}
		input := mail.DraftInput{Account: account, Subject: draftSubject, Body: body, To: draftTo, Cc: draftCc, Bcc: draftBcc}
		if draftDryRun {
			return writer.Write(output.Result{
				Data:    map[string]any{"dryRun": true, "draft": input},
				Summary: "Dry run: would create draft " + input.Subject,
				Plain:   renderLine("Dry run: would create draft %q for %v", input.Subject, input.To),
			})
		}
		message, err := mailClient.CreateDraft(input)
		if err != nil {
			return fmt.Errorf("create draft: %w", err)
		}
		return writer.Write(output.Result{
			Data:    message,
			Summary: "Created draft " + message.ID,
			Meta:    map[string]any{"account": account},
			Plain:   renderLine("Created draft %s (%s)", message.ID, message.Subject),
		})
	},
}

var draftsShowCmd = &cobra.Command{
	Use:   "show <draft-id>",
	Short: "Show a draft",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		message, err := mailClient.GetDraft(resolved.Account.Value, args[0])
		if err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    message,
			Summary: "Draft " + message.ID + ": " + message.Subject,
			Plain:   renderMessage(message, false),
		})
	},
}

var draftsUpdateCmd = &cobra.Command{
	Use:   "update <draft-id>",
	Short: "Update a draft's subject or body",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		body, err := readBodyValue(draftBody, draftBodyFile)
		if err != nil {
			return err
		}
		input := mail.DraftInput{
			Subject:    draftSubject,
			Body:       body,
			SubjectSet: cmd.Flags().Changed("subject"),
			BodySet:    cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file"),
		}
		if draftDryRun {
			return writer.Write(output.Result{
				Data:    map[string]any{"dryRun": true, "draftId": args[0], "updates": input},
				Summary: "Dry run: would update draft " + args[0],
				Plain:   renderLine("Dry run: would update draft %s", args[0]),
			})
		}
		message, err := mailClient.UpdateDraft(resolved.Account.Value, args[0], input)
		if err != nil {
			return fmt.Errorf("update draft: %w", err)
		}
		return writer.Write(output.Result{
			Data:    message,
			Summary: "Updated draft, now " + message.ID,
			Plain:   renderLine("Updated draft; new id %s", message.ID),
		})
	},
}

var draftsSendCmd = &cobra.Command{
	Use:   "send <draft-id>",
	Short: "Send an existing draft",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if draftDryRun {
			return writer.Write(output.Result{
				Data:    map[string]any{"dryRun": true, "draftId": args[0], "action": "send"},
				Summary: "Dry run: would send draft " + args[0],
				Plain:   renderLine("Dry run: would send draft %s", args[0]),
			})
		}
		if err := mailClient.SendDraft(resolved.Account.Value, args[0]); err != nil {
			return fmt.Errorf("send draft: %w", err)
		}
		return writer.Write(output.Result{
			Data:    map[string]any{"draftId": args[0], "sent": true},
			Summary: "Sent draft " + args[0],
			Plain:   renderLine("Sent draft %s", args[0]),
		})
	},
}

var draftsDeleteCmd = &cobra.Command{
	Use:   "delete <draft-id>",
	Short: "Delete a draft",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if draftDryRun {
			return writer.Write(output.Result{
				Data:    map[string]any{"dryRun": true, "draftId": args[0], "action": "delete"},
				Summary: "Dry run: would delete draft " + args[0],
				Plain:   renderLine("Dry run: would delete draft %s", args[0]),
			})
		}
		if err := mailClient.DeleteDraft(resolved.Account.Value, args[0]); err != nil {
			return fmt.Errorf("delete draft: %w", err)
		}
		return writer.Write(output.Result{
			Data:    map[string]any{"draftId": args[0], "deleted": true},
			Summary: "Deleted draft " + args[0],
			Plain:   renderLine("Deleted draft %s", args[0]),
		})
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
	draftsListCmd.Flags().IntVarP(&draftLimit, "limit", "l", 25, "Maximum drafts")
	for _, cmd := range []*cobra.Command{draftsCreateCmd, draftsUpdateCmd} {
		cmd.Flags().StringVar(&draftSubject, "subject", "", "Draft subject")
		cmd.Flags().StringVar(&draftBody, "body", "", "Draft body")
		cmd.Flags().StringVar(&draftBodyFile, "body-file", "", "Read the draft body from a file")
	}
	for _, cmd := range []*cobra.Command{draftsCreateCmd, draftsUpdateCmd, draftsSendCmd, draftsDeleteCmd} {
		cmd.Flags().BoolVar(&draftDryRun, "dry-run", false, "Report what would change without touching Mail.app")
	}
	draftsCreateCmd.Flags().StringSliceVar(&draftTo, "to", []string{}, "To recipient (repeatable)")
	draftsCreateCmd.Flags().StringSliceVar(&draftCc, "cc", []string{}, "Cc recipient (repeatable)")
	draftsCreateCmd.Flags().StringSliceVar(&draftBcc, "bcc", []string{}, "Bcc recipient (repeatable)")
}
