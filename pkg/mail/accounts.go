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
	return nil, fmt.Errorf("account not found: %s", accountName)
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

func (c *Client) GetAccountsJSON() ([]Account, error) {
	c.accountsMu.Lock()
	if c.accountsLoaded {
		accounts := append([]Account(nil), c.accounts...)
		c.accountsMu.Unlock()
		return accounts, nil
	}
	c.accountsMu.Unlock()

	script := `
const mail = Application('Mail');
const accounts = mail.accounts();
const result = [];

for (let i = 0; i < accounts.length; i++) {
	const acc = accounts[i];
	result.push({
		id: acc.id(),
		name: acc.name(),
		emailAddress: acc.emailAddresses().length > 0 ? acc.emailAddresses()[0] : '',
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

	c.accountsMu.Lock()
	c.accounts = append([]Account(nil), accounts...)
	c.accountsLoaded = true
	c.accountsMu.Unlock()

	return append([]Account(nil), accounts...), nil
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
