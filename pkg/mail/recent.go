package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	recentMessagesFile       = "recent_messages.json"
	maxRecentMessages        = 250
	recentMessagePermissions = 0600
)

type RecentMessage struct {
	ID              string   `json:"id"`
	Account         string   `json:"account"`
	Mailbox         string   `json:"mailbox"`
	OriginalMailbox string   `json:"originalMailbox,omitempty"`
	Subject         string   `json:"subject"`
	Sender          string   `json:"sender"`
	DateReceived    string   `json:"dateReceived"`
	DateSent        string   `json:"dateSent"`
	Read            bool     `json:"read"`
	Flagged         bool     `json:"flagged"`
	Deleted         bool     `json:"deleted,omitempty"`
	MessageSize     int      `json:"messageSize"`
	SearchTerms     []string `json:"searchTerms,omitempty"`
	LastAction      string   `json:"lastAction"`
	LastSeenAt      string   `json:"lastSeenAt"`
}

func recentMessagesPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "mail-app-cli", recentMessagesFile), nil
}

func loadRecentMessages() ([]RecentMessage, error) {
	path, err := recentMessagesPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []RecentMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var messages []RecentMessage
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func saveRecentMessages(messages []RecentMessage) error {
	path, err := recentMessagesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(pruneRecentMessages(messages), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, recentMessagePermissions)
}

func ClearRecentMessages() error {
	path, err := recentMessagesPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func pruneRecentMessages(messages []RecentMessage) []RecentMessage {
	sort.SliceStable(messages, func(i, j int) bool {
		return messages[i].LastSeenAt > messages[j].LastSeenAt
	})
	seen := make(map[string]bool, len(messages))
	pruned := make([]RecentMessage, 0, len(messages))
	for _, message := range messages {
		key := recentMessageKey(message.Account, message.ID)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		pruned = append(pruned, message)
		if len(pruned) >= maxRecentMessages {
			break
		}
	}
	return pruned
}

func recentMessageKey(account, id string) string {
	account = strings.TrimSpace(account)
	id = strings.TrimSpace(id)
	if account == "" || id == "" {
		return ""
	}
	return account + "\x00" + id
}

func RecordRecentMessage(message Message, action string) error {
	return recordRecentMessage(message, action, nil)
}

func RecordRecentSearchResults(messages []Message, query string) error {
	terms := searchTerms(query)
	for _, message := range messages {
		if err := recordRecentMessage(message, "search", terms); err != nil {
			return err
		}
	}
	return nil
}

func recordRecentMessage(message Message, action string, terms []string) error {
	if strings.TrimSpace(message.ID) == "" || strings.TrimSpace(message.Account) == "" {
		return nil
	}
	messages, err := loadRecentMessages()
	if err != nil {
		return err
	}
	key := recentMessageKey(message.Account, message.ID)
	now := time.Now().UTC().Format(time.RFC3339)
	hasEnvelope := message.Subject != "" || message.Sender != "" || message.DateReceived != "" || message.DateSent != "" || message.MessageSize != 0
	entry := RecentMessage{
		ID:              message.ID,
		Account:         message.Account,
		Mailbox:         message.Mailbox,
		OriginalMailbox: message.Mailbox,
		Subject:         message.Subject,
		Sender:          message.Sender,
		DateReceived:    message.DateReceived,
		DateSent:        message.DateSent,
		Read:            message.Read,
		Flagged:         message.Flagged,
		Deleted:         message.Deleted,
		MessageSize:     message.MessageSize,
		SearchTerms:     normalizeRecentTerms(terms),
		LastAction:      strings.TrimSpace(action),
		LastSeenAt:      now,
	}
	if entry.LastAction == "" {
		entry.LastAction = "seen"
	}

	replaced := false
	for i := range messages {
		if recentMessageKey(messages[i].Account, messages[i].ID) != key {
			continue
		}
		if entry.Mailbox == "" {
			entry.Mailbox = messages[i].Mailbox
		}
		if messages[i].OriginalMailbox != "" {
			entry.OriginalMailbox = messages[i].OriginalMailbox
		}
		if entry.Subject == "" {
			entry.Subject = messages[i].Subject
		}
		if entry.Sender == "" {
			entry.Sender = messages[i].Sender
		}
		if entry.DateReceived == "" {
			entry.DateReceived = messages[i].DateReceived
		}
		if entry.DateSent == "" {
			entry.DateSent = messages[i].DateSent
		}
		if !hasEnvelope && !entry.Read {
			entry.Read = messages[i].Read
		}
		if !hasEnvelope && !entry.Flagged {
			entry.Flagged = messages[i].Flagged
		}
		if !hasEnvelope && !entry.Deleted {
			entry.Deleted = messages[i].Deleted
		}
		if entry.MessageSize == 0 {
			entry.MessageSize = messages[i].MessageSize
		}
		entry.SearchTerms = mergeRecentTerms(messages[i].SearchTerms, entry.SearchTerms)
		messages[i] = entry
		replaced = true
		break
	}
	if !replaced {
		messages = append(messages, entry)
	}
	return saveRecentMessages(messages)
}

func (c *Client) RecordRecentEnvelope(accountName, mailboxName, messageID, action string) error {
	if strings.TrimSpace(accountName) == "" || strings.TrimSpace(mailboxName) == "" || strings.TrimSpace(messageID) == "" {
		return nil
	}
	if mbox, ok, err := c.resolveIndexMailbox(accountName, mailboxName); err != nil {
		return err
	} else if ok {
		message, err := c.getMessageEnvelopeFromIndex(accountName, mbox, messageID)
		if err == nil && message != nil {
			return RecordRecentMessage(*message, action)
		}
	}
	if message, err := c.getMessageEnvelopeJXA(accountName, mailboxName, messageID); err == nil && message != nil {
		return RecordRecentMessage(*message, action)
	}
	return RecordRecentMessage(Message{ID: messageID, Account: accountName, Mailbox: mailboxName}, action)
}

func (c *Client) getMessageEnvelopeJXA(accountName, mailboxName, messageID string) (*Message, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
let result = null;
const requestedMailbox = '%s';
%s
%s

try {
	const acc = mail.accounts.byName('%s');
	const mbox = %s;
	const msg = messageById(mbox, '%s');
	if (msg !== null) {
		result = {
			id: String(msg.id()),
			subject: msg.subject() || '',
			sender: msg.sender() || '',
			dateReceived: (msg.dateReceived() || new Date()).toISOString(),
			dateSent: (msg.dateSent() || new Date()).toISOString(),
			read: msg.readStatus(),
			flagged: msg.flaggedStatus(),
			messageSize: msg.messageSize(),
			mailbox: mbox.name(),
			account: acc.name()
		};
	}
} catch (e) {}

JSON.stringify(result);
`, escapeJSString(mailboxName), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(accountName), jxaMailboxLookupExpression(mailboxName), escapeJSString(messageID))

	output, err := c.runJXAWithTimeout(script, mailSearchTimeout())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(output) == "null" {
		return nil, nil
	}
	var message Message
	if err := json.Unmarshal([]byte(output), &message); err != nil {
		return nil, fmt.Errorf("failed to parse message envelope JSON: %w", err)
	}
	if message.ID == "" {
		return nil, nil
	}
	return &message, nil
}

func UpdateRecentMessageLocation(account, messageID, mailbox, action string) error {
	messages, err := loadRecentMessages()
	if err != nil {
		return err
	}
	key := recentMessageKey(account, messageID)
	if key == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range messages {
		if recentMessageKey(messages[i].Account, messages[i].ID) != key {
			continue
		}
		if strings.TrimSpace(mailbox) != "" {
			messages[i].Mailbox = mailbox
		}
		if strings.TrimSpace(action) != "" {
			messages[i].LastAction = action
		}
		messages[i].LastSeenAt = now
		return saveRecentMessages(messages)
	}
	return nil
}

func SearchRecentMessages(query, accountName, mailboxName string, limit int, since string) ([]Message, error) {
	if limit <= 0 {
		limit = 50
	}
	sinceUnix, hasSince, err := parseSinceUnix(since)
	if err != nil {
		return nil, err
	}
	terms := searchTerms(query)
	recent, err := loadRecentMessages()
	if err != nil {
		return nil, err
	}
	matches := make([]RecentMessage, 0, len(recent))
	for _, entry := range recent {
		if accountName != "" && entry.Account != accountName {
			continue
		}
		if mailboxName != "" && !strings.EqualFold(entry.Mailbox, mailboxName) {
			continue
		}
		if hasSince && !recentMessageSince(entry, sinceUnix) {
			continue
		}
		if !recentMessageMatchesTerms(entry, terms) {
			continue
		}
		matches = append(matches, entry)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].LastSeenAt > matches[j].LastSeenAt
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	messages := make([]Message, 0, len(matches))
	for _, entry := range matches {
		messages = append(messages, recentEntryToMessage(entry))
	}
	return messages, nil
}

func ResolveRecentMessage(selector, accountName, mailboxName string) (*RecentMessage, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return nil, fmt.Errorf("recent message selector is required")
	}
	recent, err := loadRecentMessages()
	if err != nil {
		return nil, err
	}
	for _, entry := range recent {
		if accountName != "" && entry.Account != accountName {
			continue
		}
		if mailboxName != "" && !strings.EqualFold(entry.Mailbox, mailboxName) {
			continue
		}
		if entry.ID == selector {
			copy := entry
			return &copy, nil
		}
	}
	matches, err := SearchRecentMessages(selector, accountName, mailboxName, 1, "")
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("no recent message matched %q", selector)
	}
	match := matches[0]
	for _, entry := range recent {
		if entry.Account == match.Account && entry.ID == match.ID {
			copy := entry
			return &copy, nil
		}
	}
	return nil, fmt.Errorf("no recent message matched %q", selector)
}

func recentMessageSince(entry RecentMessage, sinceUnix int64) bool {
	if entry.DateReceived == "" {
		return false
	}
	if unix, err := strconv.ParseInt(entry.DateReceived, 10, 64); err == nil {
		return unix >= sinceUnix
	}
	if t, err := time.Parse(time.RFC3339, entry.DateReceived); err == nil {
		return t.Unix() >= sinceUnix
	}
	return true
}

func recentMessageMatchesTerms(entry RecentMessage, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		entry.ID,
		entry.Account,
		entry.Mailbox,
		entry.Subject,
		entry.Sender,
		strings.Join(entry.SearchTerms, " "),
	}, " "))
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func normalizeRecentTerms(terms []string) []string {
	seen := make(map[string]bool, len(terms))
	normalized := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(strings.ToLower(term))
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		normalized = append(normalized, term)
	}
	return normalized
}

func mergeRecentTerms(existing, next []string) []string {
	merged := append(normalizeRecentTerms(existing), normalizeRecentTerms(next)...)
	return normalizeRecentTerms(merged)
}

func recentEntryToMessage(entry RecentMessage) Message {
	return Message{
		ID:           entry.ID,
		Subject:      entry.Subject,
		Sender:       entry.Sender,
		DateSent:     entry.DateSent,
		DateReceived: entry.DateReceived,
		Read:         entry.Read,
		Flagged:      entry.Flagged,
		Deleted:      entry.Deleted,
		MessageSize:  entry.MessageSize,
		Mailbox:      entry.Mailbox,
		Account:      entry.Account,
	}
}
