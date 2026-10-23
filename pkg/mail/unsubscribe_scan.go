package mail

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// ScanUnsubscribe discovers controls from every available local MIME body and
// header in the requested traversal. It never requests a rendered Mail body.
func (c *Client) ScanUnsubscribe(req UnsubscribeScanRequest) (UnsubscribeScanResult, error) {
	return scanUnsubscribe(c.Context(), req, func(ctx context.Context, request ScanRequest) (ScanResult, error) {
		return c.WithContext(ctx).ScanMessages(request)
	}, func(ctx context.Context, messages []ScanMessage) ([]UnsubscribeObservation, error) {
		return c.WithContext(ctx).ReadLocalUnsubscribeMessages(messages)
	})
}

func ValidateUnsubscribeScanRequest(req UnsubscribeScanRequest) error {
	if req.PageSize < 1 || req.PageSize > 5000 || req.MaxPages < 0 || req.Budget <= 0 {
		return fmt.Errorf("page size must be 1–5000, max pages non-negative, and budget positive")
	}
	if (req.Restart || req.RetryIncomplete) && req.Checkpoint == "" {
		return fmt.Errorf("--restart-checkpoint and --retry-incomplete require --checkpoint")
	}
	if req.Restart && req.RetryIncomplete {
		return fmt.Errorf("--restart-checkpoint and --retry-incomplete cannot be combined")
	}
	return ValidateScanRequest(ScanRequest{Scopes: req.Scopes, Since: req.Since, Limit: req.PageSize})
}

func unsubscribeMessageKey(account, id string) string { return account + "\x00" + id }

func scanUnsubscribe(parent context.Context, req UnsubscribeScanRequest,
	readPage func(context.Context, ScanRequest) (ScanResult, error),
	readLocal func(context.Context, []ScanMessage) ([]UnsubscribeObservation, error),
) (UnsubscribeScanResult, error) {
	req.Scopes = uniqueScanScopes(req.Scopes)
	if err := ValidateUnsubscribeScanRequest(req); err != nil {
		return UnsubscribeScanResult{}, err
	}
	ctx, cancel := context.WithTimeout(parent, req.Budget)
	defer cancel()
	path, err := unsubscribeCheckpointPath(req.Checkpoint)
	if err != nil {
		return UnsubscribeScanResult{}, err
	}
	req.Checkpoint = path
	release, err := lockUnsubscribeCheckpoint(path)
	if err != nil {
		return UnsubscribeScanResult{}, err
	}
	defer release()
	state, resumed, err := loadUnsubscribeCheckpoint(path, req)
	if err != nil {
		return UnsubscribeScanResult{}, err
	}
	if req.RetryIncomplete && !resumed {
		return UnsubscribeScanResult{}, unsubscribeCheckpointError("no existing discovery to retry")
	}
	if req.Restart || !resumed {
		state = newUnsubscribeCheckpoint(req)
		resumed = false
	}
	if req.RetryIncomplete {
		for _, observation := range state.Observations {
			if !observation.Complete {
				state.Pending = append(state.Pending, scanMessageFromObservation(observation))
			}
		}
		state.Pending = uniqueUnsubscribePending(state.Pending)
	} else if resumed && state.TraversalComplete && len(state.Pending) == 0 {
		return unsubscribeScanResult(state, path, true), nil
	}
	if err := saveUnsubscribeCheckpoint(path, &state); err != nil {
		return UnsubscribeScanResult{}, err
	}
	pages := 0
	positions := make(map[string]int, len(state.Observations))
	for i, o := range state.Observations {
		positions[unsubscribeMessageKey(o.Account, o.ID)] = i
	}
	for ctx.Err() == nil {
		if len(state.Pending) > 0 {
			// Persist the enumerated page before extraction. Cancellation can
			// therefore never advance the traversal past uninspected messages.
			n := min(200, len(state.Pending))
			batch := append([]ScanMessage(nil), state.Pending[:n]...)
			observations, readErr := readLocal(ctx, batch)
			processed := make(map[string]bool)
			allowed := make(map[string]bool)
			for _, m := range batch {
				allowed[unsubscribeMessageKey(m.Account, m.ID)] = true
			}
			for _, observation := range observations {
				key := unsubscribeMessageKey(observation.Account, observation.ID)
				if !allowed[key] || processed[key] {
					return UnsubscribeScanResult{}, fmt.Errorf("local discovery returned an unexpected or repeated message")
				}
				processed[key] = true
				if i, ok := positions[key]; ok {
					previous := state.Observations[i]
					observation.Mailboxes = unionUnsubscribeStrings(previous.Mailboxes, observation.Mailboxes)
					if !observation.Complete {
						observation.Destinations = mergeUnsubscribeDestinations(previous.Destinations, observation.Destinations)
						observation.HasHTTPSUnsubscribe = observation.HasHTTPSUnsubscribe || previous.HasHTTPSUnsubscribe
						observation.OneClickAdvertised = observation.OneClickAdvertised || previous.OneClickAdvertised
						if observation.ListID == "" {
							observation.ListID = previous.ListID
						}
						if observation.RFCMessageID == "" {
							observation.RFCMessageID = previous.RFCMessageID
						}
						if len(previous.Destinations) > 0 {
							observation.Warnings = unionUnsubscribeStrings(observation.Warnings, []string{"Previously recovered controls are retained from an earlier local observation; current extraction is incomplete."})
						}
					}
					state.Observations[i] = observation
				} else {
					positions[key] = len(state.Observations)
					state.Observations = append(state.Observations, observation)
				}
			}
			pending := state.Pending[:0]
			for _, m := range state.Pending {
				if !processed[unsubscribeMessageKey(m.Account, m.ID)] {
					pending = append(pending, m)
				}
			}
			state.Pending = pending
			state.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if err := saveUnsubscribeCheckpoint(path, &state); err != nil {
				return UnsubscribeScanResult{}, err
			}
			if readErr != nil {
				if errors.Is(readErr, context.Canceled) || errors.Is(readErr, context.DeadlineExceeded) {
					break
				}
				return UnsubscribeScanResult{}, fmt.Errorf("local unsubscribe discovery: %w (checkpoint: %s)", readErr, path)
			}
			if len(processed) == 0 {
				return UnsubscribeScanResult{}, fmt.Errorf("local discovery made no progress (checkpoint: %s)", path)
			}
			continue
		}
		if state.TraversalComplete || req.MaxPages > 0 && pages >= req.MaxPages {
			break
		}
		page, pageErr := readPage(ctx, ScanRequest{Scopes: req.Scopes, Since: req.Since, Limit: req.PageSize, Cursor: state.Cursor})
		if pageErr != nil {
			if ctx.Err() != nil {
				break
			}
			return UnsubscribeScanResult{}, fmt.Errorf("enumerate unsubscribe scope: %w (checkpoint: %s)", pageErr, path)
		}
		if len(page.Coverage) != len(req.Scopes) || !page.Complete && page.NextCursor == "" {
			return UnsubscribeScanResult{}, fmt.Errorf("inconsistent mailbox traversal result")
		}
		state.Pages++
		pages++
		state.Cursor, state.TraversalComplete = page.NextCursor, page.Complete
		for i, coverage := range page.Coverage {
			if coverage.SearchMailbox != req.Scopes[i] {
				return UnsubscribeScanResult{}, fmt.Errorf("mailbox traversal returned unexpected scope")
			}
			coverage.Count += state.Coverage[i].Count
			state.Coverage[i] = coverage
		}
		for _, message := range page.Messages {
			known := false
			if i, ok := positions[unsubscribeMessageKey(message.Account, message.ID)]; ok {
				old := &state.Observations[i]
				old.Mailboxes = unionUnsubscribeStrings(old.Mailboxes, message.Mailboxes)
				known = true
			}
			if !known {
				state.Pending = append(state.Pending, message)
			}
		}
		state.Pending = uniqueUnsubscribePending(state.Pending)
		state.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := saveUnsubscribeCheckpoint(path, &state); err != nil {
			return UnsubscribeScanResult{}, err
		}
		// An inaccessible mailbox is retried on the next invocation, rather
		// than spinning through the same failed scope within this budget.
		failed := false
		for _, coverage := range page.Coverage {
			failed = failed || coverage.Error != ""
		}
		if failed {
			// Extract this page's readable mail before returning its exceptions.
			pages = max(pages, req.MaxPages)
			if req.MaxPages == 0 {
				req.MaxPages = pages
			}
		}
	}
	state.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err := saveUnsubscribeCheckpoint(path, &state); err != nil {
		return UnsubscribeScanResult{}, err
	}
	return unsubscribeScanResult(state, path, resumed), nil
}

