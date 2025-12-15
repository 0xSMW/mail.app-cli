package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/robertmeta/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var accountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "Manage Mail.app accounts",
	Long:  `View and manage your Mail.app accounts.`,
}

var accountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all accounts",
	Long:  `List all Mail.app accounts.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		accounts, err := client.GetAccountsJSON()
		if err != nil {
			return fmt.Errorf("failed to get accounts: %w", err)
		}

		if len(accounts) == 0 {
			fmt.Println("No accounts found.")
			return nil
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
		fmt.Fprintln(w, "NAME\tEMAIL\tUSERNAME\tENABLED")
		fmt.Fprintln(w, "----\t-----\t--------\t-------")
		for _, acc := range accounts {
			enabled := "no"
			if acc.Enabled {
				enabled = "yes"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", acc.Name, acc.EmailAddress, acc.UserName, enabled)
		}
		w.Flush()

		return nil
	},
}

var accountsShowCmd = &cobra.Command{
	Use:   "show [account-name]",
	Short: "Show account details",
	Long:  `Show detailed information about a specific account.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		accountName := args[0]
		client := mail.NewClient()
		accounts, err := client.GetAccountsJSON()
		if err != nil {
			return fmt.Errorf("failed to get accounts: %w", err)
		}

		for _, acc := range accounts {
			if acc.Name == accountName {
				fmt.Printf("Name:          %s\n", acc.Name)
				fmt.Printf("Email:         %s\n", acc.EmailAddress)
				fmt.Printf("Username:      %s\n", acc.UserName)
				fmt.Printf("Enabled:       %t\n", acc.Enabled)
				fmt.Printf("ID:            %s\n", acc.ID)
				return nil
			}
		}

		return fmt.Errorf("account not found: %s", accountName)
	},
}

func init() {
	accountsCmd.AddCommand(accountsListCmd)
	accountsCmd.AddCommand(accountsShowCmd)
}
