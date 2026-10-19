package mail

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// MessageLocation is where a message can be reached through Mail.app's
// scripting bridge. Mailbox is the mailbox a mutation should target: for a
// Gmail account that is the INBOX label when the message carries it, because
// archiving means moving out of INBOX. BackingMailbox is the row the Envelope
// Index stores the message under (All Mail for labelled Gmail messages).
type MessageLocation struct {
	ID             string `json:"id"`
	Account        string `json:"account"`
	Mailbox        string `json:"mailbox"`
	BackingMailbox string `json:"backingMailbox"`
	// ArchiveMailbox is where an archive should act from: INBOX when the
	// message carries that label, otherwise the backing mailbox. Archiving
	// from a user label would strip the label instead of leaving INBOX.
	ArchiveMailbox string   `json:"archiveMailbox"`
	Labels         []string `json:"labels"`
	IsGmail        bool     `json:"-"`
	Envelope       Message  `json:"-"`
}

// LocateMessages resolves message IDs to accounts and mailboxes using the
// Envelope Index. IDs that are not in the index are absent from the result.
// The error is non-nil only when the index itself could not be queried.
func (c *Client) LocateMessages(ids []string) (map[string]MessageLocation, error) {
	numeric := make([]string, 0, len(ids))
	for _, id := range ids {
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil {
			numeric = append(numeric, strconv.FormatInt(n, 10))
		}
	}
	if len(numeric) == 0 {
		return map[string]MessageLocation{}, nil
	}
	idList := strings.Join(numeric, ", ")

	var rows []struct {
		indexMessage
		URL string `json:"URL"`
	}
	query := fmt.Sprintf(`
select
	m.ROWID as ID,
	coalesce(s.subject, '') as Subject,
	case
		when coalesce(a.comment, '') = '' then coalesce(a.address, '')
		else a.comment || ' <' || a.address || '>'
	end as Sender,
	coalesce(strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', m.date_sent, 'unixepoch'), '') as DateSent,
	coalesce(strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', m.date_received, 'unixepoch'), '') as DateReceived,
	m.read as Read,
	m.flagged as Flagged,
	m.size as MessageSize,
	mb.url as URL
from messages m
join mailboxes mb on mb.ROWID = m.mailbox
left join subjects s on s.ROWID = m.subject
left join addresses a on a.ROWID = m.sender
where m.ROWID in (%s) and m.deleted = 0;
`, idList)
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return map[string]MessageLocation{}, nil
	}

	var labelRows []struct {
		MessageID int64  `json:"MessageID"`
		URL       string `json:"URL"`
	}
	labelQuery := fmt.Sprintf(`
select l.message_id as MessageID, mb.url as URL
from labels l
join mailboxes mb on mb.ROWID = l.mailbox_id
where l.message_id in (%s);
`, idList)
	if err := c.runEnvelopeIndexQuery(labelQuery, &labelRows); err != nil {
		return nil, err
	}
	labelsByID := make(map[int64][]string)
	userLabelsByID := make(map[int64][]string)
	for _, row := range labelRows {
		name := indexMailboxDisplayName(row.URL)
		labelsByID[row.MessageID] = append(labelsByID[row.MessageID], name)
		if !isGmailSystemLabelURL(row.URL) {
			userLabelsByID[row.MessageID] = append(userLabelsByID[row.MessageID], name)
		}
	}

	accounts, err := c.GetAccountsJSON()
	if err != nil {
		return nil, err
	}
	accountNames := make(map[string]string, len(accounts))
	for _, account := range accounts {
		accountNames[account.ID] = account.Name
	}

	located := make(map[string]MessageLocation, len(rows))
	for _, row := range rows {
		accountID := indexMailboxAccountID(row.URL)
		accountName, ok := accountNames[accountID]
		if !ok {
			continue
		}
		envelope := row.indexMessage.message()
		backing := indexMailboxDisplayName(row.URL)
		labels := labelsByID[row.ID]
		if labels == nil {
			labels = []string{}
		}
		location := MessageLocation{
			ID:             envelope.ID,
			Account:        accountName,
			Mailbox:        preferredMessageMailbox(backing, userLabelsByID[row.ID]),
			BackingMailbox: backing,
			ArchiveMailbox: archiveSourceMailbox(backing, labels),
			Labels:         labels,
			IsGmail:        strings.Contains(row.URL, "/%5BGmail%5D/"),
		}
		envelope.Mailbox, envelope.Account = location.Mailbox, accountName
		location.Envelope = envelope
		located[envelope.ID] = location
	}
	return located, nil
}

