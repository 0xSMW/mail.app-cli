package mail

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type searchTarget struct {
	AccountName string
	MailboxName string
}

func (c *Client) SearchMessages(query string, limit int) ([]Message, error) {
	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("if msgCount > %d then set msgCount to %d", limit, limit)
	}

	// query is injected into AppleScript double quotes, needs escaping
	script := fmt.Sprintf(`
	tell application "Mail"
		set messageList to {}
		set foundMessages to (every message whose subject contains "%s" or sender contains "%s" or content contains "%s")
		set msgCount to count of foundMessages
		%s

		repeat with i from 1 to msgCount
			set msg to item i of foundMessages
			try
				set msgInfo to {subject:(subject of msg), sender:(sender of msg), dateSent:(date sent of msg as string), dateReceived:(date received of msg as string), isRead:(read status of msg), isFlagged:(flagged status of msg), messageSize:(message size of msg)}
				set end of messageList to msgInfo
			end try
		end repeat
		return messageList
	end tell
`, escapeAppleScriptString(query), escapeAppleScriptString(query), escapeAppleScriptString(query), limitClause)

	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	messages, err := c.parseMessages(output)
	return messages, err
}

func (c *Client) SearchMessagesJSON(query string, accountName string, mailboxName string, limit int) ([]Message, error) {
	// Set a reasonable default limit if none specified
	if limit == 0 {
		limit = 50
	}

	// If specific mailbox requested, use a single mailbox search.
	if mailboxName != "" {
		if mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName); err != nil {
			return nil, err
		} else if ok {
			messages, err := c.searchMessagesFromIndex(query, accountName, mbox, limit)
			if err != nil {
				if isEnvelopeIndexUnavailable(err) {
					c.warnEnvelopeIndexFallback(err)
					return c.searchMessagesInSingleMailboxJXA(query, accountName, mailboxName, limit)
				}
				return nil, err
			}
			return messages, nil
		}
		return c.searchMessagesInSingleMailbox(query, accountName, mailboxName, limit)
	}

	targets, err := c.defaultSearchTargets(accountName)
	if err != nil {
		return nil, err
	}

	if len(targets) == 0 {
		return []Message{}, nil
	}

	// If only one mailbox, no need for parallelization
	if len(targets) == 1 {
		target := targets[0]
		return c.searchMessagesInSingleMailbox(query, target.AccountName, target.MailboxName, limit)
	}

	// Search mailboxes in parallel
	type result struct {
		messages []Message
		err      error
	}
	results := make(chan result, len(targets))

	// Launch goroutine for each mailbox
	runWithMailCommandLimit(targets, func(target searchTarget) {
		messages, err := c.searchMessagesInSingleMailbox(query, target.AccountName, target.MailboxName, limit)
		results <- result{messages: messages, err: err}
	})

	// Collect results
	var allMessages []Message
	var errors []error
	for i := 0; i < len(targets); i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
		} else {
			allMessages = append(allMessages, res.messages...)
		}
	}

	// Return partial results even if some mailboxes failed
	if len(errors) > 0 && len(allMessages) == 0 {
		return nil, fmt.Errorf("failed to search all mailboxes: %v", errors)
	}

	// Sort by date received (newest first) and apply limit
	sort.Slice(allMessages, func(i, j int) bool {
		return allMessages[i].DateReceived > allMessages[j].DateReceived
	})

	if len(allMessages) > limit {
		allMessages = allMessages[:limit]
	}

	return allMessages, nil
}

func (c *Client) searchMessagesInSingleMailbox(query, accountName, mailboxName string, limit int) ([]Message, error) {
	if mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName); err != nil {
		return nil, err
	} else if ok {
		messages, err := c.searchMessagesFromIndex(query, accountName, mbox, limit)
		if err != nil {
			if isEnvelopeIndexUnavailable(err) {
				c.warnEnvelopeIndexFallback(err)
				return c.searchMessagesInSingleMailboxJXA(query, accountName, mailboxName, limit)
			}
			return nil, err
		}
		return messages, nil
	}

	return c.searchMessagesInSingleMailboxJXA(query, accountName, mailboxName, limit)
}

