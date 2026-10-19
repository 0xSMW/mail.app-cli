package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type searchTarget struct {
	AccountName string
	MailboxName string
}

// SearchOptions controls the safety contract for searches. A partial result is
// unsafe for workflows such as duplicate detection, so callers must explicitly
// opt in before receiving one.
type SearchOptions struct {
	AllowPartial bool
}

// SearchMailbox identifies a mailbox included in a search attempt.
type SearchMailbox struct {
	Account string `json:"account"`
	Mailbox string `json:"mailbox"`
}

// SearchMailboxFailure records a mailbox that could not be searched completely.
// Error is diagnostic metadata rather than a machine-stable error code.
type SearchMailboxFailure struct {
	Account         string `json:"account"`
	Mailbox         string `json:"mailbox"`
	Error           string `json:"error"`
	Truncated       bool   `json:"truncated,omitempty"`
	ReadFailures    int    `json:"readFailures,omitempty"`
	ScannedMessages int    `json:"scannedMessages,omitempty"`
	TotalMessages   int    `json:"totalMessages,omitempty"`
}

// SearchResult is the structured search contract. Complete is false whenever
// at least one requested mailbox could not be searched completely.
type SearchResult struct {
	Messages          []Message              `json:"messages"`
	Complete          bool                   `json:"complete"`
	SearchedMailboxes []SearchMailbox        `json:"searchedMailboxes"`
	FailedMailboxes   []SearchMailboxFailure `json:"failedMailboxes"`
}

// PartialSearchError means one or more mailboxes were searched incompletely.
// Its Result is populated so callers that deliberately opted in can inspect the precise
// scope of the incomplete result.
type PartialSearchError struct {
	Result SearchResult
}

func (e *PartialSearchError) Error() string {
	failed := make([]string, 0, len(e.Result.FailedMailboxes))
	for _, mailbox := range e.Result.FailedMailboxes {
		failed = append(failed, fmt.Sprintf("%s/%s: %s", mailbox.Account, mailbox.Mailbox, mailbox.Error))
	}
	return fmt.Sprintf("search incomplete; incomplete mailboxes: %s", strings.Join(failed, "; "))
}

type mailboxSearchResult struct {
	target   searchTarget
	messages []Message
	err      error
}

var searchTermPattern = regexp.MustCompile(`[[:alnum:]]+`)

