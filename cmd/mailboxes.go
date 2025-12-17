package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/robertmeta/mail-app-cli/pkg/cache"
	"github.com/robertmeta/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	mailboxAccount      string
	mailboxNoCache      bool
	mailboxForceRefresh bool
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
		var mailboxes []mail.Mailbox

		// Determine cache key based on whether account is specified
		cacheKey := "mailboxes"
		if mailboxAccount != "" {
			cacheKey = fmt.Sprintf("mailboxes-%s", mailboxAccount)
		}

		// Try to get from cache if not disabled
		if !mailboxNoCache && !mailboxForceRefresh {
			c, err := cache.New()
			if err == nil {
				found, err := c.Get(cacheKey, &mailboxes)
				if err == nil && found {
					output, err := json.MarshalIndent(mailboxes, "", "  ")
					if err != nil {
						return fmt.Errorf("failed to marshal mailboxes: %w", err)
					}
					fmt.Println(string(output))
					return nil
				}
			}
		}

		// Get from Mail.app
		client := mail.NewClient()
		mailboxes, err := client.GetMailboxesJSON(mailboxAccount)
		if err != nil {
			return fmt.Errorf("failed to get mailboxes: %w", err)
		}

		// Save to cache if not disabled
		if !mailboxNoCache {
			c, err := cache.New()
			if err == nil {
				c.Set(cacheKey, mailboxes)
			}
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
	mailboxesListCmd.Flags().BoolVar(&mailboxNoCache, "no-cache", false, "Bypass cache and fetch fresh data")
	mailboxesListCmd.Flags().BoolVar(&mailboxForceRefresh, "force-refresh", false, "Force refresh cache with fresh data")
}
