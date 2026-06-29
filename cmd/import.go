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
	importPath    string
	importDryRun  bool
)

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Validate or import Mail.app data",
}

var importMessagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "Validate exported message JSON before import",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAccountAndMailbox(importAccount, importMailbox); err != nil {
			return err
		}
		if importFormat != "json" {
			if importPath != "" {
				return fmt.Errorf("import messages --path is accepted only with eml directory input, which is unsupported by this Mail.app scriptability layer")
			}
			return fmt.Errorf("import messages supports --format json validation only; eml import is unsupported by this Mail.app scriptability layer")
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
			"wouldImport":    importDryRun,
			"implementation": "validation-only",
		}
		if importDryRun {
			return printJSON(result, "import validation")
		}
		return fmt.Errorf("message import is validation-only because Mail.app does not expose reliable raw-message import through this local scriptability layer; rerun with --dry-run to inspect the validated input")
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
	importMessagesCmd.Flags().StringVar(&importPath, "path", "", "Directory path for eml imports")
	importMessagesCmd.Flags().BoolVar(&importDryRun, "dry-run", false, "Validate and report would-import items without mutation")
}