func searchTerms(query string) []string {
	matches := searchTermPattern.FindAllString(strings.ToLower(query), -1)
	terms := make([]string, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, match := range matches {
		if match == "" || seen[match] {
			continue
		}
		seen[match] = true
		terms = append(terms, match)
	}
	return terms
}

func (c *Client) SearchMessages(query string, limit int) ([]Message, error) {
	if c.nativeEnabled() {
		return c.nativeSearchLegacy(query, limit)
	}
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
	return c.SearchMessagesJSONSince(query, accountName, mailboxName, limit, "")
}

func (c *Client) SearchMessagesJSONSince(query string, accountName string, mailboxName string, limit int, since string) ([]Message, error) {
	result, err := c.SearchMessagesJSONSinceWithOptions(query, accountName, mailboxName, limit, since, SearchOptions{})
	if err != nil {
		return nil, err
	}
	return result.Messages, nil
}

// SearchMessagesJSONSinceWithOptions searches the requested mailbox scope and
// records its completeness. Searches fail closed unless
// AllowPartial is set.
func (c *Client) SearchMessagesJSONSinceWithOptions(query string, accountName string, mailboxName string, limit int, since string, options SearchOptions) (SearchResult, error) {
	// Set a reasonable default limit if none specified
	if limit == 0 {
		limit = 50
	}

	if mailboxName == "" {
		if err := c.CheckEnvelopeIndex(); err != nil && isEnvelopeIndexUnavailable(err) {
			c.warnEnvelopeIndexFallback(err)
			return SearchResult{}, fmt.Errorf("fast search requires Mail Envelope Index access for archived/all-mailbox queries; grant Full Disk Access to the app launching mail-app-cli, or use --account and --mailbox for a bounded slow fallback")
		}
	}

	// Use the same completeness contract for explicit and discovered scopes.
	if mailboxName != "" {
		target := searchTarget{AccountName: accountName, MailboxName: mailboxName}
		messages, err := c.searchMessagesInSingleMailbox(query, accountName, mailboxName, limit, since)
		return applySearchOptions(collectSearchResults([]searchTarget{target}, []mailboxSearchResult{{target: target, messages: messages, err: err}}, limit), options)
	}

	// Reuse one read-only account snapshot for this search's parallel targets.
	// A named scope must not depend on unrelated account metadata.
	if c.nativeEnabled() {
		if accountName != "" {
			account, err := c.accountByName(accountName)
			if err != nil {
				return SearchResult{}, err
			}
			searchClient := *c
			searchClient.readAccountSnapshot = account
			c = &searchClient
		} else if _, err := c.GetAccountsJSON(); err != nil {
			return SearchResult{}, err
		}
	}

	targets, err := c.defaultSearchTargets(accountName)
	if err != nil {
		return SearchResult{}, err
	}

	if len(targets) == 0 {
		return completeSearchResult([]Message{}, targets), nil
	}

	// Search mailboxes in parallel
	results := make(chan mailboxSearchResult, len(targets))

	// Launch goroutine for each mailbox
	runWithMailCommandLimit(targets, func(target searchTarget) {
		messages, err := c.searchMessagesInSingleMailbox(query, target.AccountName, target.MailboxName, limit, since)
		results <- mailboxSearchResult{target: target, messages: messages, err: err}
	})

	collected := make([]mailboxSearchResult, 0, len(targets))
	for i := 0; i < len(targets); i++ {
		collected = append(collected, <-results)
	}
	result := collectSearchResults(targets, collected, limit)
	return applySearchOptions(result, options)
}

func applySearchOptions(result SearchResult, options SearchOptions) (SearchResult, error) {
	if !result.Complete && !options.AllowPartial {
		return result, &PartialSearchError{Result: result}
	}
	return result, nil
}

func completeSearchResult(messages []Message, targets []searchTarget) SearchResult {
	return SearchResult{
		Messages:          messages,
		Complete:          true,
		SearchedMailboxes: searchMailboxes(targets),
		FailedMailboxes:   []SearchMailboxFailure{},
	}
}

func collectSearchResults(targets []searchTarget, results []mailboxSearchResult, limit int) SearchResult {
	byTarget := make(map[searchTarget]mailboxSearchResult, len(results))
	for _, result := range results {
		byTarget[result.target] = result
	}

	allMessages := make([]Message, 0)
	seenMessages := make(map[string]bool)
	failed := make([]SearchMailboxFailure, 0)
	for _, target := range targets {
		result, ok := byTarget[target]
		if !ok || result.err != nil {
			errText := "search result was not returned"
			if ok {
				errText = result.err.Error()
			}
			failure := SearchMailboxFailure{Account: target.AccountName, Mailbox: target.MailboxName, Error: errText}
			var coverage *mailboxSearchCoverageError
			if errors.As(result.err, &coverage) {
				failure.Truncated = coverage.Truncated
				failure.ReadFailures = coverage.ReadFailures
				failure.ScannedMessages = coverage.ScannedMessages
				failure.TotalMessages = coverage.TotalMessages
			}
			failed = append(failed, failure)
		}
		for _, message := range result.messages {
			key := message.Account + "\x00" + message.ID
			if seenMessages[key] {
				continue
			}
			seenMessages[key] = true
			allMessages = append(allMessages, message)
		}
	}

	sort.Slice(allMessages, func(i, j int) bool { return allMessages[i].DateReceived > allMessages[j].DateReceived })
	if limit > 0 && len(allMessages) > limit {
		allMessages = allMessages[:limit]
	}

	return SearchResult{
		Messages:          allMessages,
		Complete:          len(failed) == 0,
		SearchedMailboxes: searchMailboxes(targets),
		FailedMailboxes:   failed,
	}
}

func searchMailboxes(targets []searchTarget) []SearchMailbox {
	mailboxes := make([]SearchMailbox, 0, len(targets))
	for _, target := range targets {
		mailboxes = append(mailboxes, SearchMailbox{Account: target.AccountName, Mailbox: target.MailboxName})
	}
	return mailboxes
}

func (c *Client) searchMessagesInSingleMailbox(query, accountName, mailboxName string, limit int, since string) ([]Message, error) {
	if mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName); err != nil {
		return nil, err
	} else if ok {
		messages, err := c.searchMessagesFromIndex(query, accountName, mbox, limit, since)
		if err != nil {
			if isEnvelopeIndexUnavailable(err) {
				c.warnEnvelopeIndexFallback(err)
				return c.searchMessagesInSingleMailboxJXA(query, accountName, mailboxName, limit, since)
			}
			return nil, err
		}
		return messages, nil
	}

	return c.searchMessagesInSingleMailboxJXA(query, accountName, mailboxName, limit, since)
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

	return defaultSearchTargetsFromMailboxes(mailboxes, accountName, enabledAccounts), nil
}

