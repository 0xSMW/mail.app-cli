package mail

import (
	"bytes"
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client provides an interface to interact with Mail.app via AppleScript
type Client struct {
	accountsMu               sync.Mutex
	accounts                 []Account
	accountsLoaded           bool
	indexFallbackWarningOnce sync.Once
	contentWarningOnce       sync.Once
}

// NewClient creates a new Mail.app client
func NewClient() *Client {
	return &Client{}
}

// escapeJSString escapes a string for use in JavaScript single-quoted strings
func escapeJSString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\") // Escape backslashes first
	s = strings.ReplaceAll(s, "'", "\\'")   // Escape single quotes
	s = strings.ReplaceAll(s, "\n", "\\n")  // Escape newlines
	s = strings.ReplaceAll(s, "\r", "\\r")  // Escape carriage returns
	s = strings.ReplaceAll(s, "\t", "\\t")  // Escape tabs
	return s
}

// escapeAppleScriptString escapes a string for use in AppleScript double-quoted strings
func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\") // Escape backslashes first
	s = strings.ReplaceAll(s, "\"", "\\\"") // Escape double quotes
	return s
}

// runAppleScript executes an AppleScript and returns the output
func (c *Client) runAppleScript(script string) (string, error) {
	cmd := exec.Command("osascript", "-e", script)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("applescript error: %v - %s", err, stderr.String())
	}

	return strings.TrimSpace(out.String()), nil
}

// runJXA executes JavaScript for Automation (JXA) and returns the output
func (c *Client) runJXA(script string) (string, error) {
	cmd := exec.Command("osascript", "-l", "JavaScript", "-e", script)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return "", fmt.Errorf("jxa error: %v - %s", err, stderr.String())
	}

	return strings.TrimSpace(out.String()), nil
}

func (c *Client) runJXAWithTimeout(script string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", script)
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("jxa timed out after %s", timeout)
	}
	if err != nil {
		return "", fmt.Errorf("jxa error: %v - %s", err, stderr.String())
	}

	return strings.TrimSpace(out.String()), nil
}

// Account represents a Mail.app account
type Account struct {
	ID           string
	Name         string
	EmailAddress string
	AccountType  string
	UserName     string
	Enabled      bool
}

// Mailbox represents a Mail.app mailbox
type Mailbox struct {
	Name        string
	UnreadCount int
	TotalCount  int
	Account     string
}

// Message represents an email message
type Message struct {
	ID            string
	Subject       string
	Sender        string
	DateSent      string
	DateReceived  string
	Read          bool
	Flagged       bool
	Deleted       bool
	MessageSize   int
	Content       string
	Mailbox       string
	Account       string
	ToRecipients  []string
	CcRecipients  []string
	BccRecipients []string
}

// Attachment represents an email attachment
type Attachment struct {
	Name     string
	FileSize int
	MimeType string
}

const maxConcurrentMailCommands = 4

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

type messageDateMinHeap []Message

func (h messageDateMinHeap) Len() int { return len(h) }

func (h messageDateMinHeap) Less(i, j int) bool {
	return h[i].DateReceived < h[j].DateReceived
}

func (h messageDateMinHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *messageDateMinHeap) Push(x any) {
	*h = append(*h, x.(Message))
}

func (h *messageDateMinHeap) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
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

func (c *Client) warnContentFallback(err error) {
	c.contentWarningOnce.Do(func() {
		reason := strings.TrimSpace(err.Error())
		if reason == "" {
			reason = "unknown error"
		}
		fmt.Fprintf(os.Stderr, "mail-app-cli: message content fetch was limited (%s). Returned message metadata with any content that could be fetched in time. Use a smaller --limit or messages show for full content on specific messages.\n", reason)
	})
}

func mailContentFetchBudget() time.Duration {
	const defaultBudget = 45 * time.Second
	raw := strings.TrimSpace(os.Getenv("MAIL_APP_CLI_CONTENT_TIMEOUT"))
	if raw == "" {
		return defaultBudget
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultBudget
	}
	return time.Duration(seconds) * time.Second
}

