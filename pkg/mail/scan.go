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
	Scopes         []SearchMailbox
	Query          string
	Since          string
	Limit          int // candidates per mailbox and page; zero means unlimited
	Unread         bool
	Flagged        bool
	Recipient      string
	WithRecipients bool
	// Cursor is a NextCursor from an earlier page of the same request.
	Cursor            string
	Checkpoint        string // durable private traversal state; incompatible with Cursor
	RestartCheckpoint bool
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

// ScanCoverage reports one requested mailbox. Remaining counts candidate
// messages still pending traversal; Exhausted means the traversal reached the
// end of the mailbox.
type ScanCoverage struct {
	SearchMailbox
	Count               int      `json:"count"`
	Inspected           int      `json:"inspected,omitempty"`
	RecipientUnknown    int      `json:"recipientUnknown,omitempty"`
	UnknownRecipientIDs []string `json:"unknownRecipientIDs,omitempty"`
	Truncated           bool     `json:"truncated"`
	Exhausted           bool     `json:"exhausted"`
	Remaining           int      `json:"remaining"`
	Error               string   `json:"error,omitempty"`
	Errors              []string `json:"errors,omitempty"` // checkpoint-only cumulative failed-page observations
}

// RecipientCoverage describes fields inspected on returned messages, separately
// from mailbox traversal completeness and pagination.
type RecipientCoverage struct {
	Inspected   int  `json:"inspected"`
	Unavailable int  `json:"unavailable"`
	Complete    bool `json:"complete"`
}

func updateScanRecipientCoverage(result *ScanResult, requested bool) {
	if !requested {
		return
	}
	coverage := &RecipientCoverage{Inspected: len(result.Messages), Complete: true}
	for _, message := range result.Messages {
		if !message.RecipientStatus.complete() {
			coverage.Unavailable++
			coverage.Complete = false
		}
	}
	result.RecipientCoverage = coverage
}

type ScanResult struct {
	RecipientCoverage *RecipientCoverage `json:"recipientCoverage,omitempty"`
	Messages          []ScanMessage      `json:"messages"`
	Coverage          []ScanCoverage     `json:"coverage"`
	Complete          bool               `json:"complete"`
	// NextCursor continues every mailbox that is not yet exhausted. It is
	// empty once the traversal has covered every requested mailbox.
	NextCursor string                    `json:"nextCursor,omitempty"`
	StartedAt  string                    `json:"startedAt"`
	EndedAt    string                    `json:"endedAt"`
	Checkpoint *ScanCheckpointProvenance `json:"checkpoint,omitempty"`
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
	req, _ = normalizeScanRequest(req)
	data, _ := json.Marshal(struct {
		Scopes         []SearchMailbox
		Query          []string
		Since          string
		Unread         bool
		WithRecipients bool   `json:",omitempty"`
		Recipient      string `json:",omitempty"`
		Flagged        bool   `json:",omitempty"`
	}{scopes, searchTerms(req.Query), strings.TrimSpace(req.Since), req.Unread, req.WithRecipients, req.Recipient, req.Flagged})
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

func normalizeScanRequest(req ScanRequest) (ScanRequest, error) {
	recipient, err := NormalizeRecipientFilter(req.Recipient)
	if err != nil {
		return req, err
	}
	req.Recipient = recipient
	if recipient != "" {
		req.WithRecipients = true
	}
	return req, nil
}

func ValidateScanRequest(req ScanRequest) error {
	if _, err := normalizeScanRequest(req); err != nil {
		return err
	}
	if len(req.Scopes) == 0 || req.Limit < 0 || req.Limit == int(^uint(0)>>1) {
		return fmt.Errorf("scan needs explicit mailboxes and a non-negative limit")
	}
	if _, _, err := parseSinceUnix(req.Since); err != nil {
		return err
	}
	if strings.TrimSpace(req.Query) != "" && len(searchTerms(req.Query)) == 0 {
		return fmt.Errorf("query must contain a searchable term")
	}
	for _, scope := range req.Scopes {
		if strings.TrimSpace(scope.Account) == "" || strings.TrimSpace(scope.Mailbox) == "" {
			return fmt.Errorf("scan scopes need both account and mailbox")
		}
	}
	return validateCheckpointOptions(req)
}

func (c *Client) ScanMessages(req ScanRequest) (ScanResult, error) {
	var err error
	req, err = normalizeScanRequest(req)
	if err != nil {
		return ScanResult{}, err
	}
	if err := ValidateScanRequest(req); err != nil {
		return ScanResult{}, err
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
		page, err := c.scanMessagesFromIndex(scope.Account, mbox, req, limit, after)
		if err == nil && req.WithRecipients {
			page.Messages = c.enrichMessagesWithRecipients(scope.Account, scope.Mailbox, page.Messages, req.Recipient)
		}
		return page, err
	})
}

func scanMessages(req ScanRequest, read func(SearchMailbox, int, *ScanPosition) (scanPage, error)) (ScanResult, error) {
	var err error
	req, err = normalizeScanRequest(req)
	if err != nil {
		return ScanResult{}, err
	}
	if err := ValidateScanRequest(req); err != nil {
		return ScanResult{}, err
	}
	if req.Checkpoint != "" {
		return scanCheckpointMessages(req, read)
	}
	return scanPageMessages(req, read)
}

func scanPageMessages(req ScanRequest, read func(SearchMailbox, int, *ScanPosition) (scanPage, error)) (ScanResult, error) {
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
			candidateCount := len(page.Messages)
			if req.Recipient != "" {
				coverage.Inspected = candidateCount
				matches := make([]Message, 0, candidateCount)
				for _, message := range page.Messages {
					matched, known := MatchRecipient(message, req.Recipient)
					if matched {
						matches = append(matches, message)
					}
					if !known {
						coverage.RecipientUnknown++
						coverage.UnknownRecipientIDs = append(coverage.UnknownRecipientIDs, message.ID)
					}
				}
				page.Messages = matches
			}
			coverage.Count = len(page.Messages)
			coverage.Remaining = page.Remaining
			coverage.Exhausted = page.Remaining == 0
			coverage.Truncated = !coverage.Exhausted
			if coverage.RecipientUnknown > 0 {
				// Keep the original boundary: every candidate on this page is
				// pending until recipient evidence can establish membership.
				coverage.Error = fmt.Sprintf("recipient match unavailable for %d candidate messages; retry this page", coverage.RecipientUnknown)
				coverage.Remaining += candidateCount
				coverage.Exhausted, coverage.Truncated = false, false
				result.Complete, unfinished = false, true
			} else if coverage.Exhausted {
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
					if req.WithRecipients {
						mergeRecipientEvidence(&result.Messages[index].Message, message)
					}
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
	updateScanRecipientCoverage(&result, req.WithRecipients)
	return result, nil
}
