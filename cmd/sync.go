package cmd

import (
	"fmt"
	"time"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	syncWait    bool
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
	Short: "Ask Mail.app to check for new mail",
	Long: `Ask Mail.app to check for new mail. Mail.app syncs every account
regardless of --account; the flag only scopes --wait, which polls counts until
they hold still for two samples.`,
	Args: cobra.NoArgs,
	Annotations: map[string]string{
		annotationAgentNotes: "Mail.app has no per-account sync; actualScope says what happened. Use --wait before reading a mailbox you expect to have changed.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		account := resolved.Account.Value
		mailbox := ""
		if mailboxExplicit() {
			mailbox = mailboxInScope()
		}
		result := syncResult{
			Account:          account,
			RequestedMailbox: mailbox,
			ActualScope:      "all-accounts",
			StartedAt:        time.Now().UTC(),
			Status:           "running",
		}
		finish := func(status string, err error) error {
			result.Status = status
			result.EndedAt = time.Now().UTC()
			var resultErr *clierr.Error
			if err != nil {
				result.Error = err.Error()
				resultErr = clierr.Classify(err)
			}
			summary := "Synced all accounts"
			if account != "" {
				summary = "Sync requested for " + account + " (Mail.app syncs globally)"
			}
			if status != "completed" {
				summary = "Sync " + status
			}
			writeErr := writer.Write(output.Result{
				Data:    result,
				Summary: summary,
				Meta:    map[string]any{"account": accountOrAll(account)},
				Plain:   renderLine("%s", summary),
				Err:     resultErr,
			})
			return writeErr
		}

		if account != "" {
			result.ActualScope = "account-requested; Mail.app may synchronize globally"
			if err := mailClient.SyncAccount(account); err != nil {
				return finish("failed", fmt.Errorf("sync account %s: %w", account, err))
			}
		} else if err := mailClient.SyncAllAccounts(); err != nil {
			return finish("failed", fmt.Errorf("sync accounts: %w", err))
		}

		if syncWait {
			if syncTimeout <= 0 {
				return finish("timeout", clierr.New(clierr.CodeTimeout, "sync wait timed out"))
			}
			if err := waitForSyncStability(mailClient, account, mailbox, time.Duration(syncTimeout)*time.Second); err != nil {
				return finish("timeout", clierr.Wrap(clierr.CodeTimeout, err, err.Error()))
			}
		}
		return finish("completed", nil)
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
			return 0, clierr.Usage("--mailbox requires --account for sync wait")
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
		return 0, clierr.New(clierr.CodeNotFound, "mailbox not found: "+mailbox)
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
	syncCmd.Flags().BoolVar(&syncWait, "wait", false, "Poll until mailbox counts hold still")
	syncCmd.Flags().IntVar(&syncTimeout, "timeout", 60, "Maximum --wait time in seconds")
}