func runWithMailCommandLimit[T any](items []T, run func(T)) {
	limit := maxConcurrentMailCommands
	if len(items) < limit {
		limit = len(items)
	}
	if limit <= 0 {
		return
	}

	sem := make(chan struct{}, limit)
	for _, item := range items {
		sem <- struct{}{}
		go func(v T) {
			defer func() { <-sem }()
			run(v)
		}(item)
	}
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

func indexMailboxURLPattern(accountID, mailboxName string) string {
	escapedName := strings.ReplaceAll(mailboxName, " ", "%20")
	escapedName = strings.ReplaceAll(escapedName, "[", "%5B")
	escapedName = strings.ReplaceAll(escapedName, "]", "%5D")
	if isArchiveAlias(mailboxName) {
		escapedName = "%5BGmail%5D/All%20Mail"
	}
	return fmt.Sprintf("imap://%s/%s", accountID, escapedName)
}

func jxaMailboxLookupExpression(mailboxName string) string {
	return jxaMailboxLookupExpressionFor(mailboxName, "requestedMailbox")
}

func jxaMailboxLookupExpressionFor(mailboxName, variableName string) string {
	if isArchiveAlias(mailboxName) {
		return fmt.Sprintf("(findMailboxByNames(acc.mailboxes(), ['All Mail', 'Archive']) || acc.mailboxes.byName(%s))", variableName)
	}
	return fmt.Sprintf("(findMailboxByNames(acc.mailboxes(), [%s]) || acc.mailboxes.byName(%s))", variableName, variableName)
}

func jxaMailboxLookupHelper() string {
	return `
function findMailboxByNames(mailboxes, names) {
	for (let i = 0; i < mailboxes.length; i++) {
		const mailbox = mailboxes[i];
		try {
			if (names.includes(mailbox.name())) {
				return mailbox;
			}
			const child = findMailboxByNames(mailbox.mailboxes(), names);
			if (child !== null) {
				return child;
			}
		} catch (e) {}
	}
	return null;
}
`
}

func jxaMessageByIdHelper() string {
	return `
function messageById(mbox, messageId) {
	let msg = null;
	try {
		msg = mbox.messages.byId(Number(messageId));
		msg.id();
		return msg;
	} catch (e) {
		msg = null;
	}
	try {
		const allIds = mbox.messages.id();
		const targetIdx = allIds.findIndex(id => String(id) === messageId);
		if (targetIdx >= 0) {
			return mbox.messages.at(targetIdx);
		}
	} catch (e) {}
	return null;
}
`
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
	return "exists (select 1 from labels l where l.message_id = m.ROWID and l.mailbox_id = " + strconv.Itoa(mbox.ID) + ")"
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
	needle := sqlQuote(strings.ToLower(queryText))
	membership := indexMailboxMembershipCondition(mbox)
	query := buildIndexMessageSelect(accountName, mbox.Name) + fmt.Sprintf(`
where %s
	and m.deleted = 0
	and (
		lower(coalesce(s.subject, '')) like '%%' || %s || '%%'
		or lower(coalesce(a.comment, '')) like '%%' || %s || '%%'
		or lower(coalesce(a.address, '')) like '%%' || %s || '%%'
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

// GetAccounts retrieves all Mail.app accounts
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

// GetMailboxes retrieves all mailboxes for a specific account
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

// GetAllMailboxes retrieves all mailboxes across all accounts
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

// GetMessages retrieves messages from a mailbox
func (c *Client) GetMessages(accountName, mailboxName string, limit int) ([]Message, error) {
	limitClause := ""
	if limit > 0 {
		limitClause = fmt.Sprintf("if msgCount > %d then set msgCount to %d", limit, limit)
	}

	script := fmt.Sprintf(`
	tell application "Mail"
		set messageList to {}
		try
			set targetAccount to account "%s"
			set targetMailbox to mailbox "%s" of targetAccount
			set msgCount to count of messages in targetMailbox
			%s

			repeat with i from 1 to msgCount
				set msg to message i of targetMailbox
				set msgInfo to {subject:(subject of msg), sender:(sender of msg), dateSent:(date sent of msg as string), dateReceived:(date received of msg as string), isRead:(read status of msg), isFlagged:(flagged status of msg), messageSize:(message size of msg)}
				set end of messageList to msgInfo
			end repeat
		end try
		return messageList
	end tell
`, escapeAppleScriptString(accountName), escapeAppleScriptString(mailboxName), limitClause)

	output, err := c.runAppleScript(script)
	if err != nil {
		return nil, err
	}

	messages, err := c.parseMessages(output)
	return messages, err
}

// SearchMessages searches for messages matching a query
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

// MarkMessageAsRead marks a message as read
func (c *Client) MarkMessageAsRead(accountName, mailboxName, messageID string, read bool) error {
	return c.runMessageAction(
		accountName,
		mailboxName,
		messageID,
		fmt.Sprintf("msg.readStatus = %s;", jxaBool(read)),
	)
}

func jxaBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func (c *Client) runMessageAction(accountName, mailboxName, messageID, action string) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
%s
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const msg = messageById(mbox, '%s');
	if (msg === null) {
		'Error: Message not found';
	} else {
		%s
		'Success';
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID), action)

	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

// FlagMessage sets or unsets the flagged status of a message
func (c *Client) FlagMessage(accountName, mailboxName, messageID string, flagged bool) error {
	return c.runMessageAction(
		accountName,
		mailboxName,
		messageID,
		fmt.Sprintf("msg.flaggedStatus = %s;", jxaBool(flagged)),
	)
}

// DeleteMessage moves a message to trash
func (c *Client) DeleteMessage(accountName, mailboxName, messageID string) error {
	return c.runMessageAction(accountName, mailboxName, messageID, "msg.delete();")
}

// SendMessage sends a new email message
func (c *Client) SendMessage(accountName, subject, body string, to, cc, bcc, attachments []string) error {
	// Escape all recipients
	var toList, ccList, bccList string
	var escapedTo, escapedCc, escapedBcc []string

	for _, addr := range to {
		escapedTo = append(escapedTo, escapeAppleScriptString(addr))
	}
	toList = strings.Join(escapedTo, `", "`)

	for _, addr := range cc {
		escapedCc = append(escapedCc, escapeAppleScriptString(addr))
	}
	ccList = strings.Join(escapedCc, `", "`)

	for _, addr := range bcc {
		escapedBcc = append(escapedBcc, escapeAppleScriptString(addr))
	}
	bccList = strings.Join(escapedBcc, `", "`)

	// Build attachment code
	var attachCodeBuilder strings.Builder
	for _, attPath := range attachments {
		escapedPath := escapeAppleScriptString(attPath)
		fmt.Fprintf(&attachCodeBuilder, `
			try
				make new attachment with properties {file name:"%s"} at after the last paragraph
			on error
				-- Skip files that can't be attached
			end try
`, escapedPath)
	}
	attachCode := attachCodeBuilder.String()

	// AppleScript block
	script := fmt.Sprintf(`
	tell application "Mail"
		try
			set targetAccount to account "%s"
			set newMessage to make new outgoing message with properties {subject:"%s", content:"%s", visible:false}

			tell newMessage
				set sender to (item 1 of (email addresses of targetAccount as list))

				repeat with addr in {"%s"}
					make new to recipient at end of to recipients with properties {address:addr}
				end repeat

				if "%s" is not "" then
					repeat with addr in {"%s"}
						make new cc recipient at end of cc recipients with properties {address:addr}
					end repeat
				end if

				if "%s" is not "" then
					repeat with addr in {"%s"}
						make new bcc recipient at end of bcc recipients with properties {address:addr}
					end repeat
				end if
%s
			send
			end tell
			return "Success"
		on error errMsg
			return "Error: " & errMsg
		end try
	end tell
`, escapeAppleScriptString(accountName), escapeAppleScriptString(subject), escapeAppleScriptString(body),
		toList,
		ccList, ccList,
		bccList, bccList,
		attachCode)

	_, err := c.runAppleScript(script)
	return err
}

// Helper function to parse accounts from AppleScript output
func (c *Client) parseAccounts(_ string) ([]Account, error) {
	// TODO: Implement proper parsing based on AppleScript record format
	return []Account{}, nil
}

// Helper function to parse mailboxes from AppleScript output
func (c *Client) parseMailboxes(_ string) ([]Mailbox, error) {
	// TODO: Implement proper parsing based on AppleScript record format
	return []Mailbox{}, nil
}

// Helper function to parse messages from AppleScript output
func (c *Client) parseMessages(_ string) ([]Message, error) {
	// TODO: Implement proper parsing based on AppleScript record format
	return []Message{}, nil
}

// GetUnreadCount gets the total unread message count
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

// GetAccountsJSON retrieves accounts as JSON using JXA
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

// SyncAccount forces Mail.app to check for new mail (syncs all accounts)
// Note: Mail.app's AppleScript doesn't support per-account sync, so this syncs all accounts
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

// SyncAllAccounts forces Mail.app to check for new mail across all accounts
func (c *Client) SyncAllAccounts() error {
	script := `tell application "Mail" to check for new mail`
	_, err := c.runAppleScript(script)
	return err
}

// GetMailboxesJSON retrieves mailboxes as JSON using JXA
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

// getMailboxesForSingleAccount retrieves mailboxes for a specific account
func (c *Client) getMailboxesForSingleAccount(accountName string) ([]Mailbox, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];

try {
	const acc = mail.accounts.byName('%s');
	const accName = acc.name();
	const mailboxes = acc.mailboxes();
	for (let j = 0; j < mailboxes.length; j++) {
		const mbox = mailboxes[j];
		try {
			let totalCount = 0;
			try { totalCount = mbox.messages.count(); } catch (e) {}
			result.push({
				name: mbox.name(),
				unreadCount: mbox.unreadCount(),
				totalCount: totalCount,
				account: accName
			});
		} catch (e) {
			// Skip mailboxes that can't be queried at all
		}
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

// GetMessagesJSON retrieves messages from a mailbox using JXA
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

func (c *Client) enrichMessagesWithContent(accountName, mailboxName string, messages []Message) []Message {
	if len(messages) == 0 {
		return messages
	}

	const chunkSize = 10
	deadline := time.Now().Add(mailContentFetchBudget())

	for start := 0; start < len(messages); start += chunkSize {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			c.warnContentFallback(fmt.Errorf("content fetch budget exhausted"))
			return messages
		}
		chunkTimeout := remaining
		if chunkTimeout > 10*time.Second {
			chunkTimeout = 10 * time.Second
		}

		end := start + chunkSize
		if end > len(messages) {
			end = len(messages)
		}

		ids := make([]string, 0, end-start)
		for _, message := range messages[start:end] {
			ids = append(ids, message.ID)
		}
		contentByID, err := c.getMessageContentBatch(accountName, mailboxName, ids, chunkTimeout)
		if err != nil {
			c.warnContentFallback(err)
			return messages
		}
		for i := start; i < end; i++ {
			if content, ok := contentByID[messages[i].ID]; ok {
				messages[i].Content = content
			}
		}
	}

	return messages
}

func (c *Client) getMessageContentBatch(accountName, mailboxName string, ids []string, timeout time.Duration) (map[string]string, error) {
	encodedIDs, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
const messageIds = %s;
const result = {};
%s
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	for (let i = 0; i < messageIds.length; i++) {
		const id = String(messageIds[i]);
		const msg = messageById(mbox, id);
		if (msg === null) {
			result[id] = '';
			continue;
		}
		try {
			result[id] = msg.content() || '';
		} catch(e) {
			result[id] = '';
		}
	}
} catch(e) {}

JSON.stringify(result);
`, escapeJSString(mailboxName), string(encodedIDs), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName))

	output, err := c.runJXAWithTimeout(script, timeout)
	if err != nil {
		return nil, err
	}

	var contentByID map[string]string
	if err := json.Unmarshal([]byte(output), &contentByID); err != nil {
		return nil, fmt.Errorf("failed to parse content JSON: %w", err)
	}
	return contentByID, nil
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

// GetMessageDetailsJSON retrieves full details of a specific message
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
	} catch (e) {}
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

