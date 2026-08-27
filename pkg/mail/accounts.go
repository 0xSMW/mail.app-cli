package mail

import (
	"encoding/json"
	"fmt"
)

func (c *Client) accountByName(accountName string) (*Account, error) {
	accounts, err := c.GetAccountsJSON()
	if err != nil {
		return nil, err
	}
	for _, account := range accounts {
		if account.Name == accountName {
			return &account, nil
		}
	}
	return nil, notFound("account", accountName)
}

func (c *Client) GetAccounts() ([]Account, error) {
	script := `
	tell application "Mail"
		set accountList to {}
		repeat with acc in accounts
			set accountInfo to {id:id of acc, name:name of acc, emailAddress:(try
				(email addresses of acc)
			on error
				""
			end try), accountType:(try
				(delivery account of acc) as string
			on error
				"unknown"
			end try), userName:user name of acc, enabled:enabled of acc}
			set end of accountList to accountInfo
		end repeat
		return accountList
	end tell
`
	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	// Parse AppleScript list output
	accounts, err := c.parseAccounts(output)
	return accounts, err
}

// ResetAccountCache forgets the in-process account list so the next read
// asks Mail.app again; an explicit refresh uses it.
func (c *Client) ResetAccountCache() {
	c.shared.accountsMu.Lock()
	defer c.shared.accountsMu.Unlock()
	c.shared.accounts = nil
	c.shared.accountsLoaded = false
}

// cloneAccounts copies the slice and each account's address list, so a
// caller editing a result cannot reach into the shared cache.
func cloneAccounts(accounts []Account) []Account {
	out := make([]Account, len(accounts))
	for i, account := range accounts {
		out[i] = account
		if account.EmailAddresses != nil {
			out[i].EmailAddresses = append([]string(nil), account.EmailAddresses...)
		}
	}
	return out
}

func (c *Client) GetAccountsJSON() ([]Account, error) {
	c.shared.accountsMu.Lock()
	if c.shared.accountsLoaded {
		accounts := cloneAccounts(c.shared.accounts)
		c.shared.accountsMu.Unlock()
		return accounts, nil
	}
	c.shared.accountsMu.Unlock()

	script := `
const mail = Application('Mail');
const accounts = mail.accounts();
const result = [];

for (let i = 0; i < accounts.length; i++) {
	const acc = accounts[i];
	const emailAddresses = acc.emailAddresses();
	result.push({
		id: acc.id(),
		name: acc.name(),
		emailAddress: emailAddresses.length > 0 ? emailAddresses[0] : '',
		emailAddresses,
		userName: acc.userName(),
		enabled: acc.enabled()
	});
}

JSON.stringify(result);
`
	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var accounts []Account
	if err := json.Unmarshal([]byte(output), &accounts); err != nil {
		return nil, fmt.Errorf("failed to parse accounts JSON: %w", err)
	}

	c.shared.accountsMu.Lock()
	c.shared.accounts = cloneAccounts(accounts)
	c.shared.accountsLoaded = true
	c.shared.accountsMu.Unlock()

	return cloneAccounts(accounts), nil
}

func (c *Client) GetUnreadCount() (int, error) {
	script := `
	tell application "Mail"
		set totalUnread to 0
		repeat with acc in accounts
			repeat with mbox in mailboxes of acc
				set totalUnread to totalUnread + (unread count of mbox)
			end repeat
		end repeat
		return totalUnread
	end tell
`
	output, err := c.runAppleScript(script)
	if err != nil {
		return 0, err
	}

	var count int
	fmt.Sscanf(output, "%d", &count)
	return count, nil
}

func (c *Client) parseAccounts(_ string) ([]Account, error) {
	// TODO: Implement proper parsing based on AppleScript record format
	return []Account{}, nil
}