func scanMessageFromObservation(o UnsubscribeObservation) ScanMessage {
	box := ""
	if len(o.Mailboxes) > 0 {
		box = o.Mailboxes[0]
	}
	return ScanMessage{Message: Message{ID: o.ID, RFCMessageID: o.RFCMessageID, Account: o.Account, Mailbox: box, Sender: o.Sender, Subject: o.Subject, DateReceived: o.DateReceived}, Mailboxes: append([]string(nil), o.Mailboxes...)}
}

func uniqueUnsubscribePending(messages []ScanMessage) []ScanMessage {
	result := []ScanMessage{}
	positions := make(map[string]int)
	for _, m := range messages {
		key := unsubscribeMessageKey(m.Account, m.ID)
		if i, ok := positions[key]; ok {
			result[i].Mailboxes = unionUnsubscribeStrings(result[i].Mailboxes, m.Mailboxes)
		} else {
			positions[key] = len(result)
			result = append(result, m)
		}
	}
	return result
}

func unionUnsubscribeStrings(a, b []string) []string {
	result := append([]string{}, a...)
	for _, value := range b {
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func unsubscribeScanResult(state unsubscribeCheckpoint, path string, resumed bool) UnsubscribeScanResult {
	result := UnsubscribeScanResult{Groups: GroupUnsubscribeObservations(state.Observations), Coverage: state.Coverage, Exceptions: []UnsubscribeObservation{}, TraversalComplete: state.TraversalComplete, Checkpoint: filepath.Clean(path), Resumed: resumed, Pages: state.Pages, Pending: len(state.Pending), StartedAt: state.StartedAt, EndedAt: state.EndedAt}
	result.Scope = "All messages observed in the requested mailboxes; sequential observations, not an atomic snapshot."
	if strings.TrimSpace(state.Since) != "" {
		result.Scope += " Received since " + state.Since + "."
	}
	identities := make(map[string]bool)
	for _, o := range state.Observations {
		identities[unsubscribeMessageKey(o.Account, o.ID)] = true
	}
	for _, m := range state.Pending {
		identities[unsubscribeMessageKey(m.Account, m.ID)] = true
	}
	result.Readability.Enumerated = len(identities)
	result.Readability.Inspected = len(state.Observations)
	for _, observation := range state.Observations {
		if observation.Complete {
			result.Readability.Complete++
		} else {
			result.Exceptions = append(result.Exceptions, observation)
			if observation.HeaderStatus == "available" {
				result.Readability.Partial++
			} else {
				result.Readability.Unavailable++
			}
		}
		if len(observation.Destinations) > 0 {
			result.Readability.WithOpportunities++
		}
	}
	result.Complete = result.TraversalComplete && result.Pending == 0 && len(result.Exceptions) == 0
	return result
}