// ArchiveMessage moves a message to the provider's archive mailbox.
// Gmail exposes archived mail as "All Mail", and some Gmail accounts can also
// have a user-created "Archive" label. Prefer "All Mail" when present, then
// fall back to "Archive" for providers that expose a conventional archive
// mailbox. Search recursively because some providers nest special mailboxes.
func (c *Client) ArchiveMessage(accountName, mailboxName, messageID string) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
%s
%s
try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const msg = messageById(mbox, '%s');
	if (msg === null) {
		'Error: Message not found';
	} else {
		function findArchiveCandidates(mailboxes, candidates) {
			for (let j = 0; j < mailboxes.length; j++) {
				const name = mailboxes[j].name();
				if (name === 'All Mail' || name === 'Archive') {
					candidates.push({ name: name, mailbox: mailboxes[j] });
				}
				try {
					const sub = mailboxes[j].mailboxes();
					if (sub.length > 0) {
						findArchiveCandidates(sub, candidates);
					}
				} catch(e) {}
			}
		}

		const archiveCandidates = [];
		findArchiveCandidates(acc.mailboxes(), archiveCandidates);
		let archiveBox = null;
		for (let i = 0; i < archiveCandidates.length; i++) {
			if (archiveCandidates[i].name === 'All Mail') {
				archiveBox = archiveCandidates[i].mailbox;
				break;
			}
		}
		if (!archiveBox) {
			for (let i = 0; i < archiveCandidates.length; i++) {
				if (archiveCandidates[i].name === 'Archive') {
					archiveBox = archiveCandidates[i].mailbox;
					break;
				}
			}
		}
		if (archiveBox) {
			msg.mailbox = archiveBox;
			'Success';
		} else {
			'Error: Archive mailbox not found';
		}
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID))

	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

