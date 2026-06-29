package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	importAccount string
	importMailbox string
	importFormat  string
	importFile    string
	importDryRun  bool
)

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Validate exported Mail.app data",
}

var importMessagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "Validate exported message JSON before import",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAccountAndMailbox(importAccount, importMailbox); err != nil {
			return err
		}
		if importFormat != "json" {
			return fmt.Errorf("import messages validates exported JSON only")
		}
		if importFile == "" {
			return fmt.Errorf("--file is required")
		}
		data, err := os.ReadFile(importFile)
		if err != nil {
			return err
		}
		messages, err := parseImportMessages(data)
		if err != nil {
			return err
		}
		result := map[string]any{
			"account":        importAccount,
			"mailbox":        importMailbox,
			"format":         importFormat,
			"validated":      len(messages),
			"implementation": "validation-only",
		}
		if importDryRun {
			result["dryRun"] = true
		}
		return printJSON(result, "import validation")
	},
}

func parseImportMessages(data []byte) ([]mail.Message, error) {
	var direct []mail.Message
	if err := json.Unmarshal(data, &direct); err == nil {
		return direct, nil
	}

	var payload struct {
		Messages []mail.Message `json:"messages"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("invalid message export JSON: %w", err)
	}
	if payload.Messages == nil {
		return nil, fmt.Errorf("message export must contain a messages array")
	}
	return payload.Messages, nil
}

func init() {
	importCmd.AddCommand(importMessagesCmd)
	importMessagesCmd.Flags().StringVarP(&importAccount, "account", "a", "", "Target account")
	importMessagesCmd.Flags().StringVarP(&importMailbox, "mailbox", "m", "", "Target mailbox")
	importMessagesCmd.Flags().StringVar(&importFormat, "format", "json", "Import format: json")
	importMessagesCmd.Flags().StringVar(&importFile, "file", "", "Export JSON file")
	importMessagesCmd.Flags().BoolVar(&importDryRun, "dry-run", false, "Mark validation output as a dry run")
}
