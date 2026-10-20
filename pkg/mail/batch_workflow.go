package mail

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type batchRunner struct {
	client              *Client
	opts                BatchOptions
	result              *BatchResult
	items               []BatchItem
	chunkSize           int
	autoReceipt         bool
	archiveDestinations map[string]string
	nativeArchiveReady  bool
}

func (r *batchRunner) finish(retErr *error) {
	client, opts, result := r.client, r.opts, r.result

	if (opts.Verify || opts.MarkReadOnSkip) && *retErr != nil {
		for i := range result.Items {
			item := &result.Items[i]
			if (item.Status != "succeeded" && !(item.MarkReadOnSkip && item.MarkedRead)) || item.VerifyStatus != "" {
				continue
			}
			item.VerifyStatus = "applied_destination_unverified"
			if isUnknownMutationError(client, *retErr) {
				item.VerifyStatus = "unknown_after_timeout"
			}
			item.VerifyError = "verification not completed: " + (*retErr).Error()
			if err := r.recordVerification(*item); err != nil {
				result.JournalError = err.Error()
				*retErr = errors.Join(*retErr, err)
			}
		}
	}
	result.EndedAt = time.Now().Format(time.RFC3339)
	terminal := "completed"
	if client.Done() != nil || isUnknownMutationError(client, *retErr) || hasUnknownItem(result.Items) {
		terminal = "interrupted"
	}
	if err := opts.Receipt.Record(terminal, map[string]any{"result": result}); err != nil {
		result.JournalError = err.Error()
		*retErr = errors.Join(*retErr, err)
	}
	if r.autoReceipt {
		if err := opts.Receipt.Close(); err != nil {
			result.JournalError = err.Error()
			*retErr = errors.Join(*retErr, err)
		}
	}
}

func (r *batchRunner) prepare() error {
	client, opts, result, items := r.client, r.opts, r.result, r.items
	for i := range items {
		if opts.TargetMailbox != "" {
			items[i].TargetMailbox = opts.TargetMailbox
		}
	}
	r.chunkSize = normalizedChunkSize(len(items), opts.ChunkSize)
	if opts.ReuseBridge && opts.Action != "archive" {
		r.chunkSize = min(r.chunkSize, 25)
	}
	result.Chunks = (len(items) + r.chunkSize - 1) / r.chunkSize
	if opts.Action == "archive" && !opts.TrustSource {
		items = client.archiveSources(items, opts.ExplicitSource)
	}
	if opts.Verify && !opts.DryRun {
		opts.Settle = relocationSettle(opts, items)
		if err := ValidateBatchOptions(opts); err != nil {
			return err
		}
	}
	r.opts, r.items = opts, items
	return nil
}

func (r *batchRunner) preview() error {
	client, opts, result, items := r.client, r.opts, r.result, r.items

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
			item.MarkReadOnSkip = opts.MarkReadOnSkip && opts.MarkReadBefore && opts.Action == "archive"
		} else if opts.Action == "archive" && item.TargetMailbox == "" {
			if err := r.resolveArchiveDestination(client, &item); err != nil {
				item.Status, item.Error = "failed", "resolve archive destination: "+err.Error()
				result.Failed++
				result.Items = append(result.Items, item)
				if isVerificationTimeout(err) {
					return err
				}
				continue
			}
		}
		result.Skipped++
		result.Items = append(result.Items, item)
	}
	if result.Failed > 0 {
		return &BatchFailedError{Action: opts.Action, Failed: result.Failed, Attempted: len(items)}
	}
	return nil
}