// MoveMessage moves a message to a different mailbox
func (c *Client) MoveMessage(accountName, sourceMailbox, messageID, targetMailbox string) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
const requestedTargetMailbox = '%s';
%s
%s
try {
	const acc = mail.accounts.byName('%s');
	const sourceMbox = %s;
	const msg = messageById(sourceMbox, '%s');
	if (msg === null) {
		'Error: Message not found';
	} else {
		const destMbox = %s;
		msg.mailbox = destMbox;
		'Success';
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(sourceMailbox), escapeJSString(targetMailbox), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(sourceMailbox), escapeJSString(messageID), jxaMailboxLookupExpressionFor(targetMailbox, "requestedTargetMailbox"))

	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

// GetAttachmentsJSON retrieves attachments from a message
func (c *Client) GetAttachmentsJSON(accountName, mailboxName, messageID string) ([]Attachment, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];

try {
	const acc = mail.accounts.byName('%s');
	const mbox = acc.mailboxes.byName('%s');
	const allIds = mbox.messages.id();
	const targetIdx = allIds.findIndex(id => String(id) === '%s');
	if (targetIdx >= 0) {
		const attachments = mbox.messages.at(targetIdx).mailAttachments();
		for (let a = 0; a < attachments.length; a++) {
			const att = attachments[a];
			let mimeType = 'unknown';
			try {
				mimeType = att.mimeType() || 'unknown';
			} catch (e) {
				// mimeType() sometimes fails in Mail.app
			}
			result.push({
				name: att.name(),
				fileSize: att.fileSize(),
				mimeType: mimeType
			});
		}
	}
} catch (e) {
	// Handle errors gracefully
}

JSON.stringify(result);
`, escapeJSString(accountName), escapeJSString(mailboxName), escapeJSString(messageID))

	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}

	var attachments []Attachment
	if err := json.Unmarshal([]byte(output), &attachments); err != nil {
		return nil, fmt.Errorf("failed to parse attachments JSON: %w", err)
	}

	return attachments, nil
}

// SaveAttachment saves an attachment to disk
func (c *Client) SaveAttachment(accountName, mailboxName, messageID, attachmentName, savePath string) error {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const app = Application.currentApplication();
app.includeStandardAdditions = true;

try {
	const acc = mail.accounts.byName('%s');
	const mbox = acc.mailboxes.byName('%s');
	const allIds = mbox.messages.id();
	const targetIdx = allIds.findIndex(id => String(id) === '%s');
	if (targetIdx < 0) {
		'Error: Message not found';
	} else {
		const attachments = mbox.messages.at(targetIdx).mailAttachments();
		let found = false;
		for (let a = 0; a < attachments.length; a++) {
			if (attachments[a].name() === '%s') {
				const pathObj = Path('%s');
				attachments[a].save({ in: pathObj });
				found = true;
				break;
			}
		}
		if (found) {
			'Success';
		} else {
			'Error: Attachment not found';
		}
	}
} catch (e) {
	'Error: ' + e;
}
`, escapeJSString(accountName), escapeJSString(mailboxName), escapeJSString(messageID), escapeJSString(attachmentName), escapeJSString(savePath))

	output, err := c.runJXA(script)
	if err != nil {
		return err
	}
	if strings.Contains(output, "Error") {
		return fmt.Errorf(output)
	}
	return nil
}