func defaultSearchTargetsFromMailboxes(mailboxes []Mailbox, accountName string, enabledAccounts map[string]bool) []searchTarget {
	if accountName != "" {
		targets := accountScopedSearchTargets(mailboxes, accountName)
		if len(targets) > 0 {
			return targets
		}
		return []searchTarget{{AccountName: accountName, MailboxName: "INBOX"}}
	}

	var targets []searchTarget
	seen := make(map[string]bool)
	accounts := make(map[string]bool)
	addTarget := func(target searchTarget) {
		key := target.AccountName + "\x00" + strings.ToLower(target.MailboxName)
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, target)
	}
	for _, mailbox := range mailboxes {
		if mailbox.Account == "" || accounts[mailbox.Account] {
			continue
		}
		if accountName == "" && !enabledAccounts[mailbox.Account] {
			continue
		}
		accounts[mailbox.Account] = true
		for _, target := range accountScopedSearchTargets(mailboxes, mailbox.Account) {
			addTarget(target)
		}
	}

	return targets
}

func accountScopedSearchTargets(mailboxes []Mailbox, accountName string) []searchTarget {
	seen := make(map[string]bool)
	var targets []searchTarget
	addTarget := func(mailbox Mailbox) {
		if mailbox.Account != accountName || mailbox.Name == "" {
			return
		}
		key := mailbox.Account + "\x00" + strings.ToLower(mailbox.Name)
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, searchTarget{AccountName: mailbox.Account, MailboxName: mailbox.Name})
	}
	for _, preferredGroup := range [][]string{
		{"All Mail", "Archive"},
		{"Spam", "Junk"},
		{"Trash"},
	} {
		for _, mailbox := range mailboxes {
			if mailbox.Account != accountName || mailbox.Name == "" || mailbox.TotalCount == 0 {
				continue
			}
			for _, preferred := range preferredGroup {
				if !strings.EqualFold(mailbox.Name, preferred) {
					continue
				}
				addTarget(mailbox)
				goto nextPreferredGroup
			}
		}
	nextPreferredGroup:
	}
	for _, mailbox := range mailboxes {
		if mailbox.TotalCount == 0 {
			continue
		}
		addTarget(mailbox)
	}
	for _, mailbox := range mailboxes {
		if mailbox.Account == accountName && strings.EqualFold(mailbox.Name, "INBOX") {
			addTarget(mailbox)
		}
	}

	return targets
}

