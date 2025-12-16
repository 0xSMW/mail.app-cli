package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/robertmeta/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	mailboxAccount string
)

var mailboxesCmd = &cobra.Command{
	Use:   "mailboxes",
	Short: "Manage Mail.app mailboxes",
	Long:  `View and manage your Mail.app mailboxes.`,
}

var mailboxesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all mailboxes",
	Long:  `List all mailboxes across all accounts or for a specific account. Output is JSON format. Use jq for pretty printing: mail-app-cli mailboxes list | jq`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		mailboxes, err := client.GetMailboxesJSON(mailboxAccount)
		if err != nil {
			return fmt.Errorf("failed to get mailboxes: %w", err)
		}

		output, err := json.MarshalIndent(mailboxes, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal mailboxes: %w", err)
		}

		fmt.Println(string(output))
		return nil
	},
}

func init() {
	mailboxesCmd.AddCommand(mailboxesListCmd)
	mailboxesListCmd.Flags().StringVarP(&mailboxAccount, "account", "a", "", "Filter by account name")
}