// SearchMessagesJSON searches for messages across mailboxes
// Note: By default only searches INBOX mailboxes for performance reasons
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

	type searchTarget struct {
		AccountName string
		MailboxName string
	}

	var targets []searchTarget
	if accountName != "" {
		targets = append(targets, searchTarget{AccountName: accountName, MailboxName: "All Mail"})
	} else {
		accounts, err := c.GetAccountsJSON()
		if err != nil {
			return nil, fmt.Errorf("failed to get accounts: %w", err)
		}
		for _, account := range accounts {
			if account.Enabled {
				targets = append(targets, searchTarget{AccountName: account.Name, MailboxName: "INBOX"})
			}
		}
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

// searchMessagesInSingleMailbox searches for messages in a specific mailbox
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

// GetMessagesFromMultipleMailboxes loads messages from multiple mailboxes concurrently
func (c *Client) GetMessagesFromMultipleMailboxes(requests []struct {
	AccountName string
	MailboxName string
	Limit       int
	Offset      int
	UnreadOnly  bool
	FlaggedOnly bool
	WithContent bool
	Since       string
}) ([]Message, error) {
	if len(requests) == 0 {
		return []Message{}, nil
	}

	// If only one request, no need for parallelization
	if len(requests) == 1 {
		req := requests[0]
		return c.GetMessagesJSON(req.AccountName, req.MailboxName, req.Limit, req.Offset, req.UnreadOnly, req.FlaggedOnly, req.WithContent, req.Since)
	}

	// Load messages from multiple mailboxes in parallel
	type result struct {
		messages []Message
		err      error
	}
	results := make(chan result, len(requests))

	// Launch bounded goroutines for mailbox retrieval.
	runWithMailCommandLimit(requests, func(r struct {
		AccountName string
		MailboxName string
		Limit       int
		Offset      int
		UnreadOnly  bool
		FlaggedOnly bool
		WithContent bool
		Since       string
	}) {
		messages, err := c.GetMessagesJSON(r.AccountName, r.MailboxName, r.Limit, r.Offset, r.UnreadOnly, r.FlaggedOnly, r.WithContent, r.Since)
		results <- result{messages: messages, err: err}
	})

	// Collect results
	var allMessages []Message
	var errors []error
	for i := 0; i < len(requests); i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
		} else {
			allMessages = append(allMessages, res.messages...)
		}
	}

	// Return partial results even if some mailboxes failed
	if len(errors) > 0 && len(allMessages) == 0 {
		return nil, fmt.Errorf("failed to get messages from all mailboxes: %v", errors)
	}

	return allMessages, nil
}

