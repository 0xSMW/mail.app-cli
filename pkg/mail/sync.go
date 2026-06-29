package mail

import (
	"fmt"
)

func (c *Client) SyncAccount(accountName string) error {
	// Verify account exists
	script := fmt.Sprintf(`
	tell application "Mail"
		set accountFound to false
		repeat with acc in accounts
			if name of acc is "%s" then
				set accountFound to true
				exit repeat
			end if
		end repeat
		if not accountFound then
			error "Account not found: %s"
		end if
	end tell
`, escapeAppleScriptString(accountName), escapeAppleScriptString(accountName))

	_, err := c.runAppleScript(script)
	if err != nil {
		return err
	}

	// Check for new mail (syncs all accounts)
	return c.SyncAllAccounts()
}

func (c *Client) SyncAllAccounts() error {
	script := `tell application "Mail" to check for new mail`
	_, err := c.runAppleScript(script)
	return err
}
