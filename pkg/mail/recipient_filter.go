package mail

import (
	"context"
	"fmt"
	netmail "net/mail"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const recipientCandidatePageSize = 200
const recipientQueryBudget = 45 * time.Second

// NormalizeRecipientFilter accepts one mailbox address. Matching is case
// insensitive; dots, plus tags, and domains are otherwise preserved literally.
func NormalizeRecipientFilter(value string) (string, error) {
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("--recipient requires one email address")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	address, err := netmail.ParseAddress(value)
	if err != nil || address.Name != "" {
		return "", fmt.Errorf("--recipient requires one email address")
	}
	at := strings.LastIndex(address.Address, "@")
	if at <= 0 || at == len(address.Address)-1 {
		return "", fmt.Errorf("--recipient requires one email address")
	}
	return strings.ToLower(address.Address), nil
}

// MatchRecipient uses only inspected To/Cc/Bcc collections. A positive match
// remains known even if a different field is unavailable. A negative match is
// known only after all three collections were inspected.
func MatchRecipient(message Message, recipient string) (matched, known bool) {
	known = true
	for _, field := range recipientFields(&message)[:3] {
		if field.status.State != "available" {
			known = false
		}
		if field.status.State != "available" && field.status.State != "partial" {
			continue
		}
		for _, address := range *field.values {
			if strings.EqualFold(address, recipient) {
				return true, true
			}
		}
	}
	return false, known
}

// RecipientQueryRequest paginates confirmed matches, rather than candidate
// envelopes. Query and metadata filters are applied before inspecting headers.
type RecipientQueryRequest struct {
	Scopes                                        []SearchMailbox
	Query, Recipient, Since, Sender, SenderDomain string
	Limit, Offset                                 int
	Unread, Flagged, WithContent                  bool
}

func normalizeRecipientQuery(req RecipientQueryRequest) (RecipientQueryRequest, error) {
	address, err := NormalizeRecipientFilter(req.Recipient)
	if err != nil {
		return req, err
	}
	if address == "" {
		return req, fmt.Errorf("recipient query requires --recipient")
	}
	req.Recipient = address
	if req.Limit < 0 || req.Offset < 0 || req.Limit > int(^uint(0)>>1)-req.Offset {
		return req, fmt.Errorf("recipient query needs non-negative limit and offset without overflow")
	}
	if strings.TrimSpace(req.Query) != "" && len(searchTerms(req.Query)) == 0 {
		return req, fmt.Errorf("query must contain a searchable term")
	}
	if _, _, err := parseSinceUnix(req.Since); err != nil {
		return req, err
	}
	for _, scope := range req.Scopes {
		if strings.TrimSpace(scope.Account) == "" || strings.TrimSpace(scope.Mailbox) == "" {
			return req, fmt.Errorf("recipient query scopes need account and mailbox")
		}
	}
	return req, nil
}

// RecipientMessages always reads scoped index candidates live. An unavailable
// index or unreadable recipient field cannot establish an empty result. Partial
// results contain only confirmed matches and describe each incomplete mailbox.
func (c *Client) RecipientMessages(req RecipientQueryRequest) (SearchResult, error) {
	req, err := normalizeRecipientQuery(req)
	if err != nil {
		return SearchResult{}, err
	}
	ctx, cancel := context.WithTimeout(c.Context(), recipientQueryBudget)
	defer cancel()
	client := c.WithContext(ctx)
	result := collectRecipientMessages(req, func(scope SearchMailbox, after *ScanPosition) (scanPage, error) {
		if err := client.Done(); err != nil {
			return scanPage{}, err
		}
		mbox, ok, err := client.resolveIndexMailbox(scope.Account, scope.Mailbox)
		if err != nil {
			return scanPage{}, err
		}
		if !ok {
			return scanPage{}, fmt.Errorf("exact recipient filtering requires Mail Envelope Index access for %s/%s", scope.Account, scope.Mailbox)
		}
		// Discovered scopes retain their full physical path. Carry it into the
		// candidate envelope as well as the subsequent scoped header reads.
		mbox.Name = scope.Mailbox
		page, err := client.scanMessagesFromIndex(scope.Account, mbox, ScanRequest{Query: req.Query, Since: req.Since, Unread: req.Unread, Flagged: req.Flagged}, recipientCandidatePageSize, after)
		if err != nil {
			return scanPage{}, err
		}
		// Preserve the original Last/Remaining boundary even if metadata filters
		// remove every candidate on this page.
		page.Messages = FilterBySender(page.Messages, req.Sender, req.SenderDomain)
		page.Messages = client.enrichMessagesWithRecipients(scope.Account, scope.Mailbox, page.Messages, req.Recipient)
		return page, nil
	})
	if req.WithContent && len(result.Messages) > 0 {
		result.Messages = client.enrichMessagesWithContent("", "", result.Messages)
	}
	return result, nil
}

func collectRecipientMessages(req RecipientQueryRequest, read func(SearchMailbox, *ScanPosition) (scanPage, error)) SearchResult {
	result := SearchResult{Messages: []Message{}, Complete: true, SearchedMailboxes: []SearchMailbox{}, FailedMailboxes: []SearchMailboxFailure{}}
	indices := map[string]int{}
	for _, scope := range uniqueScanScopes(req.Scopes) {
		result.SearchedMailboxes = append(result.SearchedMailboxes, scope)
		var after *ScanPosition
		scopeMatches := map[string]bool{}
		inspected := 0
		fail := func(err error, unknown int) {
			result.Complete = false
			result.FailedMailboxes = append(result.FailedMailboxes, SearchMailboxFailure{Account: scope.Account, Mailbox: scope.Mailbox, Error: err.Error(), ReadFailures: unknown, ScannedMessages: inspected})
		}
		for {
			page, err := read(scope, after)
			if err != nil {
				fail(err, 0)
				break
			}
			inspected += len(page.Messages)
			unknown := 0
			for _, message := range page.Messages {
				matched, known := MatchRecipient(message, req.Recipient)
				if !known {
					unknown++
				}
				if !matched {
					continue
				}
				message.Account, message.Mailbox = scope.Account, scope.Mailbox
				key := scope.Account + "\x00" + message.ID
				scopeMatches[key] = true
				if index, ok := indices[key]; ok {
					mergeRecipientEvidence(&result.Messages[index], message)
				} else {
					indices[key] = len(result.Messages)
					result.Messages = append(result.Messages, message)
				}
			}
			if unknown > 0 {
				fail(fmt.Errorf("recipient match unavailable for %d candidate messages; retry this mailbox", unknown), unknown)
				break
			}
			// The top offset+limit matches from each mailbox suffice for the global
			// slice, even when the same identity belongs to several mailbox labels.
			if page.Remaining == 0 || req.Limit > 0 && len(scopeMatches) >= req.Offset+req.Limit {
				break
			}
			if page.Last == nil || after != nil && (page.Last.ReceivedUnix > after.ReceivedUnix || page.Last.ReceivedUnix == after.ReceivedUnix && page.Last.ID >= after.ID) {
				fail(fmt.Errorf("recipient candidate traversal did not advance"), 0)
				break
			}
			position := *page.Last
			after = &position
		}
	}
	sort.SliceStable(result.Messages, func(i, j int) bool {
		left, right := result.Messages[i], result.Messages[j]
		leftDate, rightDate := left.DateReceived, right.DateReceived
		// Missing index dates share the same zero receipt-time boundary as
		// epoch dates; retain the raw query's ID tie order for that case.
		if leftDate == "" {
			leftDate = "1970-01-01T00:00:00Z"
		}
		if rightDate == "" {
			rightDate = "1970-01-01T00:00:00Z"
		}
		if leftDate != rightDate {
			return leftDate > rightDate
		}
		if left.Account != right.Account {
			return left.Account < right.Account
		}
		leftID, _ := strconv.ParseInt(left.ID, 10, 64)
		rightID, _ := strconv.ParseInt(right.ID, 10, 64)
		return leftID > rightID
	})
	if req.Offset >= len(result.Messages) {
		result.Messages = []Message{}
	} else {
		result.Messages = result.Messages[req.Offset:]
	}
	if req.Limit > 0 && len(result.Messages) > req.Limit {
		result.Messages = result.Messages[:req.Limit]
	}
	return result
}

// discoverRecipientScopes reads every required account independently from the
// index. General mailbox discovery can suppress one account's errors after a
// different account succeeds, which cannot establish exact-filter coverage.
func (c *Client) discoverRecipientScopes(accountName, kind string) ([]SearchMailbox, []SearchMailboxFailure, error) {
	var accounts []Account
	if accountName != "" {
		account, err := c.accountByName(accountName)
		if err != nil {
			return nil, nil, err
		}
		accounts = []Account{*account}
	} else {
		var err error
		accounts, err = c.GetAccountsJSON()
		if err != nil {
			return nil, nil, err
		}
	}
	scopes := []SearchMailbox{}
	failures := []SearchMailboxFailure{}
	for _, account := range accounts {
		if accountName == "" && kind == "" && !account.Enabled {
			continue
		}
		mailboxes, ok, err := c.recipientDiscoveryMailboxes(account)
		if strings.TrimSpace(account.ID) == "" {
			ok = false
		}
		if err != nil || !ok {
			detail := "exact recipient filtering requires a readable mailbox inventory in Mail Envelope Index"
			if err != nil {
				detail = err.Error()
			}
			failures = append(failures, SearchMailboxFailure{Account: account.Name, Mailbox: "(mailbox discovery)", Error: detail})
			continue
		}
		rootInbox, nestedInbox := false, false
		for _, mailbox := range mailboxes {
			leaf := mailboxLeafFromURL(mailbox.URL)
			if kind == "" && mailbox.TotalCount == 0 && !strings.EqualFold(leaf, "INBOX") || kind != "" && !isSpecialMailboxName(kind, leaf) {
				continue
			}
			// The general inventory intentionally exposes leaf names. Exact
			// coverage must keep Clients/Projects and Internal/Projects separate.
			_, authority, hasScheme := strings.Cut(mailbox.URL, "://")
			path, hasAccount := strings.CutPrefix(authority, account.ID+"/")
			name, err := url.PathUnescape(path)
			if !hasScheme || !hasAccount || err != nil || name == "" {
				failures = append(failures, SearchMailboxFailure{Account: account.Name, Mailbox: "(mailbox discovery)", Error: "unreadable physical mailbox path in Mail Envelope Index"})
				continue
			}
			if strings.EqualFold(name, "INBOX") {
				rootInbox = true
			} else if strings.EqualFold(leaf, "INBOX") {
				nestedInbox = true
			}
			// Existing explicit aliases can resolve a literal Archive to Gmail
			// All Mail. Do not let that alias silently replace a discovered row.
			resolved, found, err := c.resolveIndexMailbox(account.Name, name)
			if err != nil || !found || resolved.ID != mailbox.ID || resolved.URL != mailbox.URL {
				detail := "physical mailbox path cannot be resolved without aliasing or ambiguity"
				if err != nil {
					detail = err.Error()
				}
				failures = append(failures, SearchMailboxFailure{Account: account.Name, Mailbox: name, Error: detail})
				continue
			}
			scopes = append(scopes, SearchMailbox{Account: account.Name, Mailbox: name})
		}
		if kind == "" && !rootInbox {
			// Search always covers the account's root Inbox, even when it is
			// missing from the inventory. Keep the scope so its read fails
			// explicitly. A nested Inbox must not satisfy the root requirement
			// through the legacy resolver's leaf-name fallback.
			scopes = append(scopes, SearchMailbox{Account: account.Name, Mailbox: "INBOX"})
			if nestedInbox {
				failures = append(failures, SearchMailboxFailure{Account: account.Name, Mailbox: "INBOX", Error: "required root INBOX is missing from the Mail Envelope Index inventory"})
			}
		}
	}
	return uniqueScanScopes(scopes), failures, nil
}

// Keep physical URLs for this inventory; the general mailbox API exposes leaf
// names and therefore cannot establish that every distinct mailbox was covered.
func (c *Client) recipientDiscoveryMailboxes(account Account) ([]indexMailbox, bool, error) {
	var rows []indexMailbox
	query := "select ROWID as ID, url as URL, total_count as TotalCount, unread_count as UnreadCount from mailboxes where " + indexAccountMailboxCondition(account.ID) + " order by url;"
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		return nil, false, err
	}
	return rows, len(rows) > 0, nil
}