func (c *Client) defaultSearchTargets(accountName string) ([]searchTarget, error) {
	mailboxes, err := c.GetMailboxesJSON(accountName)
	if err != nil {
		return nil, fmt.Errorf("failed to get mailboxes: %w", err)
	}
	enabledAccounts := make(map[string]bool)
	if accountName == "" {
		accounts, err := c.GetAccountsJSON()
		if err != nil {
			return nil, fmt.Errorf("failed to get accounts: %w", err)
		}
		for _, account := range accounts {
			enabledAccounts[account.Name] = account.Enabled
		}
	}

	seen := make(map[string]bool)
	var targets []searchTarget
	for _, mailbox := range mailboxes {
		if mailbox.Account == "" || mailbox.Name == "" || !strings.EqualFold(mailbox.Name, "INBOX") {
			continue
		}
		if accountName == "" && !enabledAccounts[mailbox.Account] {
			continue
		}
		key := mailbox.Account + "\x00" + mailbox.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, searchTarget{AccountName: mailbox.Account, MailboxName: mailbox.Name})
	}

	if len(targets) == 0 && accountName != "" {
		targets = append(targets, searchTarget{AccountName: accountName, MailboxName: "INBOX"})
	}

	return targets, nil
}

func (c *Client) searchMessagesInSingleMailboxJXA(query, accountName, mailboxName string, limit int) ([]Message, error) {
	// Use helper for escaping
	escapedQuery := escapeJSString(query)
	escapedAccount := escapeJSString(accountName)
	escapedMailbox := escapeJSString(mailboxName)
	maxToCheck := 500
	if isArchiveAlias(mailboxName) {
		maxToCheck = 10000
		if limit > 0 && limit*100 > maxToCheck {
			maxToCheck = limit * 100
		}
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
const searchTerm = '%s'.toLowerCase();
const maxResults = %d;
const maxMessagesToCheck = %d;
const requestedMailbox = '%s';
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const accName = acc.name();
	const mboxName = mbox.name();
	const messages = mbox.messages();
	// Limit how many messages to check per mailbox for performance
	// Messages are typically sorted newest first, so this checks recent messages
	const maxToCheck = Math.min(messages.length, maxMessagesToCheck);

	for (let k = 0; k < maxToCheck && result.length < maxResults; k++) {
		const msg = messages[k];
		try {
			const subject = (msg.subject() || '').toLowerCase();
			const sender = (msg.sender() || '').toLowerCase();

			// Only search subject and sender
			if (subject.includes(searchTerm) || sender.includes(searchTerm)) {
				result.push({
					id: String(msg.id()),
					subject: msg.subject() || '',
					sender: msg.sender() || '',
					dateReceived: (msg.dateReceived() || new Date()).toISOString(),
					dateSent: (msg.dateSent() || new Date()).toISOString(),
					read: msg.readStatus(),
					flagged: msg.flaggedStatus(),
					messageSize: msg.messageSize(),
					mailbox: mboxName,
					account: accName
				});
			}
		} catch (e) {
			// Skip messages that cause errors
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapedQuery, limit, maxToCheck, escapedMailbox, jxaMailboxLookupHelper(), escapedAccount, jxaMailboxLookupExpression(mailboxName))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse search results JSON: %w", err)
	}

	if len(messages) == 0 && isArchiveAlias(mailboxName) {
		return c.searchArchiveMailboxWithWhoseJXA(query, accountName, mailboxName, limit)
	}

	return messages, nil
}

func (c *Client) searchArchiveMailboxWithWhoseJXA(query, accountName, mailboxName string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
const seen = {};
const searchTerm = '%s';
const maxResults = %d;
const requestedMailbox = '%s';
%s

function addMessage(msg, accName, mboxName) {
	if (result.length >= maxResults) return;
	try { if (msg.deletedStatus()) return; } catch(e) {}
	try {
		const id = String(msg.id());
		if (seen[id]) return;
		seen[id] = true;
		result.push({
			id: id,
			subject: msg.subject() || '',
			sender: msg.sender() || '',
			dateReceived: (msg.dateReceived() || new Date()).toISOString(),
			dateSent: (msg.dateSent() || new Date()).toISOString(),
			read: msg.readStatus(),
			flagged: msg.flaggedStatus(),
			messageSize: msg.messageSize(),
			mailbox: mboxName,
			account: accName
		});
	} catch (e) {}
}

function addMatches(matches, accName, mboxName) {
	for (let i = 0; i < matches.length && result.length < maxResults; i++) {
		addMessage(matches[i], accName, mboxName);
	}
}

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const accName = acc.name();
	const mboxName = mbox.name();
	try {
		addMatches(mbox.messages.whose({subject: {_contains: searchTerm}})(), accName, mboxName);
	} catch(e) {}
	if (result.length < maxResults) {
		try {
			addMatches(mbox.messages.whose({sender: {_contains: searchTerm}})(), accName, mboxName);
		} catch(e) {}
	}
	result.sort((a, b) => b.dateReceived.localeCompare(a.dateReceived));
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result.slice(0, maxResults));
`, escapeJSString(query), limit, escapeJSString(mailboxName), jxaMailboxLookupHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse archive search results JSON: %w", err)
	}

	return messages, nil
}
