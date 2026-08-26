package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/cache"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	mailboxNoCache      bool
	mailboxForceRefresh bool
)

var mailboxesCmd = &cobra.Command{
	Use:   "mailboxes",
	Short: "List mailboxes and unread counts",
}

var mailboxesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List mailboxes for all accounts, or one with --account",
	Annotations: map[string]string{
		annotationAgentNotes: "Counts come from the Envelope Index when readable. 'name' is what --mailbox and 'move --to' expect. Gmail's archive is named 'All Mail'.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		account := resolved.Account.Value
		var c *cache.Cache
		var cacheErr error
		if !mailboxNoCache {
			c, cacheErr = cache.New()
		}
		cacheKey := "mailboxes"
		if account != "" {
			cacheKey = "mailboxes-" + sanitizeCacheKey(account)
		}
		var mailboxes []mail.Mailbox
		fromCache := false
		if !mailboxNoCache && !mailboxForceRefresh && cacheErr == nil {
			if found, err := c.Get(cacheKey, &mailboxes); err == nil && found {
				fromCache = true
			}
		}
		if !fromCache {
			var err error
			mailboxes, err = mailClient.GetMailboxesJSON(account)
			if err != nil {
				return fmt.Errorf("get mailboxes: %w", err)
			}
			if !mailboxNoCache && cacheErr == nil {
				_ = c.Set(cacheKey, mailboxes)
			}
		}
		source := "live"
		if fromCache {
			source = "cache"
		}
		return writer.Write(output.Result{
			Data:    mailboxes,
			Summary: plural(len(mailboxes), "mailbox"),
			Meta:    map[string]any{"source": source, "account": accountOrAll(account)},
			Plain:   renderMailboxes(mailboxes),
		})
	},
}

func accountOrAll(account string) string {
	if account == "" {
		return "all"
	}
	return account
}

func init() {
	mailboxesCmd.AddCommand(mailboxesListCmd)
	mailboxesListCmd.Flags().BoolVar(&mailboxNoCache, "no-cache", false, "Bypass the cache and read from Mail.app")
	mailboxesListCmd.Flags().BoolVar(&mailboxForceRefresh, "force-refresh", false, "Refresh the cache from Mail.app")
}