func recipientDiscoveryFailures(result SearchResult, failures []SearchMailboxFailure) SearchResult {
	if len(failures) > 0 {
		result.Complete = false
		result.FailedMailboxes = append(failures, result.FailedMailboxes...)
	}
	return result
}

// SearchRecipientMessages selects the same search mailbox categories as text
// search, retaining account discovery failures. Empty Query searches recipients.
func (c *Client) SearchRecipientMessages(account, mailbox string, req RecipientQueryRequest) (SearchResult, error) {
	if req.Limit == 0 {
		req.Limit = 50
	}
	validated, err := normalizeRecipientQuery(req)
	if err != nil {
		return SearchResult{}, err
	}
	req = validated
	ctx, cancel := context.WithTimeout(c.Context(), recipientQueryBudget)
	defer cancel()
	client := c.WithContext(ctx)
	var failures []SearchMailboxFailure
	if mailbox != "" {
		if strings.TrimSpace(account) == "" {
			return SearchResult{}, fmt.Errorf("--mailbox requires --account")
		}
		req.Scopes = []SearchMailbox{{Account: account, Mailbox: mailbox}}
	} else {
		req.Scopes, failures, err = client.discoverRecipientScopes(account, "")
		if err != nil {
			return SearchResult{}, err
		}
	}
	result, err := client.RecipientMessages(req)
	return recipientDiscoveryFailures(result, failures), err
}

