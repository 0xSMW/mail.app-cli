package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
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

	if mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName); err != nil {
		return nil, err
	} else if ok {
		messages, err := c.getMessagesFromIndex(accountName, mbox, limit, offset, unreadOnly, flaggedOnly, since)
		if err != nil {
			if isEnvelopeIndexUnavailable(err) {
				c.warnEnvelopeIndexFallback(err)
				return c.getMessagesJSONFromJXA(accountName, mailboxName, limit, offset, unreadOnly, flaggedOnly, since)
			}
			return nil, err
		}
		if len(messages) > 0 || mbox.TotalCount == 0 || unreadOnly || flaggedOnly || strings.TrimSpace(since) != "" {
			return messages, nil
		}
	}

	return c.getMessagesJSONFromJXA(accountName, mailboxName, limit, offset, unreadOnly, flaggedOnly, since)
}

func (c *Client) getMessagesJSONFromJXA(accountName, mailboxName string, limit, offset int, unreadOnly, flaggedOnly bool, since string) ([]Message, error) {
	if c.nativeEnabled() {
		return c.nativeMessages(accountName, mailboxName, limit, offset, unreadOnly, flaggedOnly, false, since)
	}
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
			result.push({
				%s
			});
		} catch (e) {
			// Skip messages that cause errors
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), unreadFilter, flaggedFilter, sinceFilter, offsetClause, limitClause, jxaMessageProjection{ID: "String(msg.id())", Size: "0", Mailbox: "mboxName", Account: "accName", Content: "content: '',"}.fields())

	var messages []Message
	if err := c.runJXAJSON(script, defaultAutomationTimeout, "messages", &messages); err != nil {
		return nil, err
	}
	if len(messages) == 0 && isArchiveAlias(mailboxName) {
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
		return c.getArchiveMessagesWithWhoseJXA(accountName, mailboxName, limit, offset, unreadOnly, flaggedOnly, since)
	}

	return messages, nil
}

func (c *Client) getArchiveMessagesWithWhoseJXA(accountName, mailboxName string, limit, offset int, unreadOnly, flaggedOnly bool, since string) ([]Message, error) {
	if c.nativeEnabled() {
		return c.nativeMessages(accountName, mailboxName, limit, offset, unreadOnly, flaggedOnly, false, since)
	}
	sinceUnix, _, err := parseSinceUnix(since)
	if err != nil {
		return nil, err
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
			result.push({
				%s
			});
		} catch(e) {}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(mailboxName), sinceUnix*1000, offset, limit, unreadOnly, flaggedOnly, jxaMailboxLookupHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), jxaMessageProjection{ID: "String(msg.id())", Size: "msg.messageSize()", Mailbox: "mboxName", Account: "accName", Content: "content: '',"}.fields())

	var messages []Message
	if err := c.runJXAJSON(script, defaultAutomationTimeout, "archive messages", &messages); err != nil {
		return nil, err
	}

	return messages, nil
}

func (c *Client) GetMessageDetailsJSON(accountName, mailboxName, messageID string) (*Message, error) {
	return c.getMessageDetailsWithTimeout(accountName, mailboxName, messageID, defaultAutomationTimeout)
}

func (c *Client) getMessageDetailsWithTimeout(accountName, mailboxName, messageID string, timeout time.Duration) (*Message, error) {
	message, _, err := c.getMessageDetailsTraced(accountName, mailboxName, messageID, timeout, !c.shared.bodyFromMail)
	return message, err
}

// getMessageDetailsFromMail always takes the body from Mail.app. A draft
// rewrite uses it so the body it preserves is the one Mail.app composes with.
func (c *Client) getMessageDetailsFromMail(accountName, mailboxName, messageID string) (*Message, error) {
	message, _, err := c.getMessageDetailsTraced(accountName, mailboxName, messageID, defaultAutomationTimeout, false)
	return message, err
}

