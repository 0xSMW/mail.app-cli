package mail

import (
	"encoding/json"
	"fmt"
)

func (c *Client) GetMailboxes(accountName string) ([]Mailbox, error) {
	script := fmt.Sprintf(`
	tell application "Mail"
		set mailboxList to {}
		try
			set targetAccount to account "%s"
			repeat with mbox in mailboxes of targetAccount
				set mailboxInfo to {name:(name of mbox), unreadCount:(unread count of mbox), totalCount:(count of messages in mbox), account:(name of targetAccount)}
				set end of mailboxList to mailboxInfo
			end repeat
		end try
		return mailboxList
	end tell
`, escapeAppleScriptString(accountName))

	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	mailboxes, err := c.parseMailboxes(output)
	return mailboxes, err
}

func (c *Client) GetAllMailboxes() ([]Mailbox, error) {
	script := `
	tell application "Mail"
		set mailboxList to {}
		repeat with acc in accounts
			repeat with mbox in mailboxes of acc
				set mailboxInfo to {name:(name of mbox), unreadCount:(unread count of mbox), totalCount:(count of messages in mbox), account:(name of acc)}
				set end of mailboxList to mailboxInfo
			end repeat
		end repeat
		return mailboxList
	end tell
`
	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	mailboxes, err := c.parseMailboxes(output)
	return mailboxes, err
}

func (c *Client) GetMailboxesJSON(accountName string) ([]Mailbox, error) {
	if accountName != "" {
		account, err := c.accountByName(accountName)
		if err != nil {
			return nil, err
		}
		if mailboxes, ok, err := c.getMailboxesFromIndex(*account); err != nil {
			return nil, err
		} else if ok {
			return mailboxes, nil
		}

		// If the Envelope Index cannot provide mailbox rows, use a single JXA call.
		mailboxes, err := c.getMailboxesForSingleAccount(accountName)
		if err != nil {
			return nil, err
		}
		return c.enrichArchiveMailboxes(accountName, mailboxes), nil
	}

	accounts, err := c.GetAccountsJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to get accounts: %w", err)
	}

	if len(accounts) == 0 {
		return []Mailbox{}, nil
	}

	// If only one account total, no need for parallelization
	if len(accounts) == 1 {
		if mailboxes, ok, err := c.getMailboxesFromIndex(accounts[0]); err != nil {
			return nil, err
		} else if ok {
			return mailboxes, nil
		}
		mailboxes, err := c.getMailboxesForSingleAccount(accounts[0].Name)
		if err != nil {
			return nil, err
		}
		return c.enrichArchiveMailboxes(accounts[0].Name, mailboxes), nil
	}

	// Use channel to collect results from goroutines
	type result struct {
		mailboxes []Mailbox
		err       error
	}
	results := make(chan result, len(accounts))

	// Launch bounded goroutines for account mailbox retrieval.
	runWithMailCommandLimit(accounts, func(account Account) {
		mailboxes, ok, err := c.getMailboxesFromIndex(account)
		if err != nil {
			results <- result{err: err}
			return
		}
		if !ok {
			mailboxes, err = c.getMailboxesForSingleAccount(account.Name)
			if err == nil {
				mailboxes = c.enrichArchiveMailboxes(account.Name, mailboxes)
			}
		}
		results <- result{mailboxes: mailboxes, err: err}
	})

	// Collect results
	var allMailboxes []Mailbox
	var errors []error
	for i := 0; i < len(accounts); i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
		} else {
			allMailboxes = append(allMailboxes, res.mailboxes...)
		}
	}

	// Return partial results even if some accounts failed
	if len(errors) > 0 && len(allMailboxes) == 0 {
		return nil, fmt.Errorf("failed to get mailboxes from all accounts: %v", errors)
	}

	return allMailboxes, nil
}

func (c *Client) enrichArchiveMailboxes(accountName string, mailboxes []Mailbox) []Mailbox {
	archive, ok, err := c.resolveIndexMailbox(accountName, "All Mail")
	if err != nil || !ok {
		return mailboxes
	}
	found := false
	for i := range mailboxes {
		if isArchiveAlias(mailboxes[i].Name) || mailboxes[i].Name == "All Mail" {
			mailboxes[i].Name = "All Mail"
			mailboxes[i].UnreadCount = archive.UnreadCount
			mailboxes[i].TotalCount = archive.TotalCount
			found = true
		}
	}
	if !found {
		mailboxes = append(mailboxes, Mailbox{
			Name:        "All Mail",
			UnreadCount: archive.UnreadCount,
			TotalCount:  archive.TotalCount,
			Account:     accountName,
		})
	}
	return mailboxes
}

func (c *Client) getMailboxesForSingleAccount(accountName string) ([]Mailbox, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];

try {
	const acc = mail.accounts.byName('%s');
	const accName = acc.name();
	const seen = {};
	function addMailbox(mbox, fallbackName) {
		try {
			const name = fallbackName || mbox.name();
			if (seen[name]) return;
			seen[name] = true;
			let totalCount = 0;
			try { totalCount = mbox.messages.count(); } catch (e) {}
			result.push({
				name: name,
				unreadCount: mbox.unreadCount(),
				totalCount: totalCount,
				account: accName
			});
		} catch (e) {
			// Skip mailboxes that can't be queried at all
		}
	}
	try {
		addMailbox(acc.inbox(), 'INBOX');
	} catch (e) {}
	const mailboxes = acc.mailboxes();
	for (let j = 0; j < mailboxes.length; j++) {
		addMailbox(mailboxes[j], '');
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(accountName))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var mailboxes []Mailbox
	if err := json.Unmarshal([]byte(output), &mailboxes); err != nil {
		return nil, fmt.Errorf("failed to parse mailboxes JSON: %w", err)
	}

	return mailboxes, nil
}

func (c *Client) parseMailboxes(_ string) ([]Mailbox, error) {
	// TODO: Implement proper parsing based on AppleScript record format
	return []Mailbox{}, nil
}
