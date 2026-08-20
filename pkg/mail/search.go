package mail

import (
	"encoding/json"
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

// SearchOptions controls the safety contract for searches that span multiple
// mailboxes. A partial result is unsafe for workflows such as duplicate
// detection, so callers must explicitly opt in before receiving one.
type SearchOptions struct {
	AllowPartial bool
}

// SearchMailbox identifies a mailbox included in a search attempt.
type SearchMailbox struct {
	Account string `json:"account"`
	Mailbox string `json:"mailbox"`
}

// SearchMailboxFailure records a mailbox that could not be searched. Error is
// diagnostic metadata rather than a machine-stable error code.
type SearchMailboxFailure struct {
	Account string `json:"account"`
	Mailbox string `json:"mailbox"`
	Error   string `json:"error"`
}

// SearchResult is the structured search contract. Complete is false whenever
// at least one requested mailbox failed to return a result.
type SearchResult struct {
	Messages          []Message              `json:"messages"`
	Complete          bool                   `json:"complete"`
	SearchedMailboxes []SearchMailbox        `json:"searchedMailboxes"`
	FailedMailboxes   []SearchMailboxFailure `json:"failedMailboxes"`
}

// PartialSearchError means one or more mailboxes were unavailable. Its Result
// is populated so callers that deliberately opted in can inspect the precise
// scope of the incomplete result.
type PartialSearchError struct {
	Result SearchResult
}

func (e *PartialSearchError) Error() string {
	failed := make([]string, 0, len(e.Result.FailedMailboxes))
	for _, mailbox := range e.Result.FailedMailboxes {
		failed = append(failed, fmt.Sprintf("%s/%s: %s", mailbox.Account, mailbox.Mailbox, mailbox.Error))
	}
	return fmt.Sprintf("search incomplete; failed mailboxes: %s", strings.Join(failed, "; "))
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
// records its completeness. Cross-mailbox searches fail closed unless
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

	// If specific mailbox requested, use a single mailbox search.
	if mailboxName != "" {
		target := searchTarget{AccountName: accountName, MailboxName: mailboxName}
		if mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName); err != nil {
			return SearchResult{}, err
		} else if ok {
			messages, err := c.searchMessagesFromIndex(query, accountName, mbox, limit, since)
			if err != nil {
				if isEnvelopeIndexUnavailable(err) {
					c.warnEnvelopeIndexFallback(err)
					messages, err = c.searchMessagesInSingleMailboxJXA(query, accountName, mailboxName, limit, since)
					if err != nil {
						return SearchResult{}, err
					}
					return completeSearchResult(messages, []searchTarget{target}), nil
				}
				return SearchResult{}, err
			}
			return completeSearchResult(messages, []searchTarget{target}), nil
		}
		messages, err := c.searchMessagesInSingleMailbox(query, accountName, mailboxName, limit, since)
		if err != nil {
			return SearchResult{}, err
		}
		return completeSearchResult(messages, []searchTarget{target}), nil
	}

	targets, err := c.defaultSearchTargets(accountName)
	if err != nil {
		return SearchResult{}, err
	}

	if len(targets) == 0 {
		return completeSearchResult([]Message{}, targets), nil
	}

	// If only one mailbox, no need for parallelization
	if len(targets) == 1 {
		target := targets[0]
		messages, err := c.searchMessagesInSingleMailbox(query, target.AccountName, target.MailboxName, limit, since)
		if err != nil {
			return SearchResult{}, err
		}
		return completeSearchResult(messages, targets), nil
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
			failed = append(failed, SearchMailboxFailure{Account: target.AccountName, Mailbox: target.MailboxName, Error: errText})
			continue
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
		for _, inbox := range mailboxes {
			if inbox.Account == mailbox.Account && strings.EqualFold(inbox.Name, "INBOX") {
				addTarget(searchTarget{AccountName: inbox.Account, MailboxName: inbox.Name})
				break
			}
		}
	}

	return targets
}

