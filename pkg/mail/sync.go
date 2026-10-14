package mail

import "fmt"

// SyncAccount asks Mail to synchronize the named IMAP account (including
// iCloud), then checks the named account for new mail regardless of its type.
// Mail returns after accepting the requests; this does not prove that the
// server has finished synchronizing.
func (c *Client) SyncAccount(accountName string) error {
	script := fmt.Sprintf(`
	tell application "Mail"
		set targetAccount to missing value
		repeat with acc in accounts
			if name of acc is "%s" then
				set targetAccount to contents of acc
				exit repeat
			end if
		end repeat
		if targetAccount is missing value then
			error "Account not found: %s"
		end if
		%s
	end tell
`, escapeAppleScriptString(accountName), escapeAppleScriptString(accountName), syncAccountAction("targetAccount"))

	_, err := c.runAppleScript(script)
	return err
}

// SyncAllAccounts triggers IMAP synchronization and checks for new mail for
// each enabled receiving account. A successful return means the requests were
// accepted.
func (c *Client) SyncAllAccounts() error {
	script := fmt.Sprintf(`
	tell application "Mail"
		repeat with acc in accounts
			set targetAccount to contents of acc
			if enabled of targetAccount then
				%s
			end if
		end repeat
	end tell
`, syncAccountAction("targetAccount"))
	_, err := c.runAppleScript(script)
	return err
}

// Mail's iCloud class inherits from imap account, but class equality does not
// include subclasses. Explicitly include it to use Mail's synchronization API.
func syncAccountAction(account string) string {
	return fmt.Sprintf(`if class of %s is imap account or class of %s is iCloud account then
			synchronize with %s
		end if
		check for new mail for %s`, account, account, account, account)
}