// getMessageDetailsTraced reads one message as named phases, so a timeout or
// a Mail.app error names the operation it happened in. A nil message with a
// nil error means the mailbox answered and does not hold the ID; every other
// failure is returned as an error.
//
// With preferDisk the body comes from the message file Mail.app keeps on
// disk, and Mail.app is asked only for metadata. Mail.app renders a body on
// request, and that request can stall it for every later caller.
func (c *Client) getMessageDetailsTraced(accountName, mailboxName, messageID string, timeout time.Duration, preferDisk bool) (*Message, *PhaseTrace, error) {
	if c.nativeEnabled() {
		return c.nativeMessageDetails(accountName, mailboxName, messageID, timeout, preferDisk, false)
	}
	var diskPhase *AutomationPhase
	var bodyDiagnostics *BodyDiagnostics
	diskBody, haveDiskBody := "", false
	if preferDisk {
		started := time.Now()
		body, diagnostics, err := c.readMessageBodyFromDiskWithDiagnostics(messageID)
		bodyDiagnostics = diagnostics
		diskPhase = &AutomationPhase{Name: "disk_body", ElapsedMs: time.Since(started).Milliseconds()}
		if err != nil {
			if ctxErr := c.Done(); ctxErr != nil {
				return nil, nil, ctxErr
			}
			diskPhase.Note = "unavailable: " + err.Error()
		} else {
			diskBody, haveDiskBody = body, true
		}
	}
	withDiskPhase := func(trace *PhaseTrace) *PhaseTrace {
		if diskPhase == nil {
			return trace
		}
		if trace == nil {
			trace = &PhaseTrace{}
		}
		trace.Phases = append([]AutomationPhase{*diskPhase}, trace.Phases...)
		if trace.LastCompleted == "" {
			trace.LastCompleted = diskPhase.Name
		}
		return trace
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
const requestedAccount = '%s';
const requestedId = '%s';
const skipContent = %s;
%s

function recipientAddresses(recipients) {
	const addresses = [];
	for (let i = 0; i < recipients.length; i++) {
		addresses.push(recipients[i].address());
	}
	return addresses;
}

let message = null;
let failure = null;
try {
	phase('bridge_start', () => null);
	const acc = phase('resolve_account', () => {
		const account = mail.accounts.byName(requestedAccount);
		account.name();
		return account;
	});
	const mbox = phase('resolve_mailbox', () => {
		const found = %s;
		if (found === null) throw new Error('mailbox not found: ' + requestedMailbox);
		found.name();
		return found;
	});
	let msg = phase('lookup_by_id', (step) => {
		try {
			const direct = mbox.messages.byId(Number(requestedId));
			direct.id();
			return direct;
		} catch (e) {
			if (isTransportError(e)) throw e;
			step.note = 'miss: ' + describeError(e);
			return null;
		}
	});
	if (msg === null) {
		msg = phase('enumerate_ids', (step) => {
			const allIds = mbox.messages.id();
			step.note = allIds.length + ' ids';
			const targetIdx = allIds.findIndex(id => String(id) === requestedId);
			return targetIdx >= 0 ? mbox.messages.at(targetIdx) : null;
		});
	}
	if (msg !== null) {
		let content = '';
		let contentError = '';
		if (!skipContent) phase('content', (step) => {
			try {
				content = msg.content() || '';
			} catch (e) {
				if (isTransportError(e)) throw e;
				contentError = describeError(e);
				step.note = 'unavailable';
			}
		});
		const recipients = phase('recipients', () => ({
			to: recipientAddresses(msg.toRecipients()),
			cc: recipientAddresses(msg.ccRecipients()),
			bcc: recipientAddresses(msg.bccRecipients())
		}));
		message = phase('metadata', () => ({
			%s,
			toRecipients: recipients.to,
			ccRecipients: recipients.cc,
			bccRecipients: recipients.bcc
		}));
	}
} catch (e) {
	failure = {phase: currentPhase, message: describeError(e), number: e && e.errorNumber !== undefined ? e.errorNumber : 0};
}

JSON.stringify({message: message, failure: failure, phases: phaseLog});
`, escapeJSString(mailboxName), escapeJSString(accountName), escapeJSString(messageID), jxaBool(haveDiskBody), jxaMailboxLookupHelper()+jxaRFCMessageIDHelper()+jxaPhaseHelper(), jxaMailboxLookupExpression(mailboxName), jxaMessageProjection{ID: "String(msg.id())", Size: "msg.messageSize()", Mailbox: "mbox.name()", Account: "acc.name()", RFCMessageID: "rfcMessageId(msg)", Content: "content: content,\ncontentError: contentError,"}.fields())

	output, err := c.runJXAWithTimeout(script, timeout)
	if err != nil {
		var timeoutErr *AutomationTimeoutError
		if errors.As(err, &timeoutErr) {
			return nil, withDiskPhase(timeoutErr.Trace), err
		}
		return nil, withDiskPhase(nil), err
	}

	var envelope struct {
		Message *Message `json:"message"`
		Failure *struct {
			Phase   string `json:"phase"`
			Message string `json:"message"`
			Number  int    `json:"number"`
		} `json:"failure"`
		Phases []AutomationPhase `json:"phases"`
	}
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return nil, nil, fmt.Errorf("failed to parse message JSON: %w", err)
	}
	trace := &PhaseTrace{Phases: envelope.Phases}
	if trace.Phases == nil {
		trace.Phases = []AutomationPhase{}
	}
	for _, step := range trace.Phases {
		if step.Error == "" {
			trace.LastCompleted = step.Name
		}
	}
	trace = withDiskPhase(trace)
	if failure := envelope.Failure; failure != nil {
		// -1728 is Mail.app reporting that the named object does not exist.
		switch {
		case failure.Phase == "resolve_account" && failure.Number == -1728:
			return nil, trace, notFound("account", accountName)
		case failure.Phase == "resolve_mailbox" && strings.HasPrefix(failure.Message, "mailbox not found"):
			return nil, trace, notFound("mailbox", mailboxName)
		}
		return nil, trace, &BridgePhaseError{Engine: "jxa", Phase: failure.Phase, Message: failure.Message, Trace: trace}
	}
	if message := envelope.Message; message != nil {
		if bodyDiagnostics != nil && !c.shared.bodyAlternatives {
			for i := range bodyDiagnostics.Alternatives {
				bodyDiagnostics.Alternatives[i].Text = ""
			}
		}
		message.BodyDiagnostics = bodyDiagnostics
		message.ContentSource = "mail"
		if haveDiskBody {
			message.Content, message.ContentSource = diskBody, "disk"
		}
	}
	return envelope.Message, trace, nil
}

// GetMessageDetailsForVerificationJSON reads a message for a post-mutation
// verification. Unlike GetMessageDetailsJSON, it deliberately lets failures
// resolving the account, mailbox, or message properties reach osascript. A
// nil message is therefore proof that the message is absent, rather than a
// catch-all result for an automation failure.
func (c *Client) GetMessageDetailsForVerificationJSON(accountName, mailboxName, messageID string) (*Message, error) {
	if c.nativeEnabled() {
		message, _, err := c.nativeMessageDetails(accountName, mailboxName, messageID, identityEnrichmentTimeout, false, true)
		return message, err
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
let result = null;
const requestedMailbox = '%s';
%s

const acc = mail.accounts.byName('%s');
// Force resolution so a missing account remains an automation error.
acc.name();
const mbox = %s;
if (mbox === null) {
	throw new Error('mailbox not found: ' + requestedMailbox);
}

// Force resolution so a stale mailbox object remains an automation error.
mbox.name();

let msg = null;

// Verification callers only use this read while the pre-mutation local ID is
// known to exist. Resolve that ID directly so large mailboxes are never
// enumerated merely to capture stable identity or state.
try {
	msg = mbox.messages.byId(Number('%s'));
	msg.id();
} catch (e) {
	throw new Error('message not found by id: %s');
}

if (msg !== null) {
	result = {
		%s
	};
}

JSON.stringify(result);
`, escapeJSString(mailboxName), jxaMailboxLookupHelper()+jxaRFCMessageIDHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID), escapeJSString(messageID), jxaMessageProjection{ID: "String(msg.id())", Size: "msg.messageSize()", Mailbox: "mbox.name()", Account: "acc.name()", RFCMessageID: "rfcMessageId(msg)"}.fields())

	return c.runJXAMessage(script, identityEnrichmentTimeout, "verification message")
}

