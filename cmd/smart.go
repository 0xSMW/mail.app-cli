package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	smartLimit int
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

func init() {
	smartCmd.AddCommand(smartListCmd, smartShowCmd, smartQueryCmd)
	smartQueryCmd.Flags().IntVarP(&smartLimit, "limit", "l", 50, "Maximum messages")
}
