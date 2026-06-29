package mail

import (
	"encoding/json"
	"fmt"
	"strings"
)

func (c *Client) GetMessages(accountName, mailboxName string, limit int) ([]Message, error) {
	return c.GetMessagesJSON(accountName, mailboxName, limit, 0, false, false, false, "")
}

func (c *Client) GetMessagesJSON(accountName, mailboxName string, limit, offset int, unreadOnly, flaggedOnly, withContent bool, since string) ([]Message, error) {
	if withContent {
		messages, err := c.GetMessagesJSON(accountName, mailboxName, limit, offset, unreadOnly, flaggedOnly, false, since)
		if err != nil {
			return nil, err
		}
		return c.enrichMessagesWithContent(accountName, mailboxName, messages), nil
	}

	if !withContent {
		if mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName); err != nil {
			return nil, err
		} else if ok {
			messages, err := c.getMessagesFromIndex(accountName, mbox, limit, offset, unreadOnly, flaggedOnly, since)
			if err != nil {
				if isEnvelopeIndexUnavailable(err) {
					c.warnEnvelopeIndexFallback(err)
					return c.getMessagesJSONFromJXA(accountName, mailboxName, limit, offset, unreadOnly, flaggedOnly, withContent, since)
				}
				return nil, err
			}
			if len(messages) > 0 || mbox.TotalCount == 0 || unreadOnly || flaggedOnly || strings.TrimSpace(since) != "" {
				return messages, nil
			}
		}
	}

	return c.getMessagesJSONFromJXA(accountName, mailboxName, limit, offset, unreadOnly, flaggedOnly, withContent, since)
}

func (c *Client) getMessagesJSONFromJXA(accountName, mailboxName string, limit, offset int, unreadOnly, flaggedOnly, withContent bool, since string) ([]Message, error) {
	// Build filter/offset/limit clauses using index-based approach.
	// Bulk property accessors (mbox.messages.readStatus()) fetch all values in a
	// single IPC call rather than one round-trip per message.
	unreadFilter := ""
	if unreadOnly {
		unreadFilter = "{ const rs = mbox.messages.readStatus(); indices = indices.filter(i => !rs[i]); }"
	}

	flaggedFilter := ""
	if flaggedOnly {
		flaggedFilter = "{ const fs = mbox.messages.flaggedStatus(); indices = indices.filter(i => fs[i]); }"
	}

	sinceFilter := ""
	if since != "" {
		sinceUnix, _, err := parseSinceUnix(since)
		if err != nil {
			return nil, err
		}
		// Bulk-fetch all received dates in one IPC call, then filter by index
		sinceFilter = fmt.Sprintf("{ const sd = new Date(%d); const allDates = mbox.messages.dateReceived(); indices = indices.filter(i => { const d = allDates[i]; return d && d >= sd; }); }", sinceUnix*1000)
	}

	offsetClause := ""
	if offset > 0 {
		offsetClause = fmt.Sprintf("if (indices.length > %d) indices = indices.slice(%d);", offset, offset)
	}

	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("if (indices.length > %d) indices = indices.slice(0, %d);", limit, limit)
	}

	contentFetch := "const content = '';"
	contentField := "content: content,"
	if withContent {
		contentFetch = "let content = ''; try { content = msg.content() || ''; } catch(e) {}"
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
const requestedMailbox = '%s';
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const accName = acc.name();
	const mboxName = mbox.name();
	const messages = mbox.messages();

	// Index array; all filtering operates on indices so property access is bulk/deferred
	let indices = Array.from({length: messages.length}, (_, i) => i);

	// Bulk property filters (1 IPC call each instead of N)
	%s
	%s
	%s
	%s
	%s

	for (let k = 0; k < indices.length; k++) {
		const i = indices[k];
		const msg = messages[i];
		try { if (msg.deletedStatus()) continue; } catch(e) {}
		try {
			%s
			result.push({
				id: String(msg.id()),
				subject: msg.subject() || '',
				sender: msg.sender() || '',
				dateReceived: (msg.dateReceived() || new Date()).toISOString(),
				dateSent: (msg.dateSent() || new Date()).toISOString(),
				read: msg.readStatus(),
				flagged: msg.flaggedStatus(),
				messageSize: 0,
				%s
				mailbox: mboxName,
				account: accName
			});
		} catch (e) {
			// Skip messages that cause errors
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), unreadFilter, flaggedFilter, sinceFilter, offsetClause, limitClause, contentFetch, contentField)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse messages JSON: %w", err)
	}
	if len(messages) == 0 && isArchiveAlias(mailboxName) && !withContent {
		if mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName); err != nil {
			return nil, err
		} else if ok {
			indexMessages, err := c.getMessagesFromIndex(accountName, mbox, limit, offset, unreadOnly, flaggedOnly, since)
			if err != nil {
				if isEnvelopeIndexUnavailable(err) {
					c.warnEnvelopeIndexFallback(err)
					return messages, nil
				}
				return nil, err
			}
			return indexMessages, nil
		}
	}
	if len(messages) == 0 && isArchiveAlias(mailboxName) {
		return c.getArchiveMessagesWithWhoseJXA(accountName, mailboxName, limit, offset, unreadOnly, flaggedOnly, withContent, since)
	}

	return messages, nil
}

