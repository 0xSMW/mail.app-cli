package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	importFormat string
	importFile   string
	importDryRun bool
)

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Validate exported Mail.app data",
}

var importMessagesCmd = &cobra.Command{
	Use:   "messages",
	Short: "Validate an exported message JSON file (validation only, nothing is written to Mail.app)",
	RunE: func(cmd *cobra.Command, args []string) error {
		account, err := requireAccount()
		if err != nil {
			return err
		}
		if importFormat != "json" {
			return clierr.Usage("import messages validates exported JSON only")
		}
		if importFile == "" {
			return clierr.Usage("--file is required")
		}
		data, err := os.ReadFile(importFile)
		if err != nil {
			return err
		}
		messages, err := parseImportMessages(data)
		if err != nil {
			return clierr.Wrap(clierr.CodeUsage, err, err.Error())
		}
		result := map[string]any{
			"account":        account,
			"mailbox":        mailboxInScope(),
			"format":         importFormat,
			"validated":      len(messages),
			"implementation": "validation-only",
		}
		if importDryRun {
			result["dryRun"] = true
		}
		return writer.Write(output.Result{
			Data:    result,
			Summary: fmt.Sprintf("Validated %s in %s", plural(len(messages), "message"), importFile),
			Plain:   renderLine("Validated %s in %s (nothing written to Mail.app)", plural(len(messages), "message"), importFile),
		})
	},
}

func parseImportMessages(data []byte) ([]mail.Message, error) {
	messages, err := parseImportMessagePayload(data)
	if err == nil {
		return messages, nil
	}

	// JSON command output is wrapped in the standard envelope. Accept the
	// envelope emitted by `export messages` on stdout as well as the raw export
	// payload written with --output.
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) == nil && len(envelope.Data) > 0 {
		return parseImportMessagePayload(envelope.Data)
	}
	return nil, err
}

func parseImportMessagePayload(data []byte) ([]mail.Message, error) {
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
	importMessagesCmd.Flags().StringVar(&importFormat, "format", "json", "Import format: json")
	importMessagesCmd.Flags().StringVar(&importFile, "file", "", "Export JSON file")
	importMessagesCmd.Flags().BoolVar(&importDryRun, "dry-run", false, "Mark the validation output as a dry run")
}
