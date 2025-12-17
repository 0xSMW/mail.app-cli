package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/robertmeta/mail-app-cli/pkg/cache"
	"github.com/robertmeta/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	accountsNoCache      bool
	accountsForceRefresh bool
)

var accountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "Manage Mail.app accounts",
	Long:  `View and manage your Mail.app accounts.`,
}

var accountsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all accounts",
	Long:  `List all Mail.app accounts. Output is JSON format. Use jq for pretty printing: mail-app-cli accounts list | jq`,
	RunE: func(cmd *cobra.Command, args []string) error {
		var accounts []mail.Account

		// Try to get from cache if not disabled
		if !accountsNoCache && !accountsForceRefresh {
			c, err := cache.New()
			if err == nil {
				found, err := c.Get("accounts", &accounts)
				if err == nil && found {
					output, err := json.MarshalIndent(accounts, "", "  ")
					if err != nil {
						return fmt.Errorf("failed to marshal accounts: %w", err)
					}
					fmt.Println(string(output))
					return nil
				}
			}
		}

		// Get from Mail.app
		client := mail.NewClient()
		accounts, err := client.GetAccountsJSON()
		if err != nil {
			return fmt.Errorf("failed to get accounts: %w", err)
		}

		// Save to cache if not disabled
		if !accountsNoCache {
			c, err := cache.New()
			if err == nil {
				c.Set("accounts", accounts)
			}
		}

		output, err := json.MarshalIndent(accounts, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal accounts: %w", err)
		}

		fmt.Println(string(output))
		return nil
	},
}

var accountsShowCmd = &cobra.Command{
	Use:   "show [account-name]",
	Short: "Show account details",
	Long:  `Show detailed information about a specific account. Output is JSON format.`,
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
				output, err := json.MarshalIndent(acc, "", "  ")
				if err != nil {
					return fmt.Errorf("failed to marshal account: %w", err)
				}
				fmt.Println(string(output))
				return nil
			}
		}

		return fmt.Errorf("account not found: %s", accountName)
	},
}

func init() {
	accountsCmd.AddCommand(accountsListCmd)
	accountsCmd.AddCommand(accountsShowCmd)

	// Flags for accounts list
	accountsListCmd.Flags().BoolVar(&accountsNoCache, "no-cache", false, "Bypass cache and fetch fresh data")
	accountsListCmd.Flags().BoolVar(&accountsForceRefresh, "force-refresh", false, "Force refresh cache with fresh data")
}