func accountScopedSearchTargets(mailboxes []Mailbox, accountName string) []searchTarget {
	seen := make(map[string]bool)
	var targets []searchTarget
	hasArchiveTarget := false
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
		if isArchiveAlias(mailbox.Name) {
			hasArchiveTarget = true
		}
	}
	for _, preferredGroup := range [][]string{
		{"All Mail", "Archive"},
		{"Spam", "Junk"},
		{"Trash"},
	} {
		for _, mailbox := range mailboxes {
			if mailbox.Account != accountName || mailbox.Name == "" {
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
		if mailbox.TotalCount <= 0 {
			continue
		}
		if hasArchiveTarget && strings.EqualFold(mailbox.Name, "INBOX") {
			continue
		}
		addTarget(mailbox)
	}
	if !hasArchiveTarget {
		for _, mailbox := range mailboxes {
			if mailbox.Account == accountName && strings.EqualFold(mailbox.Name, "INBOX") {
				return append(targets, searchTarget{AccountName: mailbox.Account, MailboxName: mailbox.Name})
			}
		}
	}
	return targets
}

func (c *Client) searchMessagesInSingleMailboxJXA(query, accountName, mailboxName string, limit int, since string) ([]Message, error) {
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
	sinceUnix := int64(0)
	if strings.TrimSpace(since) != "" {
		parsedSince, _, err := parseSinceUnix(since)
		if err != nil {
			return nil, err
		}
		sinceUnix = parsedSince
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
	// Limit how many messages to check per mailbox for performance
	// Messages are typically sorted newest first, so this checks recent messages
	const maxToCheck = Math.min(messages.length, maxMessagesToCheck);

	for (let k = 0; k < maxToCheck && result.length < maxResults; k++) {
		const msg = messages[k];
		try {
			const subject = (msg.subject() || '').toLowerCase();
			const sender = (msg.sender() || '').toLowerCase();
			let content = '';
			try { content = (msg.content() || '').toLowerCase(); } catch(e) {}
			const received = msg.dateReceived();
			if (sinceDate.getTime() > 0 && (!received || received < sinceDate)) {
				continue;
			}
			const haystack = subject + ' ' + sender + ' ' + content;

			if (searchTerms.every(term => haystack.includes(term))) {
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
	throw new Error('mailbox search failed for ' + requestedMailbox + ': ' + e);
}

JSON.stringify(result);
`, string(termsJSON), limit, maxToCheck, escapedMailbox, sinceUnix*1000, jxaMailboxLookupHelper(), escapedAccount, jxaMailboxLookupExpression(mailboxName))

	output, err := c.runJXAWithTimeout(script, mailSearchTimeout())
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse search results JSON: %w", err)
	}

	if len(messages) == 0 && isArchiveAlias(mailboxName) {
		return c.searchArchiveMailboxWithWhoseJXA(query, accountName, mailboxName, limit, since)
	}

	return messages, nil
}

func (c *Client) searchArchiveMailboxWithWhoseJXA(query, accountName, mailboxName string, limit int, since string) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	terms := searchTerms(query)
	if len(terms) == 0 {
		return []Message{}, nil
	}
	termsJSON, err := json.Marshal(terms)
	if err != nil {
		return nil, err
	}
	sinceUnix := int64(0)
	if strings.TrimSpace(since) != "" {
		parsedSince, _, err := parseSinceUnix(since)
		if err != nil {
			return nil, err
		}
		sinceUnix = parsedSince
	}

	script := fmt.Sprintf(`
const mail = Application('Mail');
const result = [];
const seen = {};
const searchTerms = %s;
const maxResults = %d;
const requestedMailbox = '%s';
const sinceDate = new Date(%d);
%s

function addMessage(msg, accName, mboxName) {
	if (result.length >= maxResults) return;
	try { if (msg.deletedStatus()) return; } catch(e) {}
	try {
		const received = msg.dateReceived();
		if (sinceDate.getTime() > 0 && (!received || received < sinceDate)) return;
		const subject = (msg.subject() || '').toLowerCase();
		const sender = (msg.sender() || '').toLowerCase();
		const haystack = subject + ' ' + sender;
		if (!searchTerms.every(term => haystack.includes(term))) return;
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
	for (let t = 0; t < searchTerms.length && result.length < maxResults; t++) {
		const subjectMatches = mbox.messages.whose({subject: {_contains: searchTerms[t]}})();
		addMatches(subjectMatches, accName, mboxName);
		if (result.length < maxResults) {
			const senderMatches = mbox.messages.whose({sender: {_contains: searchTerms[t]}})();
			addMatches(senderMatches, accName, mboxName);
		}
	}
	result.sort((a, b) => b.dateReceived.localeCompare(a.dateReceived));
} catch (e) {
	throw new Error('archive mailbox search failed for ' + requestedMailbox + ': ' + e);
}

JSON.stringify(result.slice(0, maxResults));
`, string(termsJSON), limit, escapeJSString(mailboxName), sinceUnix*1000, jxaMailboxLookupHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName))

	output, err := c.runJXAWithTimeout(script, mailSearchTimeout())
	if err != nil {
		return nil, err
	}

	var messages []Message
	if err := json.Unmarshal([]byte(output), &messages); err != nil {
		return nil, fmt.Errorf("failed to parse archive search results JSON: %w", err)
	}

	return messages, nil
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
