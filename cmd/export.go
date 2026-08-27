package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

type messageExport struct {
	Metadata exportMetadata `json:"metadata"`
	Messages []mail.Message `json:"messages"`
}

type exportMetadata struct {
	Account     string    `json:"account"`
	Mailbox     string    `json:"mailbox"`
	Format      string    `json:"format"`
	ExportedAt  time.Time `json:"exportedAt"`
	Limit       int       `json:"limit"`
	Offset      int       `json:"offset"`
	Since       string    `json:"since,omitempty"`
	UnreadOnly  bool      `json:"unreadOnly"`
	FlaggedOnly bool      `json:"flaggedOnly"`
}

type savedAttachment struct {
	MessageID string `json:"messageId"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
}

var (
	exportFormat  string
	exportOutput  string
	exportLimit   int
	exportOffset  int
	exportSince   string
	exportUnread  bool
	exportFlagged bool
)

var exportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export messages and attachments",
}

var exportMessagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "Export messages with bodies as JSON",
	RunE: func(cmd *cobra.Command, args []string) error {
		account, err := requireAccount()
		if err != nil {
			return err
		}
		mailbox := mailboxInScope()
		if exportFormat != "json" {
			return clierr.Usage("export messages supports --format json only; Mail.app's scripting layer does not expose raw eml/mbox")
		}
		messages, err := mailClient.GetMessagesJSON(account, mailbox, exportLimit, exportOffset, exportUnread, exportFlagged, true, exportSince)
		if err != nil {
			return fmt.Errorf("export messages: %w", err)
		}
		payload := messageExport{
			Metadata: exportMetadata{
				Account: account, Mailbox: mailbox, Format: exportFormat, ExportedAt: time.Now().UTC(),
				Limit: exportLimit, Offset: exportOffset, Since: exportSince, UnreadOnly: exportUnread, FlaggedOnly: exportFlagged,
			},
			Messages: messages,
		}
		if exportMessagesWritesFile() {
			if err := writeJSONFile(exportOutput, payload); err != nil {
				return err
			}
			return writer.Write(output.Result{
				Data:    map[string]any{"path": exportOutput, "messages": len(messages), "account": account, "mailbox": mailbox},
				Summary: fmt.Sprintf("Exported %s to %s", plural(len(messages), "message"), exportOutput),
				Plain:   renderLine("Exported %s to %s", plural(len(messages), "message"), exportOutput),
			})
		}
		return writer.Write(output.Result{
			Data:    payload,
			Summary: fmt.Sprintf("Exported %s from %s/%s", plural(len(messages), "message"), account, mailbox),
			Plain:   renderMessages(messages, false),
		})
	},
}

func exportMessagesWritesFile() bool {
	return exportOutput != "" && exportOutput != "-"
}

var exportAttachmentsCmd = &cobra.Command{
	Use:   "attachments",
	Short: "Save every attachment from selected messages into a directory",
	RunE: func(cmd *cobra.Command, args []string) error {
		account, err := requireAccount()
		if err != nil {
			return err
		}
		mailbox := mailboxInScope()
		if exportOutput == "" || exportOutput == "-" {
			return clierr.Usage("--output directory is required")
		}
		if err := os.MkdirAll(exportOutput, 0o755); err != nil {
			return err
		}
		messages, err := mailClient.GetMessagesJSON(account, mailbox, exportLimit, exportOffset, exportUnread, exportFlagged, false, exportSince)
		if err != nil {
			return fmt.Errorf("select messages: %w", err)
		}
		saved := []savedAttachment{}
		failed := 0
		used := map[string]int{}
		for _, message := range messages {
			attachments, err := mailClient.GetAttachmentsJSON(account, mailbox, message.ID)
			if err != nil {
				saved = append(saved, savedAttachment{MessageID: message.ID, Status: "failed", Error: err.Error()})
				failed++
				continue
			}
			for _, attachment := range attachments {
				name := deterministicAttachmentName(message, attachment.Name, used)
				path := filepath.Join(exportOutput, name)
				item := savedAttachment{MessageID: message.ID, Name: attachment.Name, Path: path}
				if err := mailClient.SaveAttachmentByIndex(account, mailbox, message.ID, attachment.Name, attachment.Index, path); err != nil {
					item.Status = "failed"
					item.Error = err.Error()
					failed++
				} else {
					item.Status = "succeeded"
				}
				saved = append(saved, item)
			}
		}
		rows := make([][]string, 0, len(saved))
		for _, item := range saved {
			rows = append(rows, []string{item.MessageID, item.Name, item.Status, firstNonEmpty(item.Error, item.Path)})
		}
		return writer.Write(attachmentExportResult(saved, exportOutput, failed, rows))
	},
}

func attachmentExportResult(saved []savedAttachment, exportOutput string, failed int, rows [][]string) output.Result {
	var failure error
	if attachmentExportFailed(failed) {
		failure = clierr.New(clierr.CodeMutationFailed, fmt.Sprintf("failed to export %d attachment item(s)", failed))
	}
	return output.Result{
		Data:    saved,
		Summary: fmt.Sprintf("Saved %d attachment(s) to %s, %d failed", len(saved)-failed, exportOutput, failed),
		Plain:   renderTable([]string{"MESSAGE", "NAME", "STATUS", "PATH"}, rows, "no attachments"),
		Err:     clierr.Classify(failure),
	}
}

func writeJSONFile(path string, payload any) error {
	data, err := marshalIndentedJSON(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func attachmentExportFailed(failed int) bool {
	return failed > 0
}

func deterministicAttachmentName(message mail.Message, attachmentName string, used map[string]int) string {
	date := strings.TrimSpace(message.DateReceived)
	if len(date) >= len("2006-01-02") {
		date = date[:len("2006-01-02")]
	}
	if date == "" {
		date = "unknown-date"
	}
	base := sanitizeFilename(date + "-" + message.ID + "-" + attachmentName)
	used[base]++
	if used[base] == 1 {
		return base
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return fmt.Sprintf("%s-%d%s", stem, used[base], ext)
}

func sanitizeFilename(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unnamed"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func init() {
	exportCmd.AddCommand(exportMessagesCmd, exportAttachmentsCmd)
	for _, cmd := range []*cobra.Command{exportMessagesCmd, exportAttachmentsCmd} {
		cmd.Flags().StringVar(&exportOutput, "output", "-", "Output file (messages) or directory (attachments)")
		cmd.Flags().IntVarP(&exportLimit, "limit", "l", 100, "Maximum messages to export")
		cmd.Flags().IntVarP(&exportOffset, "offset", "o", 0, "Messages to skip")
		cmd.Flags().StringVar(&exportSince, "since", "", "Only messages since date")
		cmd.Flags().BoolVar(&exportUnread, "unread", false, "Only unread messages")
		cmd.Flags().BoolVar(&exportFlagged, "flagged", false, "Only flagged messages")
	}
	exportMessagesCmd.Flags().StringVar(&exportFormat, "format", "json", "Export format: json")
}
