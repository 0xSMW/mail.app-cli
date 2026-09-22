package mail

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ScanRequest names every mailbox explicitly. A scan always reads live state;
// its observations are sequential, not an atomic snapshot of Mail.app.
type ScanRequest struct {
	Scopes []SearchMailbox
	Query  string
	Since  string
	Limit  int // per mailbox; zero means unlimited
	Unread bool
}

type ScanMessage struct {
	Message
	Mailboxes []string `json:"mailboxes"`
}

// Message has its own marshaler; explicitly merge membership so embedding it
// does not silently drop the scan-only field.
func (m ScanMessage) MarshalJSON() ([]byte, error) {
	data, err := json.Marshal(m.Message)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	fields["mailboxes"], err = json.Marshal(m.Mailboxes)
	if err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

type ScanCoverage struct {
	SearchMailbox
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated"`
	Error     string `json:"error,omitempty"`
}

type ScanResult struct {
	Messages  []ScanMessage  `json:"messages"`
	Coverage  []ScanCoverage `json:"coverage"`
	Complete  bool           `json:"complete"`
	StartedAt string         `json:"startedAt"`
	EndedAt   string         `json:"endedAt"`
}

func (c *Client) ScanMessages(req ScanRequest) (ScanResult, error) {
	if len(req.Scopes) == 0 || req.Limit < 0 || req.Limit == int(^uint(0)>>1) {
		return ScanResult{}, fmt.Errorf("scan needs explicit mailboxes and a non-negative limit")
	}
	if _, _, err := parseSinceUnix(req.Since); err != nil {
		return ScanResult{}, err
	}
	if strings.TrimSpace(req.Query) != "" && len(searchTerms(req.Query)) == 0 {
		return ScanResult{}, fmt.Errorf("query must contain a searchable term")
	}
	for _, scope := range req.Scopes {
		if strings.TrimSpace(scope.Account) == "" || strings.TrimSpace(scope.Mailbox) == "" {
			return ScanResult{}, fmt.Errorf("scan scopes need both account and mailbox")
		}
	}
	return scanMessages(req, func(scope SearchMailbox, limit int) ([]Message, error) {
		if err := c.Done(); err != nil {
			return nil, err
		}
		// Legacy JXA list/search fallbacks can skip inaccessible messages or
		// cap the mailbox enumeration. They cannot prove complete coverage.
		mbox, ok, err := c.resolveIndexMailbox(scope.Account, scope.Mailbox)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("mailbox unavailable in Envelope Index: %s/%s; check mailbox name and Full Disk Access", scope.Account, scope.Mailbox)
		}
		if strings.TrimSpace(req.Query) == "" {
			return c.getMessagesFromIndex(scope.Account, mbox, limit, 0, req.Unread, false, req.Since)
		}
		// Filter before limiting so read matches cannot crowd unread matches out.
		searchLimit := limit
		if req.Unread {
			searchLimit = 0
		}
		messages, err := c.searchMessagesFromIndex(req.Query, scope.Account, mbox, searchLimit, req.Since)
		if req.Unread {
			unread := make([]Message, 0, len(messages))
			for _, message := range messages {
				if !message.Read {
					unread = append(unread, message)
				}
			}
			messages = unread
		}
		return messages, err
	}), nil
}

func scanMessages(req ScanRequest, read func(SearchMailbox, int) ([]Message, error)) ScanResult {
	result := ScanResult{Messages: []ScanMessage{}, Coverage: []ScanCoverage{}, Complete: true, StartedAt: time.Now().Format(time.RFC3339Nano)}
	seenScopes := map[SearchMailbox]bool{}
	seenMessages := map[string]int{}
	limit := req.Limit
	if limit > 0 {
		limit++
	} // detect truncation, including exact-boundary results
	for _, scope := range req.Scopes {
		if seenScopes[scope] {
			continue
		}
		seenScopes[scope] = true
		coverage := ScanCoverage{SearchMailbox: scope}
		messages, err := read(scope, limit)
		if err != nil {
			coverage.Error = err.Error()
			result.Complete = false
		} else {
			if req.Limit > 0 && len(messages) > req.Limit {
				coverage.Truncated = true
				result.Complete = false
				messages = messages[:req.Limit]
			}
			coverage.Count = len(messages)
			for _, message := range messages {
				key := scope.Account + "\x00" + message.ID
				if index, found := seenMessages[key]; found {
					result.Messages[index].Mailboxes = append(result.Messages[index].Mailboxes, scope.Mailbox)
				} else {
					message.Account, message.Mailbox = scope.Account, scope.Mailbox
					seenMessages[key] = len(result.Messages)
					result.Messages = append(result.Messages, ScanMessage{Message: message, Mailboxes: []string{scope.Mailbox}})
				}
			}
		}
		result.Coverage = append(result.Coverage, coverage)
	}
	sort.SliceStable(result.Messages, func(i, j int) bool { return result.Messages[i].DateReceived > result.Messages[j].DateReceived })
	result.EndedAt = time.Now().Format(time.RFC3339Nano)
	return result
}
