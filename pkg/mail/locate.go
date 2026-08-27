package mail

import (
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
		ID           int64  `json:"ID"`
		Subject      string `json:"Subject"`
		Sender       string `json:"Sender"`
		DateSent     string `json:"DateSent"`
		DateReceived string `json:"DateReceived"`
		Read         int    `json:"Read"`
		Flagged      int    `json:"Flagged"`
		MessageSize  int    `json:"MessageSize"`
		URL          string `json:"URL"`
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
		id := strconv.FormatInt(row.ID, 10)
		accountID := indexMailboxAccountID(row.URL)
		accountName, ok := accountNames[accountID]
		if !ok {
			continue
		}
		backing := indexMailboxDisplayName(row.URL)
		labels := labelsByID[row.ID]
		if labels == nil {
			labels = []string{}
		}
		location := MessageLocation{
			ID:             id,
			Account:        accountName,
			Mailbox:        preferredMessageMailbox(backing, userLabelsByID[row.ID]),
			BackingMailbox: backing,
			ArchiveMailbox: archiveSourceMailbox(backing, labels),
			Labels:         labels,
		}
		location.Envelope = Message{
			ID:           id,
			Subject:      row.Subject,
			Sender:       row.Sender,
			DateSent:     row.DateSent,
			DateReceived: row.DateReceived,
			Read:         row.Read != 0,
			Flagged:      row.Flagged != 0,
			MessageSize:  row.MessageSize,
			Mailbox:      location.Mailbox,
			Account:      accountName,
		}
		located[id] = location
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

// PrimeAccounts seeds the in-process account cache so callers holding a
// fresh on-disk copy can avoid a Mail.app round trip.
func (c *Client) PrimeAccounts(accounts []Account) {
	c.accountsMu.Lock()
	defer c.accountsMu.Unlock()
	if c.accountsLoaded || len(accounts) == 0 {
		return
	}
	c.accounts = append([]Account(nil), accounts...)
	c.accountsLoaded = true
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