// GetMultipleMessageDetails loads full details for multiple messages concurrently
func (c *Client) GetMultipleMessageDetails(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
}) ([]*Message, error) {
	if len(requests) == 0 {
		return []*Message{}, nil
	}

	// If only one request, no need for parallelization
	if len(requests) == 1 {
		req := requests[0]
		msg, err := c.GetMessageDetailsJSON(req.AccountName, req.MailboxName, req.MessageID)
		if err != nil {
			return nil, err
		}
		return []*Message{msg}, nil
	}

	// Load message details in parallel
	type result struct {
		message *Message
		err     error
		index   int
	}
	results := make(chan result, len(requests))

	type indexedRequest struct {
		index int
		req   struct {
			AccountName string
			MailboxName string
			MessageID   string
		}
	}
	indexedRequests := make([]indexedRequest, 0, len(requests))
	for i, req := range requests {
		indexedRequests = append(indexedRequests, indexedRequest{index: i, req: req})
	}
	runWithMailCommandLimit(indexedRequests, func(item indexedRequest) {
		message, err := c.GetMessageDetailsJSON(item.req.AccountName, item.req.MailboxName, item.req.MessageID)
		results <- result{message: message, err: err, index: item.index}
	})

	// Collect results in original order
	messages := make([]*Message, len(requests))
	var errors []error
	successCount := 0

	for i := 0; i < len(requests); i++ {
		res := <-results
		if res.err != nil {
			errors = append(errors, res.err)
			messages[res.index] = nil
		} else {
			messages[res.index] = res.message
			successCount++
		}
	}

	// Return error if all requests failed
	if successCount == 0 {
		return nil, fmt.Errorf("failed to get all message details: %v", errors)
	}

	return messages, nil
}

