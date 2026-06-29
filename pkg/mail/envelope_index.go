package mail

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type indexMailbox struct {
	ID          int
	URL         string
	Name        string
	TotalCount  int
	UnreadCount int
}

type indexMessage struct {
	ID            int64  `json:"ID"`
	Subject       string `json:"Subject"`
	Sender        string `json:"Sender"`
	DateSent      string `json:"DateSent"`
	DateReceived  string `json:"DateReceived"`
	Read          int    `json:"Read"`
	Flagged       int    `json:"Flagged"`
	Deleted       int    `json:"Deleted"`
	MessageSize   int    `json:"MessageSize"`
	Content       string `json:"Content"`
	Mailbox       string `json:"Mailbox"`
	Account       string `json:"Account"`
	ToRecipients  string `json:"ToRecipients"`
	CcRecipients  string `json:"CcRecipients"`
	BccRecipients string `json:"BccRecipients"`
}

func mailEnvelopeIndexPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Mail", "V10", "MailData", "Envelope Index"), nil
}

func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func escapeSQLLikePattern(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func mailboxLeafFromURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	leaf := rawURL
	if idx := strings.LastIndex(leaf, "/"); idx >= 0 {
		leaf = leaf[idx+1:]
	}
	leaf = strings.ReplaceAll(leaf, "%20", " ")
	leaf = strings.ReplaceAll(leaf, "%5B", "[")
	leaf = strings.ReplaceAll(leaf, "%5D", "]")
	return leaf
}

func normalizeMailboxAlias(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	name = strings.Trim(name, "/")
	name = strings.ReplaceAll(name, "\\", "/")
	for strings.Contains(name, "//") {
		name = strings.ReplaceAll(name, "//", "/")
	}
	return name
}

func isArchiveAlias(mailboxName string) bool {
	switch normalizeMailboxAlias(mailboxName) {
	case "archive", "all mail", "[gmail]/all mail", "gmail/all mail":
		return true
	default:
		return false
	}
}

func isEnvelopeIndexUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "executable file not found") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "envelope index disabled") ||
		strings.Contains(msg, "unable to open database") ||
		strings.Contains(msg, "authorization denied") ||
		strings.Contains(msg, "operation not permitted")
}

func (c *Client) warnEnvelopeIndexFallback(err error) {
	c.indexFallbackWarningOnce.Do(func() {
		reason := strings.TrimSpace(err.Error())
		if reason == "" {
			reason = "unknown error"
		}
		fmt.Fprintf(os.Stderr, "mail-app-cli: Mail Envelope Index is unavailable (%s). Falling back to Mail.app automation, which may be much slower. For fast local mail queries, grant Full Disk Access to the app launching mail-app-cli, for example Terminal, iTerm, Cursor, VS Code, Codex, or your automation runner, then rerun the command.\n", reason)
	})
}

func (c *Client) runEnvelopeIndexQuery(query string, v any) error {
	if os.Getenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX") != "" {
		return fmt.Errorf("envelope index disabled")
	}

	indexPath, err := mailEnvelopeIndexPath()
	if err != nil {
		return err
	}
	cmd := exec.Command("sqlite3", "-readonly", "-json", indexPath, query)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sqlite3 envelope index query failed: %v - %s", err, strings.TrimSpace(stderr.String()))
	}
	if strings.TrimSpace(out.String()) == "" {
		return json.Unmarshal([]byte("[]"), v)
	}
	if err := json.Unmarshal(out.Bytes(), v); err != nil {
		return fmt.Errorf("failed to parse envelope index JSON: %w", err)
	}
	return nil
}

func indexMailboxURLPattern(accountID, mailboxName string) string {
	escapedName := strings.ReplaceAll(mailboxName, " ", "%20")
	escapedName = strings.ReplaceAll(escapedName, "[", "%5B")
	escapedName = strings.ReplaceAll(escapedName, "]", "%5D")
	if isArchiveAlias(mailboxName) {
		escapedName = "%5BGmail%5D/All%20Mail"
	}
	return fmt.Sprintf("imap://%s/%s", accountID, escapedName)
}

func (c *Client) resolveIndexMailbox(accountName, mailboxName string) (*indexMailbox, bool, error) {
	if accountName == "" || mailboxName == "" {
		return nil, false, nil
	}

	account, err := c.accountByName(accountName)
	if err != nil {
		return nil, false, err
	}

	urlPattern := indexMailboxURLPattern(account.ID, mailboxName)
	query := fmt.Sprintf(`
select
	ROWID as ID,
	url as URL,
	total_count as TotalCount,
	unread_count as UnreadCount
from mailboxes
where url = %s
limit 1;
`, sqlQuote(urlPattern))

	var rows []struct {
		ID          int
		URL         string
		TotalCount  int
		UnreadCount int
	}
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		if isEnvelopeIndexUnavailable(err) {
			c.warnEnvelopeIndexFallback(err)
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(rows) == 0 && isArchiveAlias(mailboxName) {
		query = fmt.Sprintf(`
select
	ROWID as ID,
	url as URL,
	total_count as TotalCount,
	unread_count as UnreadCount
from mailboxes
where url like %s
order by case when url like %s then 0 else 1 end, ROWID
limit 1;
`, sqlQuote("imap://"+account.ID+"/%[Gmail]%/All%Mail"), sqlQuote("%/%5BGmail%5D/All%20Mail"))
		if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
			if isEnvelopeIndexUnavailable(err) {
				c.warnEnvelopeIndexFallback(err)
				return nil, false, nil
			}
			return nil, false, err
		}
	}
	if len(rows) == 0 {
		return nil, false, nil
	}

	mbox := &indexMailbox{
		ID:          rows[0].ID,
		URL:         rows[0].URL,
		Name:        mailboxLeafFromURL(rows[0].URL),
		TotalCount:  rows[0].TotalCount,
		UnreadCount: rows[0].UnreadCount,
	}
	if isArchiveAlias(mailboxName) {
		mbox.Name = "All Mail"
	}
	return mbox, true, nil
}

