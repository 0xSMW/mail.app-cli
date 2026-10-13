package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	sendTo          []string
	sendCc          []string
	sendBcc         []string
	sendSubject     string
	sendBody        string
	sendBodyFile    string
	sendSignature   string
	sendAttachments []string
	sendDryRun      bool
	sendFrom        string
	sendThreading   threadedOutgoingFlags
)

type sendReceipt struct {
	Account     string          `json:"account"`
	AccountID   string          `json:"accountId,omitempty"`
	Sender      string          `json:"sender,omitempty"`
	To          []string        `json:"to"`
	Cc          []string        `json:"cc"`
	Bcc         []string        `json:"bcc"`
	Subject     string          `json:"subject"`
	Body        string          `json:"body"`
	Attachments []string        `json:"attachments"`
	Signature   string          `json:"signature,omitempty"`
	DryRun      bool            `json:"dryRun"`
	Sent        bool            `json:"sent"`
	Submitted   bool            `json:"submitted"`
	Status      string          `json:"status"`
	Uncertain   bool            `json:"uncertain,omitempty"`
	Threading   *threadedSource `json:"threading,omitempty"`
}

var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send an email through Mail.app",
	Long: `Send an email from the account in scope.

Examples:
  mail-app-cli send -t user@example.com -s "Hello" --body "Message content"
  mail-app-cli send -a "Gmail" -t a@example.com -t b@example.com -s "Multi" --body-file body.md
  mail-app-cli send -t user@example.com -s "Files" --body "See attached" --attach ~/file.pdf --dry-run
  mail-app-cli send -a "Personal" -m INBOX --reply-to 123 --reply-all -t user@example.com -s "Re: Hello" --body "Complete reply body"`,
	Annotations: map[string]string{
		annotationAgentNotes: "Sending is not idempotent and cannot be undone. Run with --dry-run to see the exact message first. Use 'drafts create' when a person should review before sending.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		threading, err := sendThreading.selection(cmd)
		if err != nil {
			return err
		}
		account, err := requireAccount()
		if err != nil {
			return err
		}
		if len(sendTo) == 0 {
			return clierr.Usage("at least one --to recipient is required")
		}
		if strings.TrimSpace(sendSubject) == "" {
			return clierr.Usage("--subject is required")
		}
		body, err := readBodyValue(sendBody, sendBodyFile)
		if err != nil {
			return err
		}
		if sendSignature != "" {
			signature, err := mailClient.SignatureByName(sendSignature)
			if err != nil {
				return err
			}
			if signature.Content == "" {
				return clierr.New(clierr.CodeNotFound, fmt.Sprintf("signature %q has no readable content", sendSignature))
			}
			body = strings.TrimRight(body, "\r\n") + "\n\n" + signature.Content
		}
		if threading != nil {
			threading.Account = account
		}
		receipt := sendReceipt{
			Account: account, To: sendTo, Cc: nonNil(sendCc), Bcc: nonNil(sendBcc),
			Subject: sendSubject, Body: body, Attachments: nonNil(sendAttachments), Signature: sendSignature,
			DryRun: sendDryRun, Threading: threading,
		}
		input := mail.OutgoingInput{Account: account, Sender: sendFrom, Subject: sendSubject, Body: body,
			To: sendTo, Cc: sendCc, Bcc: sendBcc, Attachments: sendAttachments}
		var resolvedOutgoing *mail.OutgoingResult
		if sendDryRun {
			resolvedOutgoing, err = mailClient.PreviewOutgoing(input)
		} else if threading != nil {
			resolvedOutgoing, err = mailClient.SendThreadedOutgoing(threading.input(input))
		} else {
			resolvedOutgoing, err = mailClient.SendOutgoing(input)
		}
		if resolvedOutgoing != nil {
			receipt.Account, receipt.AccountID, receipt.Sender = resolvedOutgoing.Account, resolvedOutgoing.AccountID, resolvedOutgoing.Sender
			receipt.Status, receipt.Sent = resolvedOutgoing.Status, resolvedOutgoing.Submitted
		}
		if err != nil {
			var mutation *mail.MutationError
			if errors.As(err, &mutation) {
				receipt.Uncertain = mutation.Uncertain
				receipt.Sent = mutation.Submitted
				receipt.Submitted = mutation.Submitted
				if receipt.Status == "" {
					receipt.Status = "failed"
				}
				return writer.Write(output.Result{Data: receipt, Summary: mutation.Error(), Err: clierr.Classify(err)})
			}
			return fmt.Errorf("send message: %w", err)
		}
		receipt.Submitted = receipt.Sent
		summary := fmt.Sprintf("Sent %q to %s", sendSubject, strings.Join(sendTo, ", "))
		if sendDryRun {
			summary = fmt.Sprintf("Dry run: would send %q to %s", sendSubject, strings.Join(sendTo, ", "))
		}
		if n := len(sendAttachments); n > 0 {
			summary += fmt.Sprintf(" with %s", plural(n, "attachment"))
		}
		return writer.Write(output.Result{
			Data:    receipt,
			Summary: summary,
			Notices: threadedPreviewNotices(threading, sendDryRun),
			Meta:    map[string]any{"account": account, "dryRun": sendDryRun},
			Plain: func(p *output.Printer) {
				if sendDryRun {
					p.Line("%s", p.Yellow(summary))
					p.KeyValues([][2]string{
						{"Account", receipt.Account}, {"From", receipt.Sender}, {"To", strings.Join(sendTo, ", ")}, {"Cc", strings.Join(sendCc, ", ")},
						{"Bcc", strings.Join(sendBcc, ", ")}, {"Subject", sendSubject}, {"Attachments", strings.Join(sendAttachments, ", ")},
					})
					p.Blank()
					p.Line("%s", body)
					return
				}
				p.Line("%s", p.Green(summary))
			},
		})
	},
}

func previewNotices(dryRun bool) []string {
	if dryRun {
		return []string{"Configured sending identity validated; compose sender and delivery route have not been tested."}
	}
	return nil
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func init() {
	sendThreading.register(sendCmd)
	sendCmd.Flags().StringSliceVarP(&sendTo, "to", "t", []string{}, "To recipient (repeatable)")
	sendCmd.Flags().StringSliceVarP(&sendCc, "cc", "c", []string{}, "Cc recipient (repeatable)")
	sendCmd.Flags().StringSliceVarP(&sendBcc, "bcc", "b", []string{}, "Bcc recipient (repeatable)")
	sendCmd.Flags().StringVarP(&sendSubject, "subject", "s", "", "Subject (required)")
	sendCmd.Flags().StringVar(&sendBody, "body", "", "Body text")
	sendCmd.Flags().StringVar(&sendBodyFile, "body-file", "", "Read the body from a file")
	sendCmd.Flags().StringVar(&sendSignature, "signature", "", "Append a Mail.app signature by name")
	sendCmd.Flags().StringSliceVar(&sendAttachments, "attach", []string{}, "File to attach (repeatable)")
	sendCmd.Flags().BoolVar(&sendDryRun, "dry-run", false, "Show the composed message without sending")
	sendCmd.Flags().StringVar(&sendFrom, "from", "", "Sender address configured on the selected account (default: first address)")
}
