package mail

import (
	"slices"
	"strings"
	"unicode"
)

// ThreadSummary groups messages that share a normalized subject. Synthetic
// is true for every subject-derived group; only single-message groups are
// safe to act on as a unit.
type ThreadSummary struct {
	ID           string    `json:"id"`
	Subject      string    `json:"subject"`
	Synthetic    bool      `json:"synthetic"`
	Count        int       `json:"count"`
	UnreadCount  int       `json:"unreadCount"`
	LatestDate   string    `json:"latestDate"`
	Participants []string  `json:"participants"`
	MessageIDs   []string  `json:"messageIds"`
	Messages     []Message `json:"messages,omitempty"`
}

// GroupThreads buckets messages by normalized subject, newest thread first.
func GroupThreads(messages []Message) []ThreadSummary {
	byKey := map[string]*ThreadSummary{}
	for _, message := range messages {
		key := NormalizeThreadSubject(message.Subject)
		if key == "" {
			key = "message-" + message.ID
		}
		thread, ok := byKey[key]
		if !ok {
			thread = &ThreadSummary{
				ID:           key,
				Subject:      strings.TrimSpace(message.Subject),
				Synthetic:    !strings.HasPrefix(key, "message-"),
				Participants: []string{},
				MessageIDs:   []string{},
			}
			byKey[key] = thread
		}
		thread.Count++
		if !message.Read {
			thread.UnreadCount++
		}
		if message.DateReceived > thread.LatestDate {
			thread.LatestDate = message.DateReceived
		}
		thread.MessageIDs = append(thread.MessageIDs, message.ID)
		if message.Sender != "" && !slices.Contains(thread.Participants, message.Sender) {
			thread.Participants = append(thread.Participants, message.Sender)
		}
	}
	threads := make([]ThreadSummary, 0, len(byKey))
	for _, thread := range byKey {
		slices.Sort(thread.Participants)
		threads = append(threads, *thread)
	}
	slices.SortFunc(threads, func(a, b ThreadSummary) int { return strings.Compare(b.LatestDate, a.LatestDate) })
	return threads
}

// ThreadArchiveAllowed reports whether a thread can be archived as a unit.
func ThreadArchiveAllowed(thread ThreadSummary) bool {
	return !thread.Synthetic || thread.Count <= 1
}

// MessagesForThread picks the loaded messages that belong to the thread.
func MessagesForThread(ids []string, loaded []Message) []Message {
	idSet := make(map[string]bool, len(ids))
	for _, id := range ids {
		idSet[id] = true
	}
	var messages []Message
	for _, message := range loaded {
		if idSet[message.ID] {
			messages = append(messages, message)
		}
	}
	return messages
}

// NormalizeThreadSubject strips reply and forward prefixes and collapses
// whitespace so replies land in the same thread.
func NormalizeThreadSubject(subject string) string {
	subject = strings.TrimSpace(strings.ToLower(subject))
	for {
		trimmed := strings.TrimSpace(subject)
		for _, prefix := range []string{"re:", "fw:", "fwd:"} {
			if strings.HasPrefix(trimmed, prefix) {
				trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			}
		}
		if trimmed == subject {
			break
		}
		subject = trimmed
	}
	var b strings.Builder
	lastSpace := false
	for _, r := range subject {
		if unicode.IsSpace(r) {
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
			continue
		}
		lastSpace = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
