package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/pkg/cache"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
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
		var c *cache.Cache
		var cacheErr error
		if !accountsNoCache {
			c, cacheErr = cache.New()
		}

		// Try to get from cache if not disabled
		if !accountsNoCache && !accountsForceRefresh && cacheErr == nil {
			found, err := c.Get("accounts", &accounts)
			if err == nil && found {
				return printJSON(accounts, "accounts")
			}
		}

		// Get from Mail.app
		client := mail.NewClient()
		accounts, err := client.GetAccountsJSON()
		if err != nil {
			return fmt.Errorf("failed to get accounts: %w", err)
		}

		// Save to cache if not disabled
		if !accountsNoCache && cacheErr == nil {
			c.Set("accounts", accounts)
		}

		return printJSON(accounts, "accounts")
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
				return printJSON(acc, "account")
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