// LocateMessage resolves one ID. It returns a NotFoundError when the index
// has no live row for it.
func (c *Client) LocateMessage(id string) (*MessageLocation, error) {
	located, err := c.LocateMessages([]string{id})
	if err != nil {
		return nil, err
	}
	location, ok := located[strings.TrimSpace(id)]
	if !ok {
		return nil, notFound("message", id)
	}
	return &location, nil
}

// HasMessageIdentityInMailbox finds a logical message after a move may have
// replaced its local Mail.app ID. The Envelope Index has no RFC header column,
// so a header-only identity is resolved through Mail.app by the caller; the
// index path intentionally requires the complete guarded fallback.
func (c *Client) HasMessageIdentityInMailbox(accountName, mailboxName string, identity StableIdentity) (bool, error) {
	matches, err := c.countMessageIdentityInMailbox(accountName, mailboxName, identity)
	return matches > 0, err
}

func (c *Client) countMessageIdentityInMailbox(accountName, mailboxName string, identity StableIdentity) (int, error) {
	if !identity.hasFallback() {
		return 0, fmt.Errorf("Envelope Index cannot look up an RFC Message-ID directly")
	}
	if validRFCMessageID(identity.RFCMessageID) != "" {
		// The index has no RFC header column. Never accept its envelope tuple
		// alone when the caller supplied a stronger identity.
		if c.nativeEnabled() {
			return c.nativeIdentityCount(accountName, mailboxName, identity)
		}
		present, err := c.hasMessageIdentityWithWhoseJXA(accountName, mailboxName, identity)
		if present {
			return 1, err
		}
		return 0, err
	}

	mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName)
	if err != nil {
		return 0, err
	}
	if !ok {
		if c.nativeEnabled() {
			return c.nativeIdentityCount(accountName, mailboxName, identity)
		}
		return 0, fmt.Errorf("mailbox not found: %s", mailboxName)
	}
	query := buildIndexMessageSelect(accountName, mbox.Name) + fmt.Sprintf(`
where %s
	and m.deleted = 0
	and coalesce(s.subject, '') = %s
	and (case when coalesce(a.comment, '') = '' then coalesce(a.address, '') else a.comment || ' <' || a.address || '>' end) = %s
	and coalesce(strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', m.date_sent, 'unixepoch'), '') = %s
	and m.size = %d
limit 2;
`, indexMailboxMembershipCondition(mbox), sqlQuote(identity.Subject), sqlQuote(identity.Sender), sqlQuote(identity.DateSent), identity.MessageSize)
	var rows []indexMessage
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		if c.nativeEnabled() && isEnvelopeIndexUnavailable(err) {
			c.warnEnvelopeIndexFallback(err)
			return c.nativeIdentityCount(accountName, mailboxName, identity)
		}
		return 0, err
	}
	return len(rows), nil
}

func (c *Client) hasMessageIdentityWithWhoseJXA(accountName, mailboxName string, identity StableIdentity) (bool, error) {
	if !identity.hasFallback() {
		return false, fmt.Errorf("stable identity lacks complete metadata; refusing mailbox-wide RFC lookup in %s", mailboxName)
	}
	identity.RFCMessageID = validRFCMessageID(identity.RFCMessageID)
	if identity.RFCMessageID != "" && stableSenderAddress(identity.Sender) == "" {
		return false, fmt.Errorf("RFC stable identity lacks a valid sender address")
	}

	if c.nativeEnabled() {
		matches, err := c.nativeIdentityCount(accountName, mailboxName, identity)
		if err != nil {
			return false, err
		}
		if matches > 1 {
			return false, fmt.Errorf("stable identity is ambiguous in %s", mailboxName)
		}
		return matches == 1, nil
	}
	identityJSON, err := json.Marshal(struct {
		StableIdentity
		SenderAddress string `json:"senderAddress"`
	}{identity, stableSenderAddress(identity.Sender)})
	if err != nil {
		return false, err
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
%s
%s
const wanted = %s;
const acc = mail.accounts.byName('%s');
acc.name();
const mbox = %s;
if (mbox === null) throw new Error('mailbox not found: ' + requestedMailbox);
mbox.name();
const criteria = {subject: {_equals: wanted.subject}, dateSent: {_equals: new Date(wanted.dateSent)}, deletedStatus: false};
if (!wanted.rfcMessageId) criteria.messageSize = wanted.messageSize;
const candidates = mbox.messages.whose(criteria)();
if (candidates.length > 32) throw new Error('stable identity has too many metadata candidates');
const wantedSent = Date.parse(wanted.dateSent);
let matches = 0;
for (let i = 0; i < candidates.length && matches < 2; i++) {
	const msg = candidates[i];
    const sender = (msg.sender() || '').trim();
    if (wanted.rfcMessageId) {
        let address = sender;
        if (sender.endsWith('>') && sender.lastIndexOf('<') >= 0) address = sender.slice(sender.lastIndexOf('<') + 1, -1);
        const canonicalAddress = rfcMessageId({messageId: () => address});
        if (!canonicalAddress || canonicalAddress.slice(1, -1).toLowerCase() !== wanted.senderAddress) continue;
    } else if (sender !== wanted.sender) continue;
	const actualSent = (msg.dateSent() || new Date(0)).getTime();
	if (!Number.isFinite(wantedSent) || actualSent !== wantedSent) continue;
    if (!wanted.rfcMessageId && msg.messageSize() !== wanted.messageSize) continue;
    if (wanted.rfcMessageId) {
        const actualRFC = rfcMessageId(msg);
        if (!actualRFC) throw new Error('RFC identity candidate lacks a valid live header');
        if (actualRFC !== wanted.rfcMessageId) continue;
    }
	matches++;
}
JSON.stringify(matches);
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), jxaRFCMessageIDHelper(), string(identityJSON), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName))
	output, err := c.runJXA(script)
	if err != nil {
		return false, err
	}
	var matches int
	if err := json.Unmarshal([]byte(output), &matches); err != nil {
		return false, fmt.Errorf("parse bounded identity lookup: %w", err)
	}
	if matches > 1 {
		return false, fmt.Errorf("stable identity is ambiguous in %s", mailboxName)
	}
	return matches == 1, nil
}

