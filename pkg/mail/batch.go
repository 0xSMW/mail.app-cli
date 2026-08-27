package mail

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// BatchItem is one message inside a mutation receipt. The same shape is used
// whether one ID or five hundred were requested.
type BatchItem struct {
	ID            string `json:"id"`
	Account       string `json:"account"`
	SourceMailbox string `json:"sourceMailbox"`
	TargetMailbox string `json:"targetMailbox,omitempty"`
	Subject       string `json:"subject,omitempty"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
	MarkedRead    bool   `json:"markedRead,omitempty"`
	VerifyStatus  string `json:"verifyStatus,omitempty"`
	VerifyError   string `json:"verifyError,omitempty"`
}

// BatchResult is the mutation receipt.
type BatchResult struct {
	Action    string      `json:"action"`
	DryRun    bool        `json:"dryRun"`
	StartedAt string      `json:"startedAt,omitempty"`
	EndedAt   string      `json:"endedAt,omitempty"`
	Matched   int         `json:"matched"`
	Attempted int         `json:"attempted"`
	Succeeded int         `json:"succeeded"`
	Failed    int         `json:"failed"`
	Skipped   int         `json:"skipped"`
	Chunks    int         `json:"chunks,omitempty"`
	Items     []BatchItem `json:"items"`
}

// BatchOptions controls one mutation run.
type BatchOptions struct {
	Action         string
	TargetMailbox  string
	DryRun         bool
	Verify         bool
	MarkReadBefore bool
	ChunkSize      int
	Read           bool
	Flagged        bool
	// Journal records each message in the recent-message journal. It costs
	// index queries per message, so bulk selections leave it off.
	Journal bool
	// Progress receives per-chunk and per-message lines when non-nil.
	Progress io.Writer
	// TrustSource keeps each item's SourceMailbox as given. Otherwise an
	// archive looks each message up in the Envelope Index and acts from
	// INBOX or the backing mailbox, never from a user label, and skips
	// messages that are already archived.
	TrustSource bool
}

// BatchFailedError reports how many items failed.
type BatchFailedError struct {
	Action    string
	Failed    int
	Attempted int
}

func (e *BatchFailedError) Error() string {
	return fmt.Sprintf("%s failed for %d of %d message(s)", e.Action, e.Failed, e.Attempted)
}

// Mutator applies one action to one message and may update the item, for
// example to record the mailbox an archive landed in.
type Mutator func(*Client, *BatchItem) error

// ArchiveMutator archives through Mail.app and records the destination.
func ArchiveMutator(journal bool) Mutator {
	return func(client *Client, item *BatchItem) error {
		if journal {
			_ = client.RecordRecentEnvelope(item.Account, item.SourceMailbox, item.ID, "archive")
		}
		destination, err := client.ArchiveMessageWithDestination(item.Account, item.SourceMailbox, item.ID)
		if err != nil {
			return err
		}
		item.TargetMailbox = destination
		if journal {
			_ = UpdateRecentMessageLocation(item.Account, item.ID, destination, "archive")
		}
		return nil
	}
}

// DeleteMutator moves a message to the trash.
func DeleteMutator(client *Client, item *BatchItem) error {
	return client.DeleteMessageResolved(item.Account, item.SourceMailbox, item.ID)
}

// MoveMutator moves a message to item.TargetMailbox.
func MoveMutator(journal bool) Mutator {
	return func(client *Client, item *BatchItem) error {
		if journal {
			_ = client.RecordRecentEnvelope(item.Account, item.SourceMailbox, item.ID, "move")
		}
		if err := client.MoveMessage(item.Account, item.SourceMailbox, item.ID, item.TargetMailbox); err != nil {
			return err
		}
		if journal {
			_ = UpdateRecentMessageLocation(item.Account, item.ID, item.TargetMailbox, "move")
		}
		return nil
	}
}

// MarkMutator sets read status.
func MarkMutator(read bool) Mutator {
	return func(client *Client, item *BatchItem) error {
		return client.MarkMessageAsRead(item.Account, item.SourceMailbox, item.ID, read)
	}
}

// FlagMutator sets flagged status.
func FlagMutator(flagged bool) Mutator {
	return func(client *Client, item *BatchItem) error {
		return client.FlagMessage(item.Account, item.SourceMailbox, item.ID, flagged)
	}
}

// RunBatch applies mutate to every item and returns the receipt. The error
// is a *BatchFailedError when at least one item failed; the receipt is
// always complete. Cancelling the client's context stops the run between
// items.
func RunBatch(client *Client, opts BatchOptions, items []BatchItem, mutate Mutator) (BatchResult, error) {
	result := BatchResult{
		Action:    opts.Action,
		DryRun:    opts.DryRun,
		StartedAt: time.Now().Format(time.RFC3339),
		Matched:   len(items),
		Items:     make([]BatchItem, 0, len(items)),
	}
	for i := range items {
		if opts.TargetMailbox != "" {
			items[i].TargetMailbox = opts.TargetMailbox
		}
	}
	chunkSize := normalizedChunkSize(len(items), opts.ChunkSize)
	result.Chunks = (len(items) + chunkSize - 1) / chunkSize
	if opts.Action == "archive" && !opts.TrustSource {
		items = client.archiveSources(items)
	}
	if opts.DryRun {
		// A preview shows the same sources and skips the real run would use.
		for _, item := range items {
			if item.Status == "failed" {
				result.Failed++
				result.Items = append(result.Items, item)
				continue
			}
			item.Status = "dry-run"
			if skip := skipReason(client, opts, item); skip != "" {
				item.Status = "skipped"
				item.Error = skip
			}
			result.Skipped++
			result.Items = append(result.Items, item)
		}
		result.EndedAt = time.Now().Format(time.RFC3339)
		if result.Failed > 0 {
			return result, &BatchFailedError{Action: opts.Action, Failed: result.Failed, Attempted: len(items)}
		}
		return result, nil
	}

	for start := 0; start < len(items); start += chunkSize {
		end := min(start+chunkSize, len(items))
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "%s: chunk %d/%d (%d messages)\n", opts.Action, (start/chunkSize)+1, result.Chunks, end-start)
		}
		for _, item := range items[start:end] {
			if item.Status == "failed" {
				// Source resolution already refused this one.
				result.Attempted++
				result.Failed++
				result.Items = append(result.Items, item)
				continue
			}
			if err := client.Done(); err != nil {
				// A cancelled run is a failure for everything it did not reach.
				item.Status = "failed"
				item.Error = "cancelled: " + err.Error()
				result.Attempted++
				result.Failed++
				result.Items = append(result.Items, item)
				continue
			}
			if skip := skipReason(client, opts, item); skip != "" {
				item.Status = "skipped"
				item.Error = skip
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			result.Attempted++
			if opts.MarkReadBefore && opts.Action != "mark" {
				if err := client.MarkMessageAsRead(item.Account, item.SourceMailbox, item.ID, true); err != nil {
					item.Status = "failed"
					item.Error = fmt.Sprintf("mark-read before %s failed: %v", opts.Action, err)
					result.Failed++
					result.Items = append(result.Items, item)
					continue
				}
				item.MarkedRead = true
			}
			if err := mutate(client, &item); err != nil {
				item.Status = "failed"
				item.Error = err.Error()
				result.Failed++
			} else {
				item.Status = "succeeded"
				if opts.Verify {
					verifyStatus, verifyErr := VerifyMutation(client, opts, item)
					item.VerifyStatus = verifyStatus
					if verifyErr != nil {
						item.Status = "failed"
						item.VerifyError = verifyErr.Error()
						result.Failed++
					} else {
						result.Succeeded++
					}
				} else {
					result.Succeeded++
				}
			}
			if opts.Progress != nil {
				fmt.Fprintf(opts.Progress, "%s: %d/%d %s %s\n", opts.Action, result.Attempted, len(items), item.ID, item.Status)
			}
			result.Items = append(result.Items, item)
		}
	}
	result.EndedAt = time.Now().Format(time.RFC3339)
	if result.Failed > 0 {
		return result, &BatchFailedError{Action: opts.Action, Failed: result.Failed, Attempted: result.Attempted}
	}
	return result, nil
}

// skipReason says why an item is not attempted: the message is already
// where the action would put it.
func skipReason(_ *Client, opts BatchOptions, item BatchItem) string {
	switch opts.Action {
	case "move":
		if strings.EqualFold(item.TargetMailbox, item.SourceMailbox) {
			return "already in " + item.TargetMailbox
		}
	case "archive":
		if IsArchiveAlias(item.SourceMailbox) {
			return "already in " + item.SourceMailbox
		}
	}
	return ""
}

// archiveSources rewrites each item's source to INBOX or its backing
// mailbox so archive never strips a user label. An item listed under
// anything other than INBOX or the archive that the index cannot place is
// marked failed rather than archived from the label it was listed in.
func (c *Client) archiveSources(items []BatchItem) []BatchItem {
	needsLookup := func(item BatchItem) bool {
		return !strings.EqualFold(item.SourceMailbox, "INBOX") && !IsArchiveAlias(item.SourceMailbox)
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		if needsLookup(item) {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return items
	}
	located, err := c.LocateMessages(ids)
	for i, item := range items {
		if !needsLookup(item) {
			continue
		}
		loc, ok := located[item.ID]
		switch {
		case err != nil:
			items[i].Status = "failed"
			items[i].Error = fmt.Sprintf("cannot resolve a safe archive source without the Envelope Index (%v); archive from INBOX with --mailbox", err)
		case !ok || !strings.EqualFold(loc.Account, item.Account):
			items[i].Status = "failed"
			items[i].Error = "message not in the Envelope Index; archive from INBOX with --mailbox"
		default:
			items[i].SourceMailbox = loc.ArchiveMailbox
		}
	}
	return items
}

// Summary is the one-line human description of a receipt.
func (r BatchResult) Summary(opts BatchOptions) string {
	verb := map[string]string{"archive": "Archived", "delete": "Deleted", "move": "Moved", "mark": "Marked", "flag": "Flagged"}[r.Action]
	switch r.Action {
	case "mark":
		verb = "Marked read"
		if !opts.Read {
			verb = "Marked unread"
		}
	case "flag":
		if !opts.Flagged {
			verb = "Unflagged"
		}
	}
	count := countNoun(r.Matched, "message")
	target := ""
	if r.Action == "move" && opts.TargetMailbox != "" {
		target = " to " + opts.TargetMailbox
	}
	if r.Action == "archive" {
		destinations := map[string]bool{}
		for _, item := range r.Items {
			if item.TargetMailbox != "" && item.Status == "succeeded" {
				destinations[item.TargetMailbox] = true
			}
		}
		if len(destinations) == 1 {
			for name := range destinations {
				target = " to " + name
			}
		}
	}
	if r.DryRun {
		previewed, noops := 0, 0
		for _, item := range r.Items {
			switch item.Status {
			case "dry-run":
				previewed++
			case "skipped":
				noops++
			}
		}
		summary := fmt.Sprintf("Dry run: would have %s %s%s", strings.ToLower(verb), countNoun(previewed, "message"), target)
		if noops > 0 {
			summary += fmt.Sprintf("; %d already there", noops)
		}
		if r.Failed > 0 {
			summary += fmt.Sprintf("; %d cannot be %s", r.Failed, strings.ToLower(verb))
		}
		return summary
	}
	if r.Failed == 0 && r.Skipped == 0 {
		return fmt.Sprintf("%s %s%s", verb, count, target)
	}
	summary := fmt.Sprintf("%s %d of %s%s", verb, r.Succeeded, count, target)
	if r.Failed > 0 {
		summary += fmt.Sprintf("; %d failed", r.Failed)
	}
	if r.Skipped > 0 {
		summary += fmt.Sprintf("; %d already there", r.Skipped)
	}
	return summary
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// VerifyMutation re-reads a message after a mutation and reports whether
// Mail.app shows the requested end state.
func VerifyMutation(client *Client, opts BatchOptions, item BatchItem) (string, error) {
	present := func(mailbox string) (bool, error) {
		message, err := client.GetMessageDetailsForVerificationJSON(item.Account, mailbox, item.ID)
		if err != nil {
			return false, err
		}
		return message != nil, nil
	}
	switch opts.Action {
	case "archive", "move":
		// Mail.app may keep the ID (Gmail label changes) or assign a new one
		// (real moves), and Gmail keeps every message in All Mail. So: a
		// no-op is fine; archiving into All Mail is proven by absence from
		// the source; moving out of All Mail is proven by presence in the
		// destination; anything else accepts either proof.
		if item.TargetMailbox == "" || strings.EqualFold(item.TargetMailbox, item.SourceMailbox) {
			return "already-in-destination", nil
		}
		if IsArchiveAlias(item.TargetMailbox) {
			inSource, err := present(item.SourceMailbox)
			if err != nil {
				return "verification-failed", err
			}
			if inSource {
				return "present-in-source", fmt.Errorf("message still present in %s", item.SourceMailbox)
			}
			return "absent-from-source", nil
		}
		inDestination, err := present(item.TargetMailbox)
		if err != nil {
			return "verification-failed", err
		}
		if inDestination {
			return "present-in-destination", nil
		}
		if IsArchiveAlias(item.SourceMailbox) {
			return "destination-unverified", fmt.Errorf("message not found in %s by its old ID; Mail.app may have renumbered it", item.TargetMailbox)
		}
		inSource, err := present(item.SourceMailbox)
		if err != nil {
			return "verification-failed", err
		}
		if !inSource {
			return "absent-from-source", nil
		}
		return "present-in-source", fmt.Errorf("message still present in %s", item.SourceMailbox)
	case "delete":
		inSource, err := present(item.SourceMailbox)
		if err != nil {
			return "verification-failed", err
		}
		if inSource {
			return "present-in-source", fmt.Errorf("message still present in %s", item.SourceMailbox)
		}
		return "absent-from-source", nil
	}
	message, err := client.GetMessageDetailsForVerificationJSON(item.Account, item.SourceMailbox, item.ID)
	if err != nil {
		return "verification-failed", err
	}
	if message == nil {
		return "verification-failed", fmt.Errorf("message not found after mutation")
	}
	if opts.Action == "mark" {
		if message.Read == opts.Read {
			return "matched", nil
		}
		return "mismatch", fmt.Errorf("read status mismatch")
	}
	if opts.Action == "flag" {
		if message.Flagged == opts.Flagged {
			return "matched", nil
		}
		return "mismatch", fmt.Errorf("flagged status mismatch")
	}
	return "unchecked", nil
}

func normalizedChunkSize(total, requested int) int {
	if total <= 0 {
		return 1
	}
	if requested <= 0 || requested > total {
		return total
	}
	return requested
}
