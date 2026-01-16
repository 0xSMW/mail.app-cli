package cmd

import (
	"fmt"

	"github.com/intelligrit/mail-app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	syncAccount string
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Force Mail.app to synchronize accounts",
	Long: `Force Mail.app to synchronize one or all accounts with mail servers.
Examples:
  mail-app-cli sync                        # Sync all accounts
  mail-app-cli sync --account "Gmail"      # Sync specific account`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()

		if syncAccount != "" {
			err := client.SyncAccount(syncAccount)
			if err != nil {
				return fmt.Errorf("failed to sync account %s: %w", syncAccount, err)
			}
			fmt.Printf("Synced account: %s\n", syncAccount)
		} else {
			err := client.SyncAllAccounts()
			if err != nil {
				return fmt.Errorf("failed to sync accounts: %w", err)
			}
			fmt.Println("Synced all accounts")
		}

		return nil
	},
}

func init() {
	syncCmd.Flags().StringVarP(&syncAccount, "account", "a", "", "Account to sync (if not specified, syncs all accounts)")
}
