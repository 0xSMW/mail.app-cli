package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

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

func (a *application) buildDraftsCommands() {
	var (
		draftLimit       int
		draftThreading   threadedOutgoingFlags
		draftTo          []string
		draftCc          []string
		draftBcc         []string
		draftBody        string
		draftBodyFile    string
		draftAttachments []string
		draftFrom        string
		draftSubject     string
		draftDryRun      bool
	)

	a.draftsCmd = &cobra.Command{
		Use:   "drafts",
		Short: "Create and manage drafts",
		Annotations: map[string]string{
			annotationAgentNotes: "Drafts are the review-before-send lane: create, have a person look, then 'drafts send <id>'. Creation waits for Mail.app to save the draft and verify attached files. Repeat --attach for multiple files; updates preserve existing attachments.",
		},
	}
	a.draftsListCmd = &cobra.Command{
		Use:   "list",
		Short: "List drafts across accounts, or one with --account",
		RunE: func(cmd *cobra.Command, args []string) error {
			account := a.resolved.Account.Value
			var messages []mail.Message
			var err error
			if account != "" {
				messages, err = a.mailClient.GetMessagesJSON(account, "Drafts", draftLimit, 0, false, false, true, "")
			} else {
				messages, err = a.mailClient.GetUnifiedMessagesJSON("drafts", draftLimit, 0, true)
			}
			if err != nil {
				return fmt.Errorf("list drafts: %w", err)
			}
			return a.writer.Write(output.Result{
				Data:    messages,
				Summary: plural(len(messages), "draft"),
				Meta:    map[string]any{"account": accountOrAll(account)},
				Plain:   renderMessages(messages, account == ""),
			})
		},
	}
	a.draftsCreateCmd = &cobra.Command{
		Use:   "create",
		Short: "Create a draft without sending it",
		Long: `Create a draft without sending it. Reply and forward use the native backend
and require the original message's explicit mailbox and a complete outgoing body.

Examples:
  mail-app-cli drafts create -a "Personal" -m INBOX --reply-to 123 --to user@example.com --subject "Re: Hello" --body "Complete reply body"
  mail-app-cli drafts create -a "Personal" -m INBOX --forward-of 123 --to user@example.com --subject "Fwd: Hello" --body-file forward.txt`,
		RunE: func(cmd *cobra.Command, args []string) error {
			threading, err := draftThreading.selection(a, cmd)
			if err != nil {
				return err
			}
			if threading != nil && len(draftTo)+len(draftCc)+len(draftBcc) == 0 {
				return clierr.Usage("reply and forward require at least one recipient")
			}
			account, err := a.requireAccount()
			if err != nil {
				return err
			}
			body, err := readBodyValue(draftBody, draftBodyFile)
			if err != nil {
				return err
			}
			attachments, err := mail.ValidateDraftAttachments(draftAttachments)
			if err != nil {
				return err
			}
			input := mail.DraftInput{Account: account, Sender: draftFrom, Subject: draftSubject, Body: body, To: draftTo, Cc: draftCc, Bcc: draftBcc, Attachments: attachments}
			if threading != nil {
				threading.Account = account
			}
			if draftDryRun {
				identity, err := a.mailClient.PreviewOutgoing(mail.OutgoingInput{Account: account, Sender: draftFrom, Subject: draftSubject, Body: body, To: draftTo, Cc: draftCc, Bcc: draftBcc, Attachments: attachments})
				if err != nil {
					return err
				}
				input.Sender = identity.Sender
				data := map[string]any{"dryRun": true, "draft": input, "accountId": identity.AccountID}
				if threading != nil {
					data["threading"] = threading
				}
				return a.writer.Write(output.Result{
					Data:    data,
					Summary: "Dry run: would create draft " + input.Subject,
					Notices: threadedPreviewNotices(threading, true),
					Plain:   renderLine("Dry run: would create draft %q for %v", input.Subject, input.To),
				})
			}
			var message *mail.Message
			if threading != nil {
				var result *mail.OutgoingResult
				result, err = a.mailClient.SaveThreadedOutgoing(threading.input(mail.OutgoingInput{Account: account, Sender: draftFrom, Subject: draftSubject, Body: body, To: draftTo, Cc: draftCc, Bcc: draftBcc, Attachments: attachments}))
				if result != nil {
					message = result.Draft
				}
			} else {
				message, err = a.mailClient.CreateDraft(input)
			}
			if err != nil {
				var mutation *mail.MutationError
				if errors.As(err, &mutation) {
					data := map[string]any{"account": account, "sender": mutation.Sender, "draft": message,
						"saved": mutation.Saved, "uncertain": mutation.Uncertain, "status": "verification_failed", "phase": mutation.Phase}
					if threading != nil {
						data["threading"] = threading
					}
					return a.writer.Write(output.Result{
						Data:    data,
						Summary: mutation.Error(), Err: clierr.Classify(err),
					})
				}
				return fmt.Errorf("create draft: %w", err)
			}
			meta := map[string]any{"account": account}
			if threading != nil {
				meta["threading"] = threading
			}
			return a.writer.Write(output.Result{
				Data:    message,
				Summary: "Created draft " + message.ID,
				Meta:    meta,
				Plain:   renderLine("Created draft %s (%s)", message.ID, message.Subject),
			})
		},
	}
	a.draftsShowCmd = &cobra.Command{
		Use:   "show <draft-id>",
		Short: "Show a draft",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			message, err := a.mailClient.GetDraft(a.resolved.Account.Value, args[0])
			if err != nil {
				return err
			}
			return a.writer.Write(output.Result{
				Data:    message,
				Summary: "Draft " + message.ID + ": " + message.Subject,
				Plain:   renderMessage(message, false),
			})
		},
	}
	a.draftsUpdateCmd = &cobra.Command{
		Use:   "update <draft-id>",
		Short: "Update a draft's subject or body, or add attachments",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readBodyValue(draftBody, draftBodyFile)
			if err != nil {
				return err
			}
			attachments, err := mail.ValidateDraftAttachments(draftAttachments)
			if err != nil {
				return err
			}
			input := mail.DraftInput{
				Attachments: attachments,
				Subject:     draftSubject,
				Body:        body,
				SubjectSet:  cmd.Flags().Changed("subject"),
				BodySet:     cmd.Flags().Changed("body") || cmd.Flags().Changed("body-file"),
			}
			if draftDryRun {
				return a.writer.Write(output.Result{
					Data:    map[string]any{"dryRun": true, "draftId": args[0], "updates": input},
					Summary: "Dry run: would update draft " + args[0],
					Plain:   renderLine("Dry run: would update draft %s", args[0]),
				})
			}
			message, err := a.mailClient.UpdateDraft(a.resolved.Account.Value, args[0], input)
			if err != nil {
				return fmt.Errorf("update draft: %w", err)
			}
			return a.writer.Write(output.Result{
				Data:    message,
				Summary: "Updated draft, now " + message.ID,
				Plain:   renderLine("Updated draft; new id %s", message.ID),
			})
		},
	}
	a.draftsSendCmd = &cobra.Command{
		Use:   "send <draft-id>",
		Short: "Send an existing draft",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if draftDryRun {
				identity, err := a.mailClient.PreviewDraftSend(a.resolved.Account.Value, args[0])
				if err != nil {
					return err
				}
				return a.writer.Write(output.Result{
					Data:    map[string]any{"dryRun": true, "draftId": args[0], "action": "send", "account": identity.Account, "accountId": identity.AccountID, "sender": identity.Sender},
					Summary: "Dry run: would send draft " + args[0],
					Notices: previewNotices(true),
					Plain:   renderLine("Dry run: would send draft %s", args[0]),
				})
			}
			result, err := a.mailClient.SendDraftResult(a.resolved.Account.Value, args[0])
			if err != nil {
				if result != nil {
					return a.writer.Write(output.Result{Data: result, Summary: err.Error(), Err: clierr.Classify(err)})
				}
				return fmt.Errorf("send draft: %w", err)
			}
			return a.writer.Write(output.Result{
				Data:    result,
				Summary: "Sent draft " + args[0],
				Plain:   renderLine("Sent draft %s", args[0]),
			})
		},
	}
	a.draftsDeleteCmd = &cobra.Command{
		Use:   "delete <draft-id>",
		Short: "Delete a draft",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if draftDryRun {
				return a.writer.Write(output.Result{
					Data:    map[string]any{"dryRun": true, "draftId": args[0], "action": "delete"},
					Summary: "Dry run: would delete draft " + args[0],
					Plain:   renderLine("Dry run: would delete draft %s", args[0]),
				})
			}
			if err := a.mailClient.DeleteDraft(a.resolved.Account.Value, args[0]); err != nil {
				return fmt.Errorf("delete draft: %w", err)
			}
			return a.writer.Write(output.Result{
				Data:    map[string]any{"draftId": args[0], "deleted": true},
				Summary: "Deleted draft " + args[0],
				Plain:   renderLine("Deleted draft %s", args[0]),
			})
		},
	}

	draftThreading.register(a.draftsCreateCmd)
	a.draftsCmd.AddCommand(a.draftsListCmd, a.draftsCreateCmd, a.draftsShowCmd, a.draftsUpdateCmd, a.draftsSendCmd, a.draftsDeleteCmd)
	a.draftsListCmd.Flags().IntVarP(&draftLimit, "limit", "l", 25, "Maximum drafts")
	for _, cmd := range []*cobra.Command{a.draftsCreateCmd, a.draftsUpdateCmd} {
		cmd.Flags().StringVar(&draftSubject, "subject", "", "Draft subject")
		cmd.Flags().StringVar(&draftBody, "body", "", "Draft body")
		cmd.Flags().StringVar(&draftBodyFile, "body-file", "", "Read the draft body from a file")
		cmd.Flags().StringArrayVar(&draftAttachments, "attach", nil, "File to attach (repeatable; update preserves existing attachments)")
	}
	for _, cmd := range []*cobra.Command{a.draftsCreateCmd, a.draftsUpdateCmd, a.draftsSendCmd, a.draftsDeleteCmd} {
		cmd.Flags().BoolVar(&draftDryRun, "dry-run", false, "Report what would change without touching Mail.app")
	}
	a.draftsCreateCmd.Flags().StringSliceVar(&draftTo, "to", nil, "To recipient (repeatable)")
	a.draftsCreateCmd.Flags().StringSliceVar(&draftCc, "cc", nil, "Cc recipient (repeatable)")
	a.draftsCreateCmd.Flags().StringSliceVar(&draftBcc, "bcc", nil, "Bcc recipient (repeatable)")
	a.draftsCreateCmd.Flags().StringVar(&draftFrom, "from", "", "Sender address configured on the selected account (default: first address)")
}
