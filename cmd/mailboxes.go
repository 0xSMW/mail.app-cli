package cmd

import (
	"encoding/base64"
	"fmt"

	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/cache"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// mailboxCacheKey returns a filename-safe, collision-free key for a scoped
// mailbox listing. Versioning keeps the old lossy sanitized-key namespace out
// of service so a cache entry for one account cannot be reused by another.
func mailboxCacheKey(account string) string {
	if account == "" {
		return "mailboxes"
	}
	return "mailboxes-account-v2-" + base64.RawURLEncoding.EncodeToString([]byte(account))
}

func accountOrAll(account string) string {
	if account == "" {
		return "all"
	}
	return account
}

func (a *application) buildMailboxesCommands() {
	var (
		mailboxNoCache      bool
		mailboxForceRefresh bool
	)

	a.mailboxesCmd = &cobra.Command{
		Use:   "mailboxes",
		Short: "List mailboxes and unread counts",
	}
	a.mailboxesListCmd = &cobra.Command{
		Use:   "list",
		Short: "List mailboxes for all accounts, or one with --account",
		Annotations: map[string]string{
			annotationAgentNotes: "Counts come from the Envelope Index when readable. 'name' is what --mailbox and 'move --to' expect. Gmail's archive is named 'All Mail'.",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			account := a.resolved.Account.Value
			mailboxes, source, err := loadCached(mailboxCacheKey(account), mailboxNoCache, mailboxForceRefresh, false, cache.DefaultTTL, func() ([]mail.Mailbox, error) {
				return a.mailClient.GetMailboxesJSON(account)
			})
			if err != nil {
				return fmt.Errorf("get mailboxes: %w", err)
			}
			return a.writer.Write(output.Result{
				Data:    mailboxes,
				Summary: plural(len(mailboxes), "mailbox"),
				Meta:    map[string]any{"source": source, "account": accountOrAll(account)},
				Plain:   renderMailboxes(mailboxes),
			})
		},
	}

	a.mailboxesCmd.AddCommand(a.mailboxesListCmd)
	registerCacheFlags(a.mailboxesListCmd, &mailboxNoCache, &mailboxForceRefresh)
}