func (c *Client) getArchiveMessagesWithWhoseJXA(accountName, mailboxName string, limit, offset int, unreadOnly, flaggedOnly, withContent bool, since string) ([]Message, error) {
	sinceUnix := int64(0)
	if strings.TrimSpace(since) != "" {
		parsedSince, _, err := parseSinceUnix(since)
		if err != nil {
			return nil, err
		}
		sinceUnix = parsedSince
	}

	contentFetch := "const content = '';"
	contentField := "content: content,"
	if withContent {
		contentFetch = "let content = ''; try { content = msg.content() || ''; } catch(e) {}"
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
const requestedMailbox = '%s';
const sinceDate = new Date(%d);
const offset = %d;
const maxResults = %d;
const unreadOnly = %t;
const flaggedOnly = %t;
%s

function includeMessage(msg) {
	try { if (msg.deletedStatus()) return false; } catch(e) {}
	try { if (unreadOnly && msg.readStatus()) return false; } catch(e) {}
	try { if (flaggedOnly && !msg.flaggedStatus()) return false; } catch(e) {}
	return true;
}

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const accName = acc.name();
	const mboxName = mbox.name();
	const matches = mbox.messages.whose({dateReceived: {_greaterThan: sinceDate}})();
	matches.sort((a, b) => {
		const bd = b.dateReceived() || new Date(0);
		const ad = a.dateReceived() || new Date(0);
		return bd - ad;
	});
	let skipped = 0;
	for (let i = 0; i < matches.length && (maxResults <= 0 || result.length < maxResults); i++) {
		const msg = matches[i];
		if (!includeMessage(msg)) continue;
		if (skipped < offset) {
			skipped++;
			continue;
		}
		try {
			%s
			result.push({
				id: String(msg.id()),
				subject: msg.subject() || '',
				sender: msg.sender() || '',
				dateReceived: (msg.dateReceived() || new Date()).toISOString(),
				dateSent: (msg.dateSent() || new Date()).toISOString(),
				read: msg.readStatus(),
				flagged: msg.flaggedStatus(),
				messageSize: msg.messageSize(),
				%s
				mailbox: mboxName,
				account: accName
			});
		} catch(e) {}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(mailboxName), sinceUnix*1000, offset, limit, unreadOnly, flaggedOnly, jxaMailboxLookupHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), contentFetch, contentField)

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse archive messages JSON: %w", err)
	}

	return messages, nil
}

func (c *Client) GetMessageDetailsJSON(accountName, mailboxName, messageID string) (*Message, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
let result = null;
const requestedMailbox = '%s';
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	let msg = null;
	try {
		msg = mbox.messages.byId(Number('%s'));
		msg.id();
	} catch (e) {
		msg = null;
	}
	if (msg === null) {
		const allIds = mbox.messages.id();
		const targetIdx = allIds.findIndex(id => String(id) === '%s');
		if (targetIdx >= 0) {
			msg = mbox.messages.at(targetIdx);
		}
	}
	if (msg !== null) {
		let content = '';
		try { content = msg.content() || ''; } catch(e) {}

		const toRecipients = [];
		const toRecs = msg.toRecipients();
		for (let t = 0; t < toRecs.length; t++) {
			toRecipients.push(toRecs[t].address());
		}

		const ccRecipients = [];
		const ccRecs = msg.ccRecipients();
		for (let c = 0; c < ccRecs.length; c++) {
			ccRecipients.push(ccRecs[c].address());
		}

		const bccRecipients = [];
		const bccRecs = msg.bccRecipients();
		for (let b = 0; b < bccRecs.length; b++) {
			bccRecipients.push(bccRecs[b].address());
		}

		result = {
			id: String(msg.id()),
			subject: msg.subject() || '',
			sender: msg.sender() || '',
			dateReceived: (msg.dateReceived() || new Date()).toISOString(),
			dateSent: (msg.dateSent() || new Date()).toISOString(),
			read: msg.readStatus(),
			flagged: msg.flaggedStatus(),
			messageSize: msg.messageSize(),
			content: content,
			mailbox: mbox.name(),
			account: acc.name(),
			toRecipients: toRecipients,
			ccRecipients: ccRecipients,
			bccRecipients: bccRecipients
		};
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID), escapeJSString(messageID))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var message Message
	if err := json.Unmarshal([]byte(output), &message); err != nil {
		return nil, fmt.Errorf("failed to parse message JSON: %w", err)
	}

	return &message, nil
}

func (c *Client) parseMessages(_ string) ([]Message, error) {
	// TODO: Implement proper parsing based on AppleScript record format
	return []Message{}, nil
}