// GetMessageStateForVerificationJSON performs a bounded ID query through
// Mail.app. It is the verification fallback when the Envelope Index is not
// available; it never enumerates all IDs in the mailbox.
func (c *Client) GetMessageStateForVerificationJSON(accountName, mailboxName, messageID string) (*Message, error) {
	if c.nativeEnabled() {
		message, _, err := c.nativeMessageDetails(accountName, mailboxName, messageID, defaultAutomationTimeout, false, true)
		return message, err
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
%s
const acc = mail.accounts.byName('%s');
acc.name();
const mbox = %s;
if (mbox === null) throw new Error('mailbox not found: ' + requestedMailbox);
mbox.name();
const matches = mbox.messages.whose({id: {_equals: Number('%s')}})();
if (matches.length > 1) throw new Error('message ID is ambiguous: %s');
let result = null;
if (matches.length === 1) {
	const msg = matches[0];
	result = {
		id: String(msg.id()),
		read: msg.readStatus(),
		flagged: msg.flaggedStatus(),
		mailbox: mbox.name(),
		account: acc.name()
	};
}
JSON.stringify(result);
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID), escapeJSString(messageID))
	return c.runJXAMessage(script, defaultAutomationTimeout, "verification state")
}

func (c *Client) parseMessages(_ string) ([]Message, error) {
	// TODO: Implement proper parsing based on AppleScript record format
	return []Message{}, nil
}
