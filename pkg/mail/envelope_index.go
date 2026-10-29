package mail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/mailindex"
	"golang.org/x/text/unicode/norm"
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
	ReceivedUnix  int64  `json:"ReceivedUnix"`
	Subject       string `json:"Subject"`
	Sender        string `json:"Sender"`
	DateSent      string `json:"DateSent"`
	DateReceived  string `json:"DateReceived"`
	Read          int    `json:"Read"`
	Flagged       int    `json:"Flagged"`
	Deleted       int    `json:"Deleted"`
	MessageSize   int    `json:"MessageSize"`
	Content       string `json:"Content"`
	Snippet       string `json:"Snippet"`
	Mailbox       string `json:"Mailbox"`
	Account       string `json:"Account"`
	ToRecipients  string `json:"ToRecipients"`
	CcRecipients  string `json:"CcRecipients"`
	BccRecipients string `json:"BccRecipients"`
}

func mailEnvelopeIndexPath() (string, error) {
	if override := os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"); override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("MAIL_APP_CLI_ENVELOPE_INDEX_PATH must be absolute")
		}
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return discoverEnvelopeIndexPath(filepath.Join(home, "Library", "Mail"))
}

func discoverEnvelopeIndexPath(root string) (string, error) {
	fallback := filepath.Join(root, "V10", "MailData", "Envelope Index")
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return fallback, nil
		}
		return "", fmt.Errorf("discover Mail index: %w", err)
	}
	type version struct {
		number int
		name   string
	}
	var versions []version
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "V") {
			continue
		}
		digits := strings.TrimPrefix(entry.Name(), "V")
		n, err := strconv.Atoi(digits)
		if err == nil && n > 0 && strconv.Itoa(n) == digits {
			versions = append(versions, version{n, entry.Name()})
		}
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].number > versions[j].number })
	for _, v := range versions {
		path := filepath.Join(root, v.name, "MailData", "Envelope Index")
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("inspect Mail index: %w", err)
		}
		if info.Mode().IsRegular() {
			return path, nil
		}
	}
	return fallback, nil
}

// Native errors must retain their cause without triggering the historical
// automation fallback for permissions or a damaged/unsupported schema.
type nativeIndexError struct{ err error }

func (e *nativeIndexError) Error() string { return e.err.Error() }
func (e *nativeIndexError) Unwrap() error { return e.err }

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
	if decoded, err := url.PathUnescape(leaf); err == nil {
		leaf = decoded
	}
	// The Envelope Index stores IMAP folder names in NFD (decomposed), but
	// Mail.app's live Apple Events "pnam" property returns NFC (precomposed).
	// Normalize here so every caller gets the same form Mail.app itself uses;
	// otherwise a name with any combining diacritic never resolves.
	return norm.NFC.String(leaf)
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

// IsArchiveAlias reports whether a mailbox name refers to Gmail's All Mail
// or a generic Archive mailbox.
func IsArchiveAlias(mailboxName string) bool { return isArchiveAlias(mailboxName) }

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
	var nativeErr *nativeIndexError
	if errors.As(err, &nativeErr) {
		return errors.Is(nativeErr.err, os.ErrNotExist)
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
	c.shared.indexFallbackWarningOnce.Do(func() {
		reason := strings.TrimSpace(err.Error())
		if reason == "" {
			reason = "unknown error"
		}
		c.warn(fmt.Sprintf("Mail Envelope Index is unavailable (%s). Mail.app automation fallback may be much slower when used. For fast local mail queries, grant Full Disk Access to the app launching mail-app-cli, for example Terminal, iTerm, Cursor, VS Code, Codex, or your automation runner, then rerun the command.", reason))
	})
}

// envelopeIndexReadError applies the historical read fallback only to eligible
// failures; native permission and schema errors retain their original cause.
func (c *Client) envelopeIndexReadError(err error) error {
	if isEnvelopeIndexUnavailable(err) {
		c.warnEnvelopeIndexFallback(err)
		return nil
	}
	return err
}

