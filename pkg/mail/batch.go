package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
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
}

// ErrBatchFailed wraps a run in which at least one item failed.
var ErrBatchFailed = errors.New("batch failed")

// BatchFailedError reports how many items failed.
type BatchFailedError struct {
	Action    string
	Failed    int
	Attempted int
}

func (e *BatchFailedError) Error() string {
	return fmt.Sprintf("%s failed for %d of %d message(s)", e.Action, e.Failed, e.Attempted)
}

func (e *BatchFailedError) Unwrap() error { return ErrBatchFailed }

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
// always complete. Cancelling ctx stops the run between items.
func RunBatch(ctx context.Context, client *Client, opts BatchOptions, items []BatchItem, mutate Mutator) (BatchResult, error) {
	client = client.WithContext(ctx)
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
	chunkSize := NormalizedChunkSize(len(items), opts.ChunkSize)
	result.Chunks = (len(items) + chunkSize - 1) / chunkSize
	if opts.DryRun {
		for _, item := range items {
			item.Status = "dry-run"
			result.Skipped++
			result.Items = append(result.Items, item)
		}
		result.EndedAt = time.Now().Format(time.RFC3339)
		return result, nil
	}

	for start := 0; start < len(items); start += chunkSize {
		end := min(start+chunkSize, len(items))
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "%s: chunk %d/%d (%d messages)\n", opts.Action, (start/chunkSize)+1, result.Chunks, end-start)
		}
		for _, item := range items[start:end] {
			if err := ctx.Err(); err != nil {
				item.Status = "skipped"
				item.Error = "cancelled"
				result.Skipped++
				result.Items = append(result.Items, item)
				continue
			}
			if opts.Action == "move" && strings.EqualFold(item.TargetMailbox, item.SourceMailbox) {
				item.Status = "skipped"
				item.Error = "already in " + item.TargetMailbox
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

// NormalizedChunkSize clamps a requested chunk size to the item count.
func NormalizedChunkSize(total, requested int) int {
	if total <= 0 {
		return 1
	}
	if requested <= 0 || requested > total {
		return total
	}
	return requested
}
