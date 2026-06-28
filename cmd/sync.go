package cmd

import (
	"fmt"
	"time"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	syncAccount string
	syncMailbox string
	syncWait    bool
	syncJSON    bool
	syncTimeout int
)

type syncResult struct {
	Account          string    `json:"account,omitempty"`
	RequestedMailbox string    `json:"requestedMailbox,omitempty"`
	ActualScope      string    `json:"actualScope"`
	StartedAt        time.Time `json:"startedAt"`
	EndedAt          time.Time `json:"endedAt"`
	Status           string    `json:"status"`
	Error            string    `json:"error,omitempty"`
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Force Mail.app to synchronize accounts",
	Long: `Force Mail.app to synchronize one or all accounts with mail servers.
Examples:
  mail-app-cli sync
  mail-app-cli sync --account "Gmail"
  mail-app-cli sync --account "Gmail" --mailbox INBOX --wait --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		result := syncResult{
			Account:          syncAccount,
			RequestedMailbox: syncMailbox,
			ActualScope:      "all-accounts",
			StartedAt:        time.Now().UTC(),
			Status:           "running",
		}

		if syncAccount != "" {
			result.ActualScope = "account-requested; Mail.app may synchronize globally"
			if err := client.SyncAccount(syncAccount); err != nil {
				result.Status = "failed"
				result.Error = err.Error()
				result.EndedAt = time.Now().UTC()
				if syncJSON {
					_ = printJSON(result, "sync result")
				}
				return fmt.Errorf("failed to sync account %s: %w", syncAccount, err)
			}
		} else {
			if err := client.SyncAllAccounts(); err != nil {
				result.Status = "failed"
				result.Error = err.Error()
				result.EndedAt = time.Now().UTC()
				if syncJSON {
					_ = printJSON(result, "sync result")
				}
				return fmt.Errorf("failed to sync accounts: %w", err)
			}
		}

		if syncWait {
			if syncTimeout <= 0 {
				result.Status = "timeout"
				result.EndedAt = time.Now().UTC()
				if syncJSON {
					_ = printJSON(result, "sync result")
				}
				return fmt.Errorf("sync wait timed out")
			}
			if err := waitForSyncStability(client, syncAccount, syncMailbox, time.Duration(syncTimeout)*time.Second); err != nil {
				result.Status = "timeout"
				result.Error = err.Error()
				result.EndedAt = time.Now().UTC()
				if syncJSON {
					_ = printJSON(result, "sync result")
				}
				return err
			}
		}

		result.Status = "completed"
		result.EndedAt = time.Now().UTC()
		if syncJSON {
			return printJSON(result, "sync result")
		}
		if syncAccount != "" {
			fmt.Printf("Synced account request: %s (actual scope: %s)\n", syncAccount, result.ActualScope)
		} else {
			fmt.Println("Synced all accounts")
		}
		if syncMailbox != "" {
			fmt.Printf("Requested mailbox: %s\n", syncMailbox)
		}
		return nil
	},
}

var syncStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report sync status metadata",
	RunE: func(cmd *cobra.Command, args []string) error {
		now := time.Now().UTC()
		return printJSON(syncResult{
			Account:          syncAccount,
			RequestedMailbox: syncMailbox,
			ActualScope:      "all-accounts",
			StartedAt:        now,
			EndedAt:          now,
			Status:           "idle",
		}, "sync status")
	},
}

func waitForSyncStability(client *mail.Client, account, mailbox string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastCount := -1
	stableSamples := 0
	for {
		count, err := syncObservedCount(client, account, mailbox)
		if err != nil {
			return err
		}
		if count == lastCount {
			stableSamples++
		} else {
			stableSamples = 1
			lastCount = count
		}
		if stableSamples >= 2 {
			return nil
		}
		if time.Now().Add(2 * time.Second).After(deadline) {
			return fmt.Errorf("sync wait timed out after %s", timeout)
		}
		time.Sleep(2 * time.Second)
	}
}

func syncObservedCount(client *mail.Client, account, mailbox string) (int, error) {
	if mailbox != "" {
		if account == "" {
			return 0, fmt.Errorf("--mailbox requires --account for sync wait")
		}
		boxes, err := client.GetMailboxesJSON(account)
		if err != nil {
			return 0, err
		}
		for _, box := range boxes {
			if box.Name == mailbox {
				return box.TotalCount, nil
			}
		}
		return 0, fmt.Errorf("mailbox not found: %s", mailbox)
	}
	if account != "" {
		boxes, err := client.GetMailboxesJSON(account)
		if err != nil {
			return 0, err
		}
		total := 0
		for _, box := range boxes {
			total += box.TotalCount
		}
		return total, nil
	}
	return client.GetUnreadCount()
}

func init() {
	syncCmd.AddCommand(syncStatusCmd)
	syncCmd.Flags().StringVarP(&syncAccount, "account", "a", "", "Account to sync")
	syncCmd.Flags().StringVarP(&syncMailbox, "mailbox", "m", "", "Requested mailbox scope")
	syncCmd.Flags().BoolVar(&syncWait, "wait", false, "Wait briefly after triggering sync")
	syncCmd.Flags().BoolVar(&syncJSON, "json", false, "Print structured sync result")
	syncCmd.Flags().IntVar(&syncTimeout, "timeout", 60, "Maximum wait time in seconds")
	syncStatusCmd.Flags().StringVarP(&syncAccount, "account", "a", "", "Account name")
	syncStatusCmd.Flags().StringVarP(&syncMailbox, "mailbox", "m", "", "Mailbox name")
}
