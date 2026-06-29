package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
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

var (
	exportAccount string
	exportMailbox string
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
	Short: "Export Mail.app data",
}

var exportMessagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "Export messages as JSON",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAccountAndMailbox(exportAccount, exportMailbox); err != nil {
			return err
		}
		if exportFormat != "json" {
			return fmt.Errorf("export messages supports --format json; raw eml/mbox export is unsupported by this Mail.app scriptability layer")
		}
		client := mail.NewClient()
		messages, err := client.GetMessagesJSON(exportAccount, exportMailbox, exportLimit, exportOffset, exportUnread, exportFlagged, true, exportSince)
		if err != nil {
			return fmt.Errorf("failed to export messages: %w", err)
		}
		payload := messageExport{
			Metadata: exportMetadata{
				Account:     exportAccount,
				Mailbox:     exportMailbox,
				Format:      exportFormat,
				ExportedAt:  time.Now().UTC(),
				Limit:       exportLimit,
				Offset:      exportOffset,
				Since:       exportSince,
				UnreadOnly:  exportUnread,
				FlaggedOnly: exportFlagged,
			},
			Messages: messages,
		}
		if exportOutput == "" || exportOutput == "-" {
			return printJSON(payload, "message export")
		}
		return writeJSONFile(exportOutput, payload, "message export")
	},
}

var exportAttachmentsCmd = &cobra.Command{
	Use:   "attachments",
	Short: "Export attachments from selected messages",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAccountAndMailbox(exportAccount, exportMailbox); err != nil {
			return err
		}
		if exportOutput == "" {
			return fmt.Errorf("--output directory is required")
		}
		if err := os.MkdirAll(exportOutput, 0o755); err != nil {
			return err
		}
		client := mail.NewClient()
		messages, err := client.GetMessagesJSON(exportAccount, exportMailbox, exportLimit, exportOffset, exportUnread, exportFlagged, false, exportSince)
		if err != nil {
			return fmt.Errorf("failed to select messages: %w", err)
		}
		type savedAttachment struct {
			MessageID string `json:"messageId"`
			Name      string `json:"name"`
			Path      string `json:"path"`
			Status    string `json:"status"`
			Error     string `json:"error,omitempty"`
		}
		var saved []savedAttachment
		failed := 0
		used := map[string]int{}
		for _, message := range messages {
			attachments, err := client.GetAttachmentsJSON(exportAccount, exportMailbox, message.ID)
			if err != nil {
				saved = append(saved, savedAttachment{MessageID: message.ID, Status: "failed", Error: err.Error()})
				failed++
				continue
			}
			for _, attachment := range attachments {
				name := deterministicAttachmentName(message, attachment.Name, used)
				path := filepath.Join(exportOutput, name)
				item := savedAttachment{MessageID: message.ID, Name: attachment.Name, Path: path}
				if err := client.SaveAttachmentByIndex(exportAccount, exportMailbox, message.ID, attachment.Name, attachment.Index, path); err != nil {
					item.Status = "failed"
					item.Error = err.Error()
					failed++
				} else {
					item.Status = "succeeded"
				}
				saved = append(saved, item)
			}
		}
		if err := printJSON(saved, "attachment export"); err != nil {
			return err
		}
		if attachmentExportFailed(failed) {
			return fmt.Errorf("failed to export %d attachment item(s)", failed)
		}
		return nil
	},
}

func writeJSONFile(path string, payload any, label string) error {
	data, err := marshalIndentedJSON(payload, label)
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
	exportCmd.AddCommand(exportMessagesCmd)
	exportCmd.AddCommand(exportAttachmentsCmd)
	for _, cmd := range []*cobra.Command{exportMessagesCmd, exportAttachmentsCmd} {
		cmd.Flags().StringVarP(&exportAccount, "account", "a", "", "Account name")
		cmd.Flags().StringVarP(&exportMailbox, "mailbox", "m", "", "Mailbox name")
		cmd.Flags().StringVar(&exportOutput, "output", "-", "Output file or directory")
		cmd.Flags().IntVarP(&exportLimit, "limit", "l", 100, "Maximum messages to export")
		cmd.Flags().IntVarP(&exportOffset, "offset", "o", 0, "Number of messages to skip")
		cmd.Flags().StringVar(&exportSince, "since", "", "Only messages since date")
		cmd.Flags().BoolVar(&exportUnread, "unread", false, "Only unread messages")
		cmd.Flags().BoolVar(&exportFlagged, "flagged", false, "Only flagged messages")
	}
	exportMessagesCmd.Flags().StringVar(&exportFormat, "format", "json", "Export format: json")
}
