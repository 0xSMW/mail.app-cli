package mail

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	Limit  int // per mailbox and page; zero means unlimited
	Unread bool
	// Cursor is a NextCursor from an earlier page of the same request.
	Cursor string
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

// ScanCoverage reports one requested mailbox. Remaining counts the matching
// messages older than this page; Exhausted means the traversal reached the
// end of the mailbox.
type ScanCoverage struct {
	SearchMailbox
	Count     int    `json:"count"`
	Truncated bool   `json:"truncated"`
	Exhausted bool   `json:"exhausted"`
	Remaining int    `json:"remaining"`
	Error     string `json:"error,omitempty"`
}

type ScanResult struct {
	Messages []ScanMessage  `json:"messages"`
	Coverage []ScanCoverage `json:"coverage"`
	Complete bool           `json:"complete"`
	// NextCursor continues every mailbox that is not yet exhausted. It is
	// empty once the traversal has covered every requested mailbox.
	NextCursor string `json:"nextCursor,omitempty"`
	StartedAt  string `json:"startedAt"`
	EndedAt    string `json:"endedAt"`
}

// ScanPosition is the last message a traversal returned from one mailbox.
type ScanPosition struct {
	ReceivedUnix int64 `json:"receivedUnix"`
	ID           int64 `json:"id"`
}

type scanPage struct {
	Messages  []Message
	Remaining int
	Last      *ScanPosition
}

type scanCursorScope struct {
	Account string        `json:"account"`
	Mailbox string        `json:"mailbox"`
	Done    bool          `json:"done,omitempty"`
	After   *ScanPosition `json:"after,omitempty"`
}

type scanCursor struct {
	Version int               `json:"v"`
	Request string            `json:"request"`
	Scopes  []scanCursorScope `json:"scopes"`
}

// ErrInvalidScanCursor reports a cursor that is unreadable or that belongs
// to a different scan request.
var ErrInvalidScanCursor = errors.New("invalid scan cursor")

const scanCursorVersion = 1

func uniqueScanScopes(scopes []SearchMailbox) []SearchMailbox {
	seen := map[SearchMailbox]bool{}
	unique := make([]SearchMailbox, 0, len(scopes))
	for _, scope := range scopes {
		if !seen[scope] {
			seen[scope] = true
			unique = append(unique, scope)
		}
	}
	return unique
}

// scanRequestFingerprint binds a cursor to the filters it was issued for.
// The page size is left out so a caller may change --limit between pages.
func scanRequestFingerprint(req ScanRequest, scopes []SearchMailbox) string {
	data, _ := json.Marshal(struct {
		Scopes []SearchMailbox
		Query  []string
		Since  string
		Unread bool
	}{scopes, searchTerms(req.Query), strings.TrimSpace(req.Since), req.Unread})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

func encodeScanCursor(cursor scanCursor) string {
	data, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(data)
}

func decodeScanCursor(value string, req ScanRequest, scopes []SearchMailbox) (scanCursor, error) {
	fresh := scanCursor{Version: scanCursorVersion, Request: scanRequestFingerprint(req, scopes), Scopes: make([]scanCursorScope, len(scopes))}
	for i, scope := range scopes {
		fresh.Scopes[i] = scanCursorScope{Account: scope.Account, Mailbox: scope.Mailbox}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return fresh, nil
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return scanCursor{}, fmt.Errorf("%w: not a cursor returned by scan", ErrInvalidScanCursor)
	}
	var cursor scanCursor
	if err := json.Unmarshal(data, &cursor); err != nil || cursor.Version != scanCursorVersion {
		return scanCursor{}, fmt.Errorf("%w: not a cursor returned by this version of scan", ErrInvalidScanCursor)
	}
	if cursor.Request != fresh.Request || len(cursor.Scopes) != len(scopes) {
		return scanCursor{}, fmt.Errorf("%w: it was issued for different mailboxes or filters; repeat the original scan arguments", ErrInvalidScanCursor)
	}
	for i, scope := range scopes {
		if cursor.Scopes[i].Account != scope.Account || cursor.Scopes[i].Mailbox != scope.Mailbox {
			return scanCursor{}, fmt.Errorf("%w: it was issued for different mailboxes or filters; repeat the original scan arguments", ErrInvalidScanCursor)
		}
	}
	return cursor, nil
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
	return scanMessages(req, func(scope SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
		if err := c.Done(); err != nil {
			return scanPage{}, err
		}
		// Legacy JXA list/search fallbacks can skip inaccessible messages or
		// cap the mailbox enumeration. They cannot prove complete coverage.
		mbox, ok, err := c.resolveIndexMailbox(scope.Account, scope.Mailbox)
		if err != nil {
			return scanPage{}, err
		}
		if !ok {
			return scanPage{}, fmt.Errorf("mailbox unavailable in Envelope Index: %s/%s; check mailbox name and Full Disk Access", scope.Account, scope.Mailbox)
		}
		return c.scanMessagesFromIndex(scope.Account, mbox, req, limit, after)
	})
}

func scanMessages(req ScanRequest, read func(SearchMailbox, int, *ScanPosition) (scanPage, error)) (ScanResult, error) {
	scopes := uniqueScanScopes(req.Scopes)
	cursor, err := decodeScanCursor(req.Cursor, req, scopes)
	if err != nil {
		return ScanResult{}, err
	}
	result := ScanResult{Messages: []ScanMessage{}, Coverage: []ScanCoverage{}, Complete: true, StartedAt: time.Now().Format(time.RFC3339Nano)}
	seenMessages := map[string]int{}
	unfinished := false
	for i, scope := range scopes {
		state := &cursor.Scopes[i]
		coverage := ScanCoverage{SearchMailbox: scope}
		if state.Done {
			// An earlier page already reached the end of this mailbox.
			coverage.Exhausted = true
			result.Coverage = append(result.Coverage, coverage)
			continue
		}
		page, err := read(scope, req.Limit, state.After)
		if err != nil {
			// The position is kept, so the next page retries this mailbox.
			coverage.Error = err.Error()
			result.Complete = false
			unfinished = true
		} else {
			coverage.Count = len(page.Messages)
			coverage.Remaining = page.Remaining
			coverage.Exhausted = page.Remaining == 0
			coverage.Truncated = !coverage.Exhausted
			if coverage.Exhausted {
				state.Done, state.After = true, nil
			} else {
				state.After = page.Last
				result.Complete = false
				unfinished = true
			}
			for _, message := range page.Messages {
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
	if unfinished {
		result.NextCursor = encodeScanCursor(cursor)
	}
	sort.SliceStable(result.Messages, func(i, j int) bool { return result.Messages[i].DateReceived > result.Messages[j].DateReceived })
	result.EndedAt = time.Now().Format(time.RFC3339Nano)
	return result, nil
}