func (c *Client) runEnvelopeIndexQuery(query string, v any) error {
	if os.Getenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX") != "" {
		return fmt.Errorf("envelope index disabled")
	}

	indexPath, err := mailEnvelopeIndexPath()
	if err != nil {
		if c.nativeEnabled() {
			return &nativeIndexError{err}
		}
		return err
	}
	if c.nativeEnabled() {
		out, err := mailindex.Query(c.Context(), indexPath, query)
		if err != nil {
			return &nativeIndexError{err}
		}
		if err := json.Unmarshal(out, v); err != nil {
			return &nativeIndexError{fmt.Errorf("failed to parse envelope index JSON: %w", err)}
		}
		return nil
	}
	cmd := exec.CommandContext(c.Context(), "sqlite3", "-readonly", "-json", indexPath, query)
	// Combined verification scopes can exceed the OS argument-size limit.
	// Stream large statements without splitting their database snapshot.
	if len(query) > 64*1024 {
		cmd = exec.CommandContext(c.Context(), "sqlite3", "-readonly", "-json", indexPath)
		cmd.Stdin = strings.NewReader(query)
	}
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	started := time.Now()
	defer func() { recordLegacySQLiteProcess(time.Since(started)) }()
	if err := cmd.Run(); err != nil {
		if ctxErr := c.Context().Err(); ctxErr != nil {
			// The caller's context ended the query; keep that classifiable.
			return fmt.Errorf("envelope index query: %w", ctxErr)
		}
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

func (c *Client) CheckEnvelopeIndex() error {
	var rows []struct {
		OK int `json:"ok"`
	}
	return c.runEnvelopeIndexQuery("select 1 as ok;", &rows)
}

func indexMailboxURLPattern(accountID, mailboxName string) string {
	escapedName := escapeMailboxURLPath(mailboxName)
	if isArchiveAlias(mailboxName) {
		escapedName = "%5BGmail%5D/All%20Mail"
	}
	return fmt.Sprintf("imap://%s/%s", accountID, escapedName)
}

// Match the complete authority in each indexed URL rather than assuming an
// account's provider scheme. Comparing the authority with = keeps account IDs
// case-sensitive and treats LIKE wildcards in an ID literally.
func indexAccountMailboxCondition(accountID string) string {
	if accountID == "" {
		return "0"
	}
	authority := "substr(url, instr(url, '://') + 3)"
	return fmt.Sprintf("instr(url, '://') > 1 and substr(%s, 1, instr(%s, '/') - 1) = %s collate binary",
		authority, authority, sqlQuote(accountID))
}

// Use the same ranked selection for normal reads and verification snapshots.
// The scheme comes from each row, so mixed schemes and subsequent index updates
// do not require a cached account-to-scheme mapping.
func indexMailboxSelect(accountID, mailbox string, counts bool) string {
	projection := "ROWID as ID, url as URL"
	if counts {
		projection += ", total_count, unread_count"
	}
	return indexMailboxCandidateSelect(accountID, mailbox, projection)
}

func indexMailboxPath(accountID string) string {
	return fmt.Sprintf("substr(url, instr(url, '://') + 3 + length(%s))", sqlQuote(accountID+"/"))
}

func escapeMailboxURLPath(name string) string {
	components := strings.Split(name, "/")
	for i := range components {
		components[i] = url.PathEscape(components[i])
	}
	return strings.Join(components, "/")
}

// escapeMailboxURLPathForms returns the percent-escaped path for name in
// every Unicode normalization form a caller-typed mailbox name needs to be
// checked against. The Envelope Index stores IMAP folder names in whichever
// form the account's IMAP server happened to report: observed as NFD
// (decomposed) for at least one real account, and NFC (precomposed) is also
// a normal stored form (existing fixtures in this package model that case).
// A caller-typed name is usually NFC, so matching only one direction misses
// real mailboxes. Returns a single form when NFC and NFD coincide, which is
// every name without a combining mark (ASCII, by far the common case) and
// keeps the SQL for that case exactly as before this existed.
func escapeMailboxURLPathForms(name string) []string {
	nfc := norm.NFC.String(name)
	nfd := norm.NFD.String(name)
	if nfc == nfd {
		return []string{escapeMailboxURLPath(nfc)}
	}
	return []string{escapeMailboxURLPath(nfc), escapeMailboxURLPath(nfd)}
}

func (c *Client) resolveIndexMailbox(accountName, mailboxName string) (*indexMailbox, bool, error) {
	if accountName == "" || mailboxName == "" {
		return nil, false, nil
	}

	account, err := c.accountByName(accountName)
	if err != nil {
		return nil, false, err
	}

	exact := indexMailboxExactCondition(account.ID, mailboxName)
	var rows []indexMailbox
	if err := c.queryIndexMailboxCandidate(exact, &rows); err != nil {
		return nil, false, c.envelopeIndexReadError(err)
	}
	if len(rows) == 0 && normalizeMailboxAlias(mailboxName) == "archive" {
		literal, nested := indexMailboxArchiveConditions(account.ID)
		predicate := literal + " or " + nested + "\norder by case when " + literal + " then 0 else 1 end, ROWID"
		if err := c.queryIndexMailboxCandidate(predicate, &rows); err != nil {
			return nil, false, c.envelopeIndexReadError(err)
		}
	}
	if len(rows) == 0 {
		predicate := indexMailboxBroadCondition(account.ID, mailboxName) + "\norder by case when " + exact + " then 0 else 1 end, ROWID"
		if err := c.queryIndexMailboxCandidate(predicate, &rows); err != nil {
			return nil, false, c.envelopeIndexReadError(err)
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
	return mbox, true, nil
}

func (c *Client) queryIndexMailboxCandidate(predicate string, rows *[]indexMailbox) error {
	query := fmt.Sprintf(`
select
	ROWID as ID,
	url as URL,
	total_count as TotalCount,
	unread_count as UnreadCount
from mailboxes
where %s
limit 1;
`, predicate)
	return c.runEnvelopeIndexQuery(query, rows)
}

func escapedMailboxLeafForms(mailboxName string) []string {
	if isArchiveAlias(mailboxName) {
		return []string{"All%20Mail"}
	}
	return escapeMailboxURLPathForms(mailboxName)
}

func (c *Client) getMailboxesFromIndex(account Account) ([]Mailbox, bool, error) {
	query := fmt.Sprintf(`
select
	ROWID as ID,
	url as URL,
	total_count as TotalCount,
	unread_count as UnreadCount
from mailboxes
where %s
order by url;
`, indexAccountMailboxCondition(account.ID))

	var rows []indexMailbox
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, false, c.envelopeIndexReadError(err)
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

func (row indexMessage) message() Message {
	return Message{
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
		Snippet:      row.Snippet,
		Mailbox:      row.Mailbox,
		Account:      row.Account,
	}
}

func indexMessagesToMessages(rows []indexMessage) []Message {
	messages := make([]Message, 0, len(rows))
	for _, row := range rows {
		messages = append(messages, row.message())
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

// Mail stores the literal reply/forward prefix separately from the base
// subject. Preserve its exact text everywhere an indexed subject is observed.
const indexSubjectExpression = "(coalesce(m.subject_prefix, '') || coalesce(s.subject, ''))"

const indexMessageFrom = `
from messages m
join subjects s on s.ROWID = m.subject
join addresses a on a.ROWID = m.sender
left join summaries su on su.ROWID = m.summary
join mailboxes mb on mb.ROWID = m.mailbox
`

func buildIndexMessageSelect(accountName, mailboxName string) string {
	return fmt.Sprintf(`
select
	m.ROWID as ID,
	coalesce(m.date_received, 0) as ReceivedUnix,
	`+indexSubjectExpression+` as Subject,
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
	coalesce(su.summary, '') as Snippet,
	%s as Mailbox,
	%s as Account,
	'' as ToRecipients,
	'' as CcRecipients,
	'' as BccRecipients`, sqlQuote(mailboxName), sqlQuote(accountName)) + indexMessageFrom
}

func indexMailboxMembershipCondition(mbox *indexMailbox) string {
	if isArchiveAlias(mbox.Name) {
		return "m.mailbox = " + strconv.Itoa(mbox.ID)
	}
	mailboxID := strconv.Itoa(mbox.ID)
	return "(m.mailbox = " + mailboxID + " or " + indexLabelMembershipCondition(mailboxID) + ")"
}

func indexLabelMembershipCondition(mailboxID string) string {
	return "exists (select 1 from labels l where l.message_id = m.ROWID and l.mailbox_id = " + mailboxID + ")"
}

func indexMessageFilterConditions(unread, flagged bool, since string) ([]string, error) {
	sinceUnix, hasSince, err := parseSinceUnix(since)
	if err != nil {
		return nil, err
	}
	var where []string
	if unread {
		where = append(where, "m.read = 0")
	}
	if flagged {
		where = append(where, "m.flagged != 0")
	}
	if hasSince {
		where = append(where, "m.date_received >= "+strconv.FormatInt(sinceUnix, 10))
	}
	return where, nil
}

func (c *Client) getMessagesFromIndex(accountName string, mbox *indexMailbox, limit, offset int, unreadOnly, flaggedOnly bool, since string) ([]Message, error) {
	where, err := indexMessageFilterConditions(unreadOnly, flaggedOnly, since)
	if err != nil {
		return nil, err
	}
	where = append([]string{"m.deleted = 0", indexMailboxMembershipCondition(mbox)}, where...)
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

func (c *Client) getMessageEnvelopeFromIndex(accountName string, mbox *indexMailbox, messageID string) (*Message, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(messageID), 10, 64)
	if err != nil {
		return nil, nil
	}
	query := buildIndexMessageSelect(accountName, mbox.Name) + fmt.Sprintf(`
where %s
	and m.deleted = 0
	and m.ROWID = %d
limit 1;
`, indexMailboxMembershipCondition(mbox), id)

	var rows []indexMessage
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	message := rows[0].message()
	return &message, nil
}

// getMessageEnvelopesForVerification reads every requested local message ID
// from one mailbox-scoped Envelope Index snapshot. Its cost depends on the
// batch size, not on the total number of messages in the mailbox.
func (c *Client) getMessageEnvelopesForVerification(accountName, mailboxName string, messageIDs []string) (map[string]Message, error) {
	mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("mailbox unavailable in Envelope Index: %s", mailboxName)
	}
	numeric := make([]string, 0, len(messageIDs))
	for _, messageID := range messageIDs {
		if id, parseErr := strconv.ParseInt(strings.TrimSpace(messageID), 10, 64); parseErr == nil {
			numeric = append(numeric, strconv.FormatInt(id, 10))
		}
	}
	if len(numeric) == 0 {
		return map[string]Message{}, nil
	}
	query := buildIndexMessageSelect(accountName, mbox.Name) + fmt.Sprintf(`
where %s
	and m.deleted = 0
	and m.ROWID in (%s);
`, indexMailboxMembershipCondition(mbox), strings.Join(numeric, ", "))
	var rows []indexMessage
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, err
	}
	byID := make(map[string]Message, len(rows))
	for _, row := range rows {
		message := row.message()
		byID[message.ID] = message
	}
	return byID, nil
}

func indexIdentityConditions(identities []StableIdentity) ([]string, error) {
	conditions := make([]string, 0, len(identities))
	seen := make(map[string]bool)
	for _, identity := range identities {
		if !identity.hasFallback() {
			return nil, fmt.Errorf("stable identity lacks complete indexed metadata")
		}
		key := identity.verificationKey()
		if seen[key] {
			continue
		}
		seen[key] = true
		senderCondition := "(case when coalesce(a.comment, '') = '' then coalesce(a.address, '') else a.comment || ' <' || a.address || '>' end) = " + sqlQuote(identity.Sender)
		rfc := validRFCMessageID(identity.RFCMessageID)
		if rfc != "" {
			address := stableSenderAddress(identity.Sender)
			if address == "" {
				return nil, fmt.Errorf("RFC stable identity lacks a valid sender address")
			}
			senderCondition = "lower(coalesce(a.address, '')) = " + sqlQuote(address)
		}
		condition := fmt.Sprintf(`(`+indexSubjectExpression+` = %s
        and %s
        and coalesce(strftime('%%Y-%%m-%%dT%%H:%%M:%%SZ', m.date_sent, 'unixepoch'), '') = %s`, sqlQuote(identity.Subject), senderCondition, sqlQuote(canonicalIdentityDate(identity.DateSent)))
		// A confirmed RFC header tolerates server reserialization changing size.
		if rfc == "" {
			condition += fmt.Sprintf(" and m.size = %d", identity.MessageSize)
		}
		conditions = append(conditions, condition+")")
	}
	return conditions, nil
}

func indexSearchTermConditions(terms []string) []string {
	conditions := make([]string, 0, len(terms))
	for _, term := range terms {
		needle := sqlQuote(escapeSQLLikePattern(term))
		conditions = append(conditions, fmt.Sprintf(`(
		lower(`+indexSubjectExpression+`) like '%%' || %s || '%%' escape '\'
		or lower(coalesce(a.comment, '')) like '%%' || %s || '%%' escape '\'
		or lower(coalesce(a.address, '')) like '%%' || %s || '%%' escape '\'
		or lower(coalesce(su.summary, '')) like '%%' || %s || '%%' escape '\'
	)`, needle, needle, needle, needle))
	}
	return conditions
}

// scanMessagesFromIndex reads one page of a mailbox in a fixed order: newest
// receipt first, then highest local ID. The position after the last row
// resumes the traversal without an offset, so mail arriving between pages
// cannot shift a row across a page boundary.
func (c *Client) scanMessagesFromIndex(accountName string, mbox *indexMailbox, req ScanRequest, limit int, after *ScanPosition) (scanPage, error) {
	where, err := indexMessageFilterConditions(req.Unread, false, req.Since)
	if err != nil {
		return scanPage{}, err
	}
	where = append([]string{indexMailboxMembershipCondition(mbox), "m.deleted = 0"}, where...)
	where = append(where, indexSearchTermConditions(searchTerms(req.Query))...)
	if after != nil {
		where = append(where, fmt.Sprintf("(coalesce(m.date_received, 0) < %d or (coalesce(m.date_received, 0) = %d and m.ROWID < %d))", after.ReceivedUnix, after.ReceivedUnix, after.ID))
	}
	query := buildIndexMessageSelect(accountName, mbox.Name) + "where " + strings.Join(where, "\n\tand ") + "\norder by coalesce(m.date_received, 0) desc, m.ROWID desc"
	if limit > 0 {
		query += "\nlimit " + strconv.Itoa(limit)
	}
	query += ";"
	var rows []indexMessage
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return scanPage{}, err
	}
	page := scanPage{Messages: indexMessagesToMessages(rows)}
	if len(rows) == 0 {
		return page, nil
	}
	last := rows[len(rows)-1]
	page.Last = &ScanPosition{ReceivedUnix: last.ReceivedUnix, ID: last.ID}
	if limit <= 0 || len(rows) < limit {
		return page, nil
	}
	where = append(where, fmt.Sprintf("(coalesce(m.date_received, 0) < %d or (coalesce(m.date_received, 0) = %d and m.ROWID < %d))", last.ReceivedUnix, last.ReceivedUnix, last.ID))
	var counts []struct {
		Remaining int `json:"Remaining"`
	}
	if err := c.runEnvelopeIndexQuery("select count(*) as Remaining"+indexMessageFrom+"where "+strings.Join(where, "\n\tand ")+";", &counts); err != nil {
		return scanPage{}, err
	}
	if len(counts) != 1 {
		return scanPage{}, fmt.Errorf("envelope index returned no remaining count for %s", mbox.Name)
	}
	page.Remaining = counts[0].Remaining
	return page, nil
}

func (c *Client) searchMessagesFromIndex(queryText, accountName string, mbox *indexMailbox, limit int, since string) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	terms := searchTerms(queryText)
	if len(terms) == 0 {
		return []Message{}, nil
	}
	where, err := indexMessageFilterConditions(false, false, since)
	if err != nil {
		return nil, err
	}
	where = append([]string{indexMailboxMembershipCondition(mbox), "m.deleted = 0"}, where...)
	where = append(where, indexSearchTermConditions(terms)...)
	query := buildIndexMessageSelect(accountName, mbox.Name) + fmt.Sprintf(`
where %s
order by m.date_received desc
limit %d;
`, strings.Join(where, "\n\tand "), limit)

	var rows []indexMessage
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, err
	}
	return indexMessagesToMessages(rows), nil
}

type indexMailboxCandidates struct{ exact, literal, nested, broad string }

// indexMailboxPreference keeps Archive's provider ambiguity explicit. An exact
// Gmail mailbox wins, then literal/nested Archive, then the broad nested leaf.
func indexMailboxPreference(accountID, mailbox string) indexMailboxCandidates {
	preference := indexMailboxCandidates{
		exact: indexMailboxExactCondition(accountID, mailbox),
		broad: indexMailboxBroadCondition(accountID, mailbox),
	}
	if normalizeMailboxAlias(mailbox) == "archive" {
		preference.literal, preference.nested = indexMailboxArchiveConditions(accountID)
	}
	return preference
}

func indexMailboxExactCondition(accountID, mailbox string) string {
	var forms []string
	if isArchiveAlias(mailbox) {
		forms = []string{"%5BGmail%5D/All%20Mail"}
	} else {
		forms = escapeMailboxURLPathForms(mailbox)
	}
	comparisons := make([]string, len(forms))
	for i, escaped := range forms {
		comparisons[i] = indexMailboxPath(accountID) + " = " + sqlQuote(escaped)
		if strings.EqualFold(mailbox, "INBOX") {
			comparisons[i] += " collate nocase"
		}
	}
	comparison := comparisons[0]
	if len(comparisons) > 1 {
		comparison = "(" + strings.Join(comparisons, " or ") + ")"
	}
	return "(" + indexAccountMailboxCondition(accountID) + " and " + comparison + ")"
}

func indexMailboxBroadCondition(accountID, mailbox string) string {
	leaves := escapedMailboxLeafForms(mailbox)
	clauses := make([]string, len(leaves))
	for i, leaf := range leaves {
		clauses[i] = indexMailboxPath(accountID) + " like " + sqlQuote("%/"+escapeSQLLikePattern(leaf)) + ` escape '\'`
	}
	clause := clauses[0]
	if len(clauses) > 1 {
		clause = "(" + strings.Join(clauses, " or ") + ")"
	}
	return "(" + indexAccountMailboxCondition(accountID) + " and " + clause + ")"
}

func indexMailboxArchiveConditions(accountID string) (literal, nested string) {
	prefix := "(" + indexAccountMailboxCondition(accountID) + " and " + indexMailboxPath(accountID)
	return prefix + " = 'Archive')", prefix + ` like '%/Archive' escape '\')`
}

func indexMailboxCandidateSelect(accountID, mailbox, projection string) string {
	preference := indexMailboxPreference(accountID, mailbox)
	where := preference.exact + " or " + preference.broad
	order := "case when " + preference.exact + " then 0 else 3 end"
	if preference.literal != "" {
		where += " or " + preference.literal + " or " + preference.nested
		order = "case when " + preference.exact + " then 0 when " + preference.literal + " then 1 when " + preference.nested + " then 2 else 3 end"
	}
	return "select " + projection + " from mailboxes where " + where + " order by " + order + ", ROWID limit 1"
}