// isGmailAccount performs the narrow live check needed when the Envelope
// Index is unavailable. A recursively visible All Mail mailbox is treated as
// Gmail-like and therefore fail-closed: only the index can prove whether a
// selected Important message also has INBOX.
func (c *Client) isGmailAccount(accountName string) (bool, error) {
	if c.nativeEnabled() {
		return c.nativeGmailCapability(accountName)
	}
	script := fmt.Sprintf(`
const mail = Application('Mail');
const acc = mail.accounts.byName('%s');
acc.name();
function hasAllMail(boxes) {
	for (let i = 0; i < boxes.length; i++) {
		const box = boxes[i];
		if (String(box.name()).toLowerCase() === 'all mail') return true;
		try { if (hasAllMail(box.mailboxes())) return true; } catch (e) {}
	}
	return false;
}
JSON.stringify(hasAllMail(acc.mailboxes()));
`, escapeJSString(accountName))
	output, err := c.runJXA(script)
	if err != nil {
		return false, err
	}
	var gmail bool
	if err := json.Unmarshal([]byte(output), &gmail); err != nil {
		return false, fmt.Errorf("parse Gmail mailbox capability: %w", err)
	}
	return gmail, nil
}

// PrimeAccounts seeds the in-process account cache so callers holding a
// fresh on-disk copy can avoid a Mail.app round trip.
func (c *Client) PrimeAccounts(accounts []Account) {
	c.shared.accountsMu.Lock()
	defer c.shared.accountsMu.Unlock()
	if c.shared.accountsLoaded || len(accounts) == 0 {
		return
	}
	c.shared.accounts = cloneAccounts(accounts)
	c.shared.accountsLoaded = true
}

func indexMailboxAccountID(rawURL string) string {
	rest := rawURL
	if idx := strings.Index(rest, "://"); idx >= 0 {
		rest = rest[idx+3:]
	}
	if idx := strings.Index(rest, "/"); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

func indexMailboxDisplayName(rawURL string) string {
	if strings.HasSuffix(rawURL, "/%5BGmail%5D/All%20Mail") {
		return "All Mail"
	}
	return mailboxLeafFromURL(rawURL)
}

// isGmailSystemLabelURL reports whether a label row is one of Gmail's own
// ([Gmail]/Important, Starred, categories). Those describe a message; they
// are not places it can be moved out of.
func isGmailSystemLabelURL(rawURL string) bool {
	return strings.Contains(rawURL, "/%5BGmail%5D/")
}

// archiveSourceMailbox is INBOX when the message is labelled with it and the
// backing mailbox otherwise, so archive only ever removes INBOX.
func archiveSourceMailbox(backing string, labels []string) string {
	for _, label := range labels {
		if strings.EqualFold(label, "INBOX") {
			return label
		}
	}
	return backing
}

// preferredMessageMailbox picks the mailbox a mutation should address. INBOX
// wins when present so archive semantics hold for Gmail; any other user
// label beats the backing All Mail row; otherwise the backing mailbox is used.
func preferredMessageMailbox(backing string, labels []string) string {
	for _, label := range labels {
		if strings.EqualFold(label, "INBOX") {
			return label
		}
	}
	if isArchiveAlias(backing) {
		for _, label := range labels {
			if !isArchiveAlias(label) && label != "" {
				return label
			}
		}
	}
	return backing
}