// BulkMarkMessages marks multiple messages as read/unread concurrently
func (c *Client) BulkMarkMessages(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
	Read        bool
}) error {
	return runBulkOperations(requests, "failed to mark some messages", func(req struct {
		AccountName string
		MailboxName string
		MessageID   string
		Read        bool
	}) error {
		return c.MarkMessageAsRead(req.AccountName, req.MailboxName, req.MessageID, req.Read)
	})
}

// BulkFlagMessages flags/unflags multiple messages concurrently
func (c *Client) BulkFlagMessages(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
	Flagged     bool
}) error {
	return runBulkOperations(requests, "failed to flag some messages", func(req struct {
		AccountName string
		MailboxName string
		MessageID   string
		Flagged     bool
	}) error {
		return c.FlagMessage(req.AccountName, req.MailboxName, req.MessageID, req.Flagged)
	})
}

// BulkDeleteMessages deletes multiple messages concurrently
func (c *Client) BulkDeleteMessages(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
}) error {
	return runBulkOperations(requests, "failed to delete some messages", func(req struct {
		AccountName string
		MailboxName string
		MessageID   string
	}) error {
		return c.DeleteMessage(req.AccountName, req.MailboxName, req.MessageID)
	})
}

// BulkArchiveMessages archives multiple messages concurrently
func (c *Client) BulkArchiveMessages(requests []struct {
	AccountName string
	MailboxName string
	MessageID   string
}) error {
	return runBulkOperations(requests, "failed to archive some messages", func(req struct {
		AccountName string
		MailboxName string
		MessageID   string
	}) error {
		return c.ArchiveMessage(req.AccountName, req.MailboxName, req.MessageID)
	})
}

func runBulkOperations[T any](requests []T, failureMessage string, run func(T) error) error {
	if len(requests) == 0 {
		return nil
	}

	if len(requests) == 1 {
		return run(requests[0])
	}

	errors := make(chan error, len(requests))

	runWithMailCommandLimit(requests, func(r T) {
		errors <- run(r)
	})

	var errorList []error
	for i := 0; i < len(requests); i++ {
		if err := <-errors; err != nil {
			errorList = append(errorList, err)
		}
	}

	if len(errorList) > 0 {
		return fmt.Errorf("%s: %v", failureMessage, errorList)
	}

	return nil
}

// GetUnifiedMessagesJSON retrieves messages from unified views across all accounts.
//
// mailboxType must be one of: "inbox", "unread", "flagged", "sent", "drafts",
// "trash", "junk".
//
// inbox/unread/flagged use the accounts-based path (GetMessagesFromMultipleMailboxes
// → GetMessagesJSON per account INBOX) because mailbox objects from
// mail.inboxes() don't support the same bulk property operations as those
// obtained via acc.mailboxes.byName(), causing unreliable filtering.
//
// sent/drafts/trash/junk use Mail.app's JXA special-mailbox accessors
// (mail.sentMailboxes() etc.) which don't require per-message filtering.
func (c *Client) GetUnifiedMessagesJSON(mailboxType string, limit, offset int, withContent bool) ([]Message, error) {
	switch mailboxType {
	case "inbox", "unread", "flagged":
		return c.getInboxBasedUnified(mailboxType, limit, offset, withContent)
	case "sent", "drafts", "trash", "junk":
		return c.getSpecialMailboxUnified(mailboxType, limit, offset, withContent)
	default:
		return nil, fmt.Errorf("unknown unified mailbox type: %s", mailboxType)
	}
}

