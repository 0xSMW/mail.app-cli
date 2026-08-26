package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/config"
	"github.com/0xSMW/mail.app-cli/pkg/cache"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
)

// accountsCached returns accounts from the on-disk cache when fresh,
// otherwise from Mail.app, and primes the client either way.
func accountsCached(noCache, forceRefresh bool) ([]mail.Account, error) {
	var c *cache.Cache
	var cacheErr error
	if !noCache {
		c, cacheErr = cache.New()
	}
	if !noCache && !forceRefresh && cacheErr == nil {
		var cached []mail.Account
		if found, err := c.Get("accounts", &cached); err == nil && found {
			mailClient.PrimeAccounts(cached)
			return cached, nil
		}
	}
	accounts, err := mailClient.GetAccountsJSON()
	if err != nil {
		return nil, err
	}
	if !noCache && cacheErr == nil {
		_ = c.Set("accounts", accounts)
	}
	return accounts, nil
}

// requireAccount returns the account in scope. With nothing configured it
// picks the only account Mail.app has; with several it asks the user to choose.
func requireAccount() (string, error) {
	if name := strings.TrimSpace(resolved.Account.Value); name != "" {
		return name, nil
	}
	accounts, err := accountsCached(false, false)
	if err != nil {
		return "", err
	}
	enabled := make([]string, 0, len(accounts))
	for _, account := range accounts {
		if account.Enabled {
			enabled = append(enabled, account.Name)
		}
	}
	if len(enabled) == 0 {
		for _, account := range accounts {
			enabled = append(enabled, account.Name)
		}
	}
	switch len(enabled) {
	case 0:
		return "", clierr.New(clierr.CodeUnavailable, "Mail.app has no accounts").WithHint("add an account in Mail.app, then rerun")
	case 1:
		resolved.Account = config.Value{Value: enabled[0], Source: "auto"}
		return enabled[0], nil
	}
	sort.Strings(enabled)
	return "", clierr.Usagef("choose an account with --account; Mail.app has %s", strings.Join(quoteAll(enabled), ", ")).
		WithHint("set a default with 'mail-app-cli config set account <name>'")
}

// mailboxInScope is the mailbox to read or act on when none is named.
func mailboxInScope() string {
	if name := strings.TrimSpace(resolved.Mailbox.Value); name != "" {
		return name
	}
	return "INBOX"
}

// mailboxExplicit reports whether the user named the mailbox on the command
// line, which is the only case a message ID is trusted to live there.
func mailboxExplicit() bool {
	return resolved.Mailbox.Source == config.SourceFlag
}

// accountExplicit reports whether the user named the account on the command line.
func accountExplicit() bool {
	return resolved.Account.Source == config.SourceFlag
}

// messageRef is a message ID with the account and mailbox it can be reached in.
type messageRef struct {
	ID       string
	Account  string
	Mailbox  string
	Envelope *mail.Message
}

// locateMessages resolves IDs to accounts and mailboxes. An explicit
// --mailbox is trusted; otherwise the Envelope Index says where each message
// lives, and the configured scope is the fallback when the index cannot.
func locateMessages(ids []string) ([]messageRef, []string, error) {
	ids = uniqueStrings(trimAll(ids))
	if len(ids) == 0 {
		return nil, nil, clierr.Usage("provide at least one message ID")
	}
	for _, id := range ids {
		if !isNumericID(id) {
			return nil, nil, clierr.Usagef("message ID %q is not numeric; IDs come from list, search, and show output", id)
		}
	}

	if mailboxExplicit() {
		account, err := requireAccount()
		if err != nil {
			return nil, nil, err
		}
		refs := make([]messageRef, 0, len(ids))
		for _, id := range ids {
			refs = append(refs, messageRef{ID: id, Account: account, Mailbox: mailboxInScope()})
		}
		return refs, nil, nil
	}

	var notices []string
	located, err := mailClient.LocateMessages(ids)
	if err != nil {
		notices = append(notices, fmt.Sprintf("Envelope Index unavailable (%v); assuming %s", err, describeScope()))
		located = map[string]mail.MessageLocation{}
	}

	refs := make([]messageRef, 0, len(ids))
	var missing []string
	for _, id := range ids {
		if location, ok := located[id]; ok {
			if accountExplicit() && !strings.EqualFold(location.Account, resolved.Account.Value) {
				return nil, nil, clierr.New(clierr.CodeNotFound, fmt.Sprintf("message %s is in account %q, not %q", id, location.Account, resolved.Account.Value))
			}
			envelope := location.Envelope
			refs = append(refs, messageRef{ID: id, Account: location.Account, Mailbox: location.Mailbox, Envelope: &envelope})
			continue
		}
		missing = append(missing, id)
	}
	if len(missing) > 0 {
		account, accountErr := requireAccount()
		if accountErr != nil {
			if err == nil {
				return nil, nil, clierr.New(clierr.CodeNotFound, fmt.Sprintf("message not found in the Envelope Index: %s", strings.Join(missing, ", "))).
					WithHint("pass --account and --mailbox to address a message the index has not seen yet")
			}
			return nil, nil, accountErr
		}
		if err == nil {
			notices = append(notices, fmt.Sprintf("not in the Envelope Index, assuming %s/%s: %s", account, mailboxInScope(), strings.Join(missing, ", ")))
		}
		for _, id := range missing {
			refs = append(refs, messageRef{ID: id, Account: account, Mailbox: mailboxInScope()})
		}
	}
	return refs, notices, nil
}

func describeScope() string {
	account := strings.TrimSpace(resolved.Account.Value)
	if account == "" {
		account = "<account>"
	}
	return account + "/" + mailboxInScope()
}

func isNumericID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func trimAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func quoteAll(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, fmt.Sprintf("%q", v))
	}
	return out
}