func (r *batchRunner) dispatch(mutate Mutator) error {
	client, opts, result, items, chunkSize := r.client, r.opts, r.result, r.items, r.chunkSize
	for start := 0; start < len(items); start += chunkSize {
		end := min(start+chunkSize, len(items))
		chunkClient := client
		var session *jxaSession
		if !client.nativeEnabled() && opts.ReuseBridge && end-start > 1 && (opts.Action == "mark" || opts.Action == "flag" || opts.Action == "delete" || opts.Action == "move") {
			var err error
			session, err = newJXASession(client.Context())
			if err != nil {
				return err
			}
			defer session.Close()
			copy := *client
			copy.session = session
			chunkClient = &copy
		}
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "%s: chunk %d/%d (%d messages)\n", opts.Action, (start/chunkSize)+1, result.Chunks, end-start)
		}
		for _, item := range items[start:end] {
			if err := r.applyItem(chunkClient, item, mutate); err != nil {
				return err
			}
		}
		if session != nil {
			session.Close()
		}
	}
	return nil
}

func (r *batchRunner) applyItem(chunkClient *Client, item BatchItem, mutate Mutator) error {
	client, opts, result := r.client, r.opts, r.result

	if item.Status == "failed" {
		// Source resolution already refused this one.
		result.Attempted++
		return r.recordFailure(item, "mutation_failed", nil)
	}
	if err := client.Done(); err != nil {
		return err
	}
	if skip := skipReason(client, opts, item); skip != "" {
		item.Status = "skipped"
		item.Error = skip
		result.Skipped++
		if opts.Action == "archive" && opts.MarkReadOnSkip {
			item.MarkReadOnSkip = true
			if ready, err := r.markRead(chunkClient, &item, true); !ready {
				return err
			}
		}
		result.Items = append(result.Items, item)
		if err := opts.Receipt.Record("skipped", journalItem(item)); err != nil {
			return err
		}
		return nil
	}
	result.Attempted++
	if opts.Action == "archive" && item.TargetMailbox == "" {
		if err := r.resolveArchiveDestination(chunkClient, &item); err != nil {
			item.Status, item.Error = "failed", "resolve archive destination: "+err.Error()
			// No mutation was issued; a failed read is never an unknown archive.
			var stopErr error
			if isVerificationTimeout(err) {
				stopErr = err
			}
			return r.recordFailure(item, "preflight_failed", stopErr)
		}
	}
	useNativeArchive := false
	if opts.Action == "archive" {
		var err error
		useNativeArchive, err = chunkClient.archiveUsesNative(item.Account, item.SourceMailbox, item.TargetMailbox)
		if err != nil {
			item.Status, item.Error = "failed", "archive account preflight: "+err.Error()
			return r.recordFailure(item, "preflight_failed", err)
		}
	}
	if useNativeArchive && !r.nativeArchiveReady {
		if err := chunkClient.preflightNativeArchive(); err != nil {
			item.Status, item.Error = "failed", "native archive preflight: "+err.Error()
			return r.recordFailure(item, "preflight_failed", err)
		}
		r.nativeArchiveReady = true
	}
	if opts.MarkReadBefore && opts.Action != "mark" {
		if ready, err := r.markRead(chunkClient, &item, false); !ready {
			return err
		}
	}
	if err := opts.Receipt.Record("mutation_started", journalItem(item)); err != nil {
		item.Status = "failed"
		item.Error = "receipt durability before mutation: " + err.Error()
		return r.recordFailure(item, "", err)
	}
	if err := mutate(chunkClient, &item); err != nil {
		phase := "mutation_failed"
		item.Status = "failed"
		if isUnknownMutationError(client, err) {
			item.Status = "unknown"
			phase = "mutation_unknown"
		}
		item.Error = err.Error()
		result.Failed++
		if journalErr := opts.Receipt.Record(phase, journalItem(item)); journalErr != nil {
			result.Items = append(result.Items, item)
			return journalErr
		}
		if item.Status == "unknown" {
			result.Items = append(result.Items, item)
			return err
		}
	} else {
		item.Status = "succeeded"
		if err := opts.Receipt.Record("mutation_succeeded", journalItem(item)); err != nil {
			item.Status = "unknown"
			item.Error = "receipt durability after mutation: " + err.Error()
			return r.recordFailure(item, "", err)
		}
		result.Succeeded++
	}
	if opts.Progress != nil {
		fmt.Fprintf(opts.Progress, "%s: %d/%d %s %s\n", opts.Action, result.Attempted, result.Matched, item.ID, item.Status)
	}
	result.Items = append(result.Items, item)
	return nil
}