func (c *Client) getMailboxesFromIndex(account Account) ([]Mailbox, bool, error) {
	query := fmt.Sprintf(`
select
	ROWID as ID,
	url as URL,
	total_count as TotalCount,
	unread_count as UnreadCount
from mailboxes
where url like %s
order by url;
`, sqlQuote("imap://"+account.ID+"/%"))

	var rows []indexMailbox
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		if isEnvelopeIndexUnavailable(err) {
			c.warnEnvelopeIndexFallback(err)
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(rows) == 0 {
		return nil, false, nil
	}

	mailboxes := make([]Mailbox, 0, len(rows))
	for _, row := range rows {
		name := mailboxLeafFromURL(row.URL)
		if strings.HasSuffix(row.URL, "/%5BGmail%5D/All%20Mail") {
			name = "All Mail"
		}
		mailboxes = append(mailboxes, Mailbox{
			Name:        name,
			UnreadCount: row.UnreadCount,
			TotalCount:  row.TotalCount,
			Account:     account.Name,
		})
	}
	return mailboxes, true, nil
}

func indexMessagesToMessages(rows []indexMessage) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, Message{
			ID:           strconv.FormatInt(row.ID, 10),
			Subject:      row.Subject,
			Sender:       row.Sender,
			DateSent:     row.DateSent,
			DateReceived: row.DateReceived,
			Read:         row.Read != 0,
			Flagged:      row.Flagged != 0,
			Deleted:      row.Deleted != 0,
			MessageSize:  row.MessageSize,
			Content:      row.Content,
			Mailbox:      row.Mailbox,
			Account:      row.Account,
		})
	}
	return messages
}

func parseSinceUnix(since string) (int64, bool, error) {
	if strings.TrimSpace(since) == "" {
		return 0, false, nil
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, since, time.Local); err == nil {
			return t.Unix(), true, nil
		}
	}
	return 0, false, fmt.Errorf("invalid --since date %q", since)
}

func buildIndexMessageSelect(accountName, mailboxName string) string {
	return fmt.Sprintf(`
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
	m.deleted as Deleted,
	m.size as MessageSize,
	'' as Content,
	%s as Mailbox,
	%s as Account,
	'' as ToRecipients,
	'' as CcRecipients,
	'' as BccRecipients
from messages m
join subjects s on s.ROWID = m.subject
join addresses a on a.ROWID = m.sender
join mailboxes mb on mb.ROWID = m.mailbox
`, sqlQuote(mailboxName), sqlQuote(accountName))
}

func indexMailboxMembershipCondition(mbox *indexMailbox) string {
	if isArchiveAlias(mbox.Name) {
		return "m.mailbox = " + strconv.Itoa(mbox.ID)
	}
	mailboxID := strconv.Itoa(mbox.ID)
	return "(m.mailbox = " + mailboxID + " or exists (select 1 from labels l where l.message_id = m.ROWID and l.mailbox_id = " + mailboxID + "))"
}

func (c *Client) getMessagesFromIndex(accountName string, mbox *indexMailbox, limit, offset int, unreadOnly, flaggedOnly bool, since string) ([]Message, error) {
	sinceUnix, hasSince, err := parseSinceUnix(since)
	if err != nil {
		return nil, err
	}
	var where []string
	where = append(where, "m.deleted = 0", indexMailboxMembershipCondition(mbox))
	if unreadOnly {
		where = append(where, "m.read = 0")
	}
	if flaggedOnly {
		where = append(where, "m.flagged != 0")
	}
	if hasSince {
		where = append(where, "m.date_received >= "+strconv.FormatInt(sinceUnix, 10))
	}
	query := buildIndexMessageSelect(accountName, mbox.Name) + "\nwhere " + strings.Join(where, " and ") + "\norder by m.date_received desc"
	if limit > 0 {
		query += "\nlimit " + strconv.Itoa(limit)
		if offset > 0 {
			query += " offset " + strconv.Itoa(offset)
		}
	} else if offset > 0 {
		query += "\nlimit -1 offset " + strconv.Itoa(offset)
	}
	query += ";"

	var rows []indexMessage
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, err
	}
	return indexMessagesToMessages(rows), nil
}

func (c *Client) searchMessagesFromIndex(queryText, accountName string, mbox *indexMailbox, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	needle := sqlQuote(escapeSQLLikePattern(strings.ToLower(queryText)))
	membership := indexMailboxMembershipCondition(mbox)
	query := buildIndexMessageSelect(accountName, mbox.Name) + fmt.Sprintf(`
where %s
	and m.deleted = 0
	and (
		lower(coalesce(s.subject, '')) like '%%' || %s || '%%' escape '\'
		or lower(coalesce(a.comment, '')) like '%%' || %s || '%%' escape '\'
		or lower(coalesce(a.address, '')) like '%%' || %s || '%%' escape '\'
	)
order by m.date_received desc
limit %d;
`, membership, needle, needle, needle, limit)

	var rows []indexMessage
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, err
	}
	return indexMessagesToMessages(rows), nil
}