// UnifiedRecipientMessages retains existing enabled-account and special
// mailbox selection rules, with filtering before the global limit.
func (c *Client) UnifiedRecipientMessages(kind string, req RecipientQueryRequest) (SearchResult, error) {
	validated, err := normalizeRecipientQuery(req)
	if err != nil {
		return SearchResult{}, err
	}
	req = validated
	ctx, cancel := context.WithTimeout(c.Context(), recipientQueryBudget)
	defer cancel()
	client := c.WithContext(ctx)
	req.Scopes = nil
	var failures []SearchMailboxFailure
	switch kind {
	case "inbox", "unread", "flagged":
		accounts, err := client.GetAccountsJSON()
		if err != nil {
			return SearchResult{}, err
		}
		for _, account := range accounts {
			if account.Enabled {
				req.Scopes = append(req.Scopes, SearchMailbox{Account: account.Name, Mailbox: "INBOX"})
			}
		}
		req.Unread, req.Flagged = kind == "unread", kind == "flagged"
	case "sent", "drafts", "trash", "junk":
		req.Scopes, failures, err = client.discoverRecipientScopes("", kind)
		if err != nil {
			return SearchResult{}, err
		}
	default:
		return SearchResult{}, fmt.Errorf("unknown unified mailbox type: %s", kind)
	}
	result, err := client.RecipientMessages(req)
	return recipientDiscoveryFailures(result, failures), err
}