func (r *batchRunner) verify() error {
	client, opts, result := r.client, r.opts, r.result
	verifyCtx, verifyCancel := context.WithTimeout(client.Context(), effectiveVerifyBudget(opts))
	defer verifyCancel()
	verificationClient := client.WithContext(verifyCtx)

	if opts.Verify {
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "%s: verifying %d applied message(s)\n", opts.Action, result.Succeeded)
		}
		verified := VerifyMutations(verificationClient, opts, result.Items)
		for i := range verified {
			if verified[i].Status != "succeeded" {
				continue
			}
			result.Items[i] = verified[i]
			if err := r.recordVerification(verified[i]); err != nil {
				return err
			}
			if opts.Progress != nil {
				fmt.Fprintf(opts.Progress, "%s: verified %d/%d %s %s\n", opts.Action, i+1, len(verified), verified[i].ID, verified[i].VerifyStatus)
			}
		}
	}
	// Verify opted-in skipped marks together, sharing the overall verification
	// deadline and indexed snapshots. No archive is replayed for these items.
	return verifySkippedMarks(verificationClient, opts, result.Items, r.recordVerification)
}

// recordFailure retains the item before a supplemental journal failure can stop
// the run. An empty phase is used when the phase's durable write already failed.
func (r *batchRunner) recordFailure(item BatchItem, phase string, stopErr error) error {
	r.result.Failed++
	r.result.Items = append(r.result.Items, item)
	if phase != "" {
		if err := r.opts.Receipt.Record(phase, journalItem(item)); err != nil {
			return err
		}
	}
	return stopErr
}

// markRead shares the durable mark phase for skipped archives and pre-mutation
// marks. Its boolean distinguishes a recorded failure from permission to proceed.
func (r *batchRunner) markRead(client *Client, item *BatchItem, skippedArchive bool) (bool, error) {
	if err := r.opts.Receipt.Record("mark_read_started", journalItem(*item)); err != nil {
		return false, err
	}
	if err := client.MarkMessageAsRead(item.Account, item.SourceMailbox, item.ID, true); err != nil {
		item.Status = "failed"
		if skippedArchive {
			item.Error = "mark-read on skipped archive failed: " + err.Error()
		} else {
			item.Error = fmt.Sprintf("mark-read before %s failed: %v", r.opts.Action, err)
		}
		var stopErr error
		if isUnknownMutationError(r.client, err) {
			item.Status, stopErr = "unknown", err
		}
		return false, r.recordFailure(*item, "mark_read_failed", stopErr)
	}
	item.MarkedRead = true
	if err := r.opts.Receipt.Record("mark_read_succeeded", journalItem(*item)); err != nil {
		prefix := "receipt durability after mark-read: "
		if skippedArchive {
			prefix = "receipt durability after mark-read on skipped archive: "
		}
		item.Status, item.Error = "unknown", prefix+err.Error()
		return false, r.recordFailure(*item, "", err)
	}
	return true, nil
}

func (r *batchRunner) recordVerification(item BatchItem) error {
	if item.VerifyError != "" {
		r.result.Unverified++
	}
	return r.opts.Receipt.Record("verification_result", journalItem(item))
}

// Pin one successfully resolved destination per account for preview and dispatch.
func (r *batchRunner) resolveArchiveDestination(client *Client, item *BatchItem) error {
	destination, cached := r.archiveDestinations[item.Account]
	if !cached {
		var err error
		destination, err = client.ResolveArchiveMailbox(item.Account)
		if err != nil {
			return err
		}
		r.archiveDestinations[item.Account] = destination
	}
	item.TargetMailbox = destination
	return nil
}