func (c *Client) searchMessagesInSingleMailboxJXA(query, accountName, mailboxName string, limit int, since string) ([]Message, error) {
	if c.nativeEnabled() {
		return c.nativeSearchMailbox(query, accountName, mailboxName, limit, since, true)
	}
	// Use helper for escaping
	terms := searchTerms(query)
	if len(terms) == 0 {
		return []Message{}, nil
	}
	termsJSON, err := json.Marshal(terms)
	if err != nil {
		return nil, err
	}
	escapedAccount := escapeJSString(accountName)
	escapedMailbox := escapeJSString(mailboxName)
	sinceUnix, _, err := parseSinceUnix(since)
	if err != nil {
		return nil, err
	}
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
let returnValue;
const searchTerms = %s;
const maxResults = %d;
const maxMessagesToCheck = %d;
const requestedMailbox = '%s';
const sinceDate = new Date(%d);
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const accName = acc.name();
	const mboxName = mbox.name();
	const messages = mbox.messages();
 const maxToCheck = Math.min(messages.length, maxMessagesToCheck);
 let scanned = 0;
 let readFailures = 0;
 for (let k = 0; k < maxToCheck && (maxResults <= 0 || result.length < maxResults); k++) {
  scanned++;
  const msg = messages[k];
  try {
   // Date is required only for a date filter or for a matching result.
   let received;
   if (sinceDate.getTime() > 0) {
    received = msg.dateReceived();
    if (!received) throw new Error('required received date unavailable');
    if (received < sinceDate) continue;
   }
   // A known subject/sender match does not require reading the body.
   let subject = '', sender = '', fieldFailures = 0;
   try { subject = msg.subject() || ''; } catch(e) { fieldFailures++; }
   try { sender = msg.sender() || ''; } catch(e) { fieldFailures++; }
   let haystack = (subject + ' ' + sender).toLowerCase();
   if (!searchTerms.every(term => haystack.includes(term))) {
    let content;
    try { content = msg.content() || ''; } catch(e) { throw new Error('required content unavailable'); }
    haystack += ' ' + content.toLowerCase();
    if (!searchTerms.every(term => haystack.includes(term))) {
     if (fieldFailures) throw new Error('required subject or sender unavailable');
     continue;
    }
   }
   // Optional presentation fields must not hide a proven match.
   function optional(read, fallback) { try { return read(); } catch(e) { return fallback; } }
   received = received || msg.dateReceived();
   result.push({
    id: String(msg.id()), subject: subject, sender: sender,
    dateReceived: received.toISOString(),
    dateSent: optional(() => msg.dateSent().toISOString(), ''),
    read: optional(() => msg.readStatus(), false),
    flagged: optional(() => msg.flaggedStatus(), false),
    messageSize: optional(() => msg.messageSize(), 0),
    mailbox: mboxName, account: accName
   });
  } catch(e) { readFailures++; }
 }
 // Reaching the requested output limit is ordinary success. Exhausting the
 // scan budget before that limit leaves an explicitly incomplete result.
 const truncated = scanned < messages.length && !(maxResults > 0 && result.length >= maxResults);
 returnValue = {messages: result, truncated: truncated, readFailures: readFailures,
  scannedMessages: scanned, totalMessages: messages.length};
} catch (e) {
	throw new Error('mailbox search failed for ' + requestedMailbox + ': ' + e);
}

JSON.stringify(returnValue);
`, string(termsJSON), limit, maxToCheck, escapedMailbox, sinceUnix*1000, jxaMailboxLookupHelper(), escapedAccount, jxaMailboxLookupExpression(mailboxName))

	output, err := c.runJXAWithTimeout(script, mailSearchTimeout())
	if err != nil {
		return nil, err
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &envelope); err != nil {
		return nil, fmt.Errorf("failed to parse search results JSON: %w", err)
	}
	for _, field := range []string{"messages", "truncated", "readFailures", "scannedMessages", "totalMessages"} {
		if value, ok := envelope[field]; !ok || string(value) == "null" {
			return nil, fmt.Errorf("invalid search coverage response: missing %s", field)
		}
	}
	var result mailboxSearchCoverageError
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		return nil, fmt.Errorf("failed to parse search results JSON: %w", err)
	}
	if result.Messages == nil || result.ScannedMessages < 0 || result.TotalMessages < result.ScannedMessages || result.ReadFailures < 0 || result.ReadFailures > result.ScannedMessages || len(result.Messages) > result.ScannedMessages || (limit > 0 && len(result.Messages) > limit) {
		return nil, fmt.Errorf("invalid search coverage response: inconsistent counts")
	}
	truncated := result.ScannedMessages < result.TotalMessages && !(limit > 0 && len(result.Messages) >= limit)
	if result.Truncated != truncated {
		return nil, fmt.Errorf("invalid search coverage response: inconsistent truncation")
	}
	if result.Truncated || result.ReadFailures > 0 {
		return result.Messages, &result
	}
	return result.Messages, nil
}

// mailboxSearchCoverageError carries observed matches even when the bounded
// bridge could not determine every required match. It is internal; callers see
// the additive diagnostic fields on SearchMailboxFailure.
type mailboxSearchCoverageError struct {
	Messages        []Message `json:"messages"`
	Truncated       bool      `json:"truncated"`
	ReadFailures    int       `json:"readFailures"`
	ScannedMessages int       `json:"scannedMessages"`
	TotalMessages   int       `json:"totalMessages"`
}

func (e *mailboxSearchCoverageError) Error() string {
	return fmt.Sprintf("mailbox search incomplete (truncated: %v; read failures: %d; scanned: %d/%d)", e.Truncated, e.ReadFailures, e.ScannedMessages, e.TotalMessages)
}

func mailSearchTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("MAIL_APP_CLI_SEARCH_TIMEOUT"))
	if raw == "" {
		return 8 * time.Second
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 8 * time.Second
	}
	return time.Duration(seconds) * time.Second
}