// getInboxBasedUnified fetches messages from each account's INBOX using the
// proven GetMessagesJSON path, then merges, sorts, and slices globally.
func (c *Client) getInboxBasedUnified(mailboxType string, limit, offset int, withContent bool) ([]Message, error) {
	accounts, err := c.GetAccountsJSON()
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}

	// Over-fetch per account so the global sort+slice is accurate.
	perLimit := limit + offset
	if perLimit < 50 {
		perLimit = 50
	}

	type req = struct {
		AccountName string
		MailboxName string
		Limit       int
		Offset      int
		UnreadOnly  bool
		FlaggedOnly bool
		WithContent bool
		Since       string
	}

	var requests []req
	for _, acc := range accounts {
		if !acc.Enabled {
			continue
		}
		requests = append(requests, req{
			AccountName: acc.Name,
			MailboxName: "INBOX",
			Limit:       perLimit,
			Offset:      0,
			UnreadOnly:  mailboxType == "unread",
			FlaggedOnly: mailboxType == "flagged",
			WithContent: withContent,
		})
	}

	if len(requests) == 0 {
		return []Message{}, nil
	}

	messages, err := c.GetMessagesFromMultipleMailboxes(requests)
	if err != nil {
		return nil, err
	}

	return sortAndSlice(messages, offset, limit), nil
}

// getSpecialMailboxUnified fetches messages from provider-specific mailbox names.
// Mail.app's JXA special-mailbox accessors are not available consistently, so
// this reuses the same account/mailbox path as explicit list commands.
func (c *Client) getSpecialMailboxUnified(mailboxType string, limit, offset int, withContent bool) ([]Message, error) {
	perLimit := limit + offset
	if perLimit < 50 {
		perLimit = 50
	}

	allMailboxes, err := c.GetMailboxesJSON("")
	if err != nil {
		return nil, err
	}

	type req = struct {
		AccountName string
		MailboxName string
		Limit       int
		Offset      int
		UnreadOnly  bool
		FlaggedOnly bool
		WithContent bool
		Since       string
	}

	seen := make(map[string]bool)
	var requests []req
	for _, mailbox := range allMailboxes {
		if mailbox.Account == "" || mailbox.Name == "" || !isSpecialMailboxName(mailboxType, mailbox.Name) {
			continue
		}
		key := mailbox.Account + "\x00" + mailbox.Name
		if seen[key] {
			continue
		}
		seen[key] = true
		requests = append(requests, req{
			AccountName: mailbox.Account,
			MailboxName: mailbox.Name,
			Limit:       perLimit,
			WithContent: withContent,
		})
	}

	if len(requests) == 0 {
		return []Message{}, nil
	}

	messages, err := c.GetMessagesFromMultipleMailboxes(requests)
	if err != nil {
		return nil, err
	}

	return sortAndSlice(messages, offset, limit), nil
}

func isSpecialMailboxName(mailboxType, mailboxName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(mailboxName))
	candidates := map[string][]string{
		"sent":   {"sent", "sent messages", "sent mail"},
		"drafts": {"drafts", "draft"},
		"trash":  {"trash", "deleted messages", "deleted items", "bin"},
		"junk":   {"junk", "spam", "junk e-mail", "junk email", "category_spam"},
	}
	for _, candidate := range candidates[mailboxType] {
		if normalized == candidate {
			return true
		}
	}
	return false
}

// sortAndSlice sorts messages by date descending then applies offset and limit.
func sortAndSlice(messages []Message, offset, limit int) []Message {
	if limit > 0 {
		keep := offset + limit
		if keep > 0 && keep < len(messages) {
			h := make(messageDateMinHeap, 0, keep)
			for _, message := range messages {
				if len(h) < keep {
					heap.Push(&h, message)
					continue
				}
				if message.DateReceived > h[0].DateReceived {
					h[0] = message
					heap.Fix(&h, 0)
				}
			}
			messages = h
		}
	}

	sort.Slice(messages, func(i, j int) bool {
		return messages[i].DateReceived > messages[j].DateReceived
	})
	if offset > 0 {
		if offset >= len(messages) {
			return []Message{}
		}
		messages = messages[offset:]
	}
	if limit > 0 && len(messages) > limit {
		messages = messages[:limit]
	}
	return messages
}

// BulkMoveMessages moves multiple messages concurrently
func (c *Client) BulkMoveMessages(requests []struct {
	AccountName   string
	SourceMailbox string
	MessageID     string
	TargetMailbox string
}) error {
	return runBulkOperations(requests, "failed to move some messages", func(req struct {
		AccountName   string
		SourceMailbox string
		MessageID     string
		TargetMailbox string
	}) error {
		return c.MoveMessage(req.AccountName, req.SourceMailbox, req.MessageID, req.TargetMailbox)
	})
}
