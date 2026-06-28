package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	smartDryRun bool
	smartQuery  string
	smartLimit  int
)

var smartCmd = &cobra.Command{
	Use:   "smart",
	Short: "List and inspect smart mailboxes",
}

var smartListCmd = &cobra.Command{
	Use:   "list",
	Short: "List smart mailboxes",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		boxes, err := client.ListSmartMailboxes()
		if err != nil {
			return fmt.Errorf("failed to list smart mailboxes: %w", err)
		}
		return printJSON(boxes, "smart mailboxes")
	},
}

var smartShowCmd = &cobra.Command{
	Use:   "show [name]",
	Short: "Show a smart mailbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		boxes, err := client.ListSmartMailboxes()
		if err != nil {
			return err
		}
		for _, box := range boxes {
			if box.Name == args[0] {
				return printJSON(box, "smart mailbox")
			}
		}
		return fmt.Errorf("smart mailbox not found: %s", args[0])
	},
}

var smartQueryCmd = &cobra.Command{
	Use:   "query [query]",
	Short: "Query messages using normal search semantics",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		messages, err := client.SearchMessagesJSON(args[0], "", "", smartLimit)
		if err != nil {
			return err
		}
		return printJSON(messages, "smart query messages")
	},
}

var smartCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a smart mailbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if smartDryRun {
			return printJSON(map[string]any{"dryRun": true, "name": args[0], "query": smartQuery}, "smart create dry-run")
		}
		return fmt.Errorf("smart create is unsupported because Mail.app smart mailbox criteria are not reliably writable through this local scriptability layer")
	},
}

var smartDeleteCmd = &cobra.Command{
	Use:   "delete [name]",
	Short: "Delete a smart mailbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if smartDryRun {
			return printJSON(map[string]any{"dryRun": true, "name": args[0], "action": "delete"}, "smart delete dry-run")
		}
		return fmt.Errorf("smart delete is unsupported because Mail.app smart mailbox mutation is not reliably exposed through this local scriptability layer")
	},
}

func init() {
	smartCmd.AddCommand(smartListCmd, smartShowCmd, smartQueryCmd, smartCreateCmd, smartDeleteCmd)
	smartQueryCmd.Flags().IntVarP(&smartLimit, "limit", "l", 50, "Maximum messages")
	smartCreateCmd.Flags().StringVar(&smartQuery, "query", "", "Search query for smart mailbox dry-run metadata")
	smartCreateCmd.Flags().BoolVar(&smartDryRun, "dry-run", false, "Show mutation without applying it")
	smartDeleteCmd.Flags().BoolVar(&smartDryRun, "dry-run", false, "Show mutation without applying it")
}
