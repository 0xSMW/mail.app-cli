package mail

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// BatchItem is one message inside a mutation receipt. The same shape is used
// whether one ID or five hundred were requested.
type BatchItem struct {
	ID               string `json:"id"`
	Account          string `json:"account"`
	SourceMailbox    string `json:"sourceMailbox"`
	TargetMailbox    string `json:"targetMailbox,omitempty"`
	Subject          string `json:"subject,omitempty"`
	Sender           string `json:"sender,omitempty"`
	DateSent         string `json:"dateSent,omitempty"`
	MessageSize      int    `json:"messageSize,omitempty"`
	GmailInboxSource bool   `json:"gmailInboxSource,omitempty"`
	// Identity is captured before a move so verification survives Mail.app
	// assigning the logical message a new local ID.
	Identity     StableIdentity `json:"identity,omitempty"`
	Status       string         `json:"status"`
	Error        string         `json:"error,omitempty"`
	MarkedRead   bool           `json:"markedRead,omitempty"`
	VerifyStatus string         `json:"verifyStatus,omitempty"`
	VerifyError  string         `json:"verifyError,omitempty"`
}

// BatchResult is the mutation receipt.
type BatchResult struct {
	Action       string      `json:"action"`
	DryRun       bool        `json:"dryRun"`
	StartedAt    string      `json:"startedAt,omitempty"`
	EndedAt      string      `json:"endedAt,omitempty"`
	ReceiptPath  string      `json:"receiptPath,omitempty"`
	JournalError string      `json:"journalError,omitempty"`
	Matched      int         `json:"matched"`
	Attempted    int         `json:"attempted"`
	Succeeded    int         `json:"succeeded"`
	Failed       int         `json:"failed"`
	Unverified   int         `json:"unverified,omitempty"`
	Skipped      int         `json:"skipped"`
	Chunks       int         `json:"chunks,omitempty"`
	Items        []BatchItem `json:"items"`
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
	// ExplicitSource preserves an explicit non-Gmail source when the Envelope
	// Index is unavailable. Gmail remains fail-closed because labels cannot be
	// resolved safely without the index.
	ExplicitSource bool
	// Receipt is an append-only durable journal written before and after every
	// mailbox-side phase. It is optional so library callers retain the existing
	// in-memory receipt behavior.
	Receipt *BatchJournal
}

// BatchFailedError reports mutation failures and requested verifications that
// remained inconclusive after the mutation was applied.
type BatchFailedError struct {
	Action     string
	Failed     int
	Unverified int
	Attempted  int
}

func (e *BatchFailedError) Error() string {
	if e.Failed == 0 && e.Unverified > 0 {
		return fmt.Sprintf("%s applied for %d message(s), but verification was inconclusive for %d", e.Action, e.Attempted, e.Unverified)
	}
	if e.Unverified > 0 {
		return fmt.Sprintf("%s failed for %d of %d message(s); verification was inconclusive for %d", e.Action, e.Failed, e.Attempted, e.Unverified)
	}
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
func RunBatch(client *Client, opts BatchOptions, items []BatchItem, mutate Mutator) (result BatchResult, retErr error) {
	result = BatchResult{
		Action:    opts.Action,
		DryRun:    opts.DryRun,
		StartedAt: time.Now().Format(time.RFC3339),
		Matched:   len(items),
		Items:     make([]BatchItem, 0, len(items)),
	}
	autoReceipt := false
	if opts.Receipt == nil {
		journal, err := CreateDefaultBatchJournal()
		if err != nil {
			return result, err
		}
		opts.Receipt = journal
		autoReceipt = true
	}
	result.ReceiptPath = opts.Receipt.Path()
	if err := opts.Receipt.Record("started", map[string]any{
		"action": opts.Action, "dryRun": opts.DryRun, "matched": len(items),
	}); err != nil {
		return result, err
	}
	defer func() {
		if opts.Verify && client.Done() != nil {
			for i := range result.Items {
				item := &result.Items[i]
				if item.Status != "succeeded" || item.VerifyStatus != "" {
					continue
				}
				item.VerifyStatus = "unknown_after_timeout"
				item.VerifyError = "verification not completed: " + client.Done().Error()
				result.Unverified++
				if err := opts.Receipt.Record("verification_result", journalItem(*item)); err != nil {
					result.JournalError = err.Error()
					retErr = errors.Join(retErr, err)
				}
			}
		}
		result.EndedAt = time.Now().Format(time.RFC3339)
		terminal := "completed"
		if client.Done() != nil || isUnknownMutationError(client, retErr) || hasUnknownItem(result.Items) {
			terminal = "interrupted"
		}
		if err := opts.Receipt.Record(terminal, map[string]any{"result": result}); err != nil {
			result.JournalError = err.Error()
			retErr = errors.Join(retErr, err)
		}
		if autoReceipt {
			if err := opts.Receipt.Close(); err != nil {
				result.JournalError = err.Error()
				retErr = errors.Join(retErr, err)
			}
		}
	}()
	for i := range items {
		if opts.TargetMailbox != "" {
			items[i].TargetMailbox = opts.TargetMailbox
		}
	}
	chunkSize := normalizedChunkSize(len(items), opts.ChunkSize)
	result.Chunks = (len(items) + chunkSize - 1) / chunkSize
	if opts.Action == "archive" && !opts.TrustSource {
		items = client.archiveSources(items, opts.ExplicitSource)
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
		if result.Failed > 0 {
			return result, &BatchFailedError{Action: opts.Action, Failed: result.Failed, Attempted: len(items)}
		}
		return result, nil
	}
	if opts.Verify && (opts.Action == "archive" || opts.Action == "move") {
		for i := range items {
			if items[i].Status == "failed" {
				continue
			}
			identity, err := client.captureStableIdentity(items[i])
			if err != nil {
				items[i].Status = "failed"
				items[i].Error = "capture stable identity: " + err.Error()
				continue
			}
			items[i].Identity = identity
		}
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
				if err := opts.Receipt.Record("mutation_failed", journalItem(item)); err != nil {
					return result, err
				}
				continue
			}
			if err := client.Done(); err != nil {
				return result, err
			}
			if skip := skipReason(client, opts, item); skip != "" {
				item.Status = "skipped"
				item.Error = skip
				result.Skipped++
				result.Items = append(result.Items, item)
				if err := opts.Receipt.Record("skipped", journalItem(item)); err != nil {
					return result, err
				}
				continue
			}
			result.Attempted++
			if opts.MarkReadBefore && opts.Action != "mark" {
				if err := client.MarkMessageAsRead(item.Account, item.SourceMailbox, item.ID, true); err != nil {
					item.Status = "failed"
					if isUnknownMutationError(client, err) {
						item.Status = "unknown"
					}
					item.Error = fmt.Sprintf("mark-read before %s failed: %v", opts.Action, err)
					result.Failed++
					result.Items = append(result.Items, item)
					if journalErr := opts.Receipt.Record("mark_read_failed", journalItem(item)); journalErr != nil {
						return result, journalErr
					}
					continue
				}
				item.MarkedRead = true
				if err := opts.Receipt.Record("mark_read_succeeded", journalItem(item)); err != nil {
					item.Status = "unknown"
					item.Error = "receipt durability after mark-read: " + err.Error()
					result.Failed++
					result.Items = append(result.Items, item)
					return result, err
				}
			}
			if err := opts.Receipt.Record("mutation_started", journalItem(item)); err != nil {
				item.Status = "failed"
				item.Error = "receipt durability before mutation: " + err.Error()
				result.Failed++
				result.Items = append(result.Items, item)
				return result, err
			}
			if err := mutate(client, &item); err != nil {
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
					return result, journalErr
				}
			} else {
				item.Status = "succeeded"
				if err := opts.Receipt.Record("mutation_succeeded", journalItem(item)); err != nil {
					item.Status = "unknown"
					item.Error = "receipt durability after mutation: " + err.Error()
					result.Failed++
					result.Items = append(result.Items, item)
					return result, err
				}
				result.Succeeded++
			}
			if opts.Progress != nil {
				fmt.Fprintf(opts.Progress, "%s: %d/%d %s %s\n", opts.Action, result.Attempted, len(items), item.ID, item.Status)
			}
			result.Items = append(result.Items, item)
		}
	}
	if opts.Verify {
		if opts.Progress != nil {
			fmt.Fprintf(opts.Progress, "%s: verifying %d applied message(s)\n", opts.Action, result.Succeeded)
		}
		verified := VerifyMutations(client, opts, result.Items)
		for i := range verified {
			if verified[i].Status != "succeeded" {
				continue
			}
			result.Items[i] = verified[i]
			if verified[i].VerifyError != "" {
				result.Unverified++
			}
			if err := opts.Receipt.Record("verification_result", journalItem(verified[i])); err != nil {
				return result, err
			}
			if opts.Progress != nil {
				fmt.Fprintf(opts.Progress, "%s: verified %d/%d %s %s\n", opts.Action, i+1, len(verified), verified[i].ID, verified[i].VerifyStatus)
			}
		}
	}
	if result.Failed > 0 || result.Unverified > 0 {
		return result, &BatchFailedError{Action: opts.Action, Failed: result.Failed, Unverified: result.Unverified, Attempted: result.Attempted}
	}
	return result, nil
}

func isUnknownMutationError(client *Client, err error) bool {
	if err == nil {
		return false
	}
	if client.Done() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isAutomationTimeout(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "timeout") || strings.Contains(message, "timed out") || strings.Contains(message, "deadline exceeded") || strings.Contains(message, "context canceled")
}

func hasUnknownItem(items []BatchItem) bool {
	for _, item := range items {
		if item.Status == "unknown" || item.VerifyStatus == "unknown_after_timeout" {
			return true
		}
	}
	return false
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
// mailbox so archive never strips a user label. A message listed under
// All Mail (a Gmail search hit, say) is looked up too, because it may still
// carry the INBOX label. An item under a label that the index cannot place
// is marked failed rather than archived from that label.
func (c *Client) archiveSources(items []BatchItem, explicitSource bool) []BatchItem {
	// Resolve every archive source, including an explicitly supplied INBOX:
	// the label set is needed both to select the right source and to prove the
	// narrow Gmail INBOX-label transition verification outcome.
	needsLookup := func(BatchItem) bool { return true }
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
		case err != nil && IsArchiveAlias(item.SourceMailbox):
			// Without the index a message listed under All Mail is left
			// there; the skip below reports it.
		case err != nil:
			if explicitSource {
				gmail, gmailErr := c.isGmailAccount(item.Account)
				if gmailErr == nil && !gmail {
					// A non-Gmail IMAP/iCloud mailbox has no label alias to
					// resolve, so the human's explicit source remains usable.
					continue
				}
				items[i].Status = "failed"
				if gmailErr != nil {
					items[i].Error = fmt.Sprintf("cannot determine whether %s uses Gmail labels while the Envelope Index is unavailable (%v)", item.Account, gmailErr)
				} else {
					items[i].Error = "cannot safely archive a Gmail message while the Envelope Index is unavailable because INBOX labels cannot be resolved"
				}
				continue
			}
			items[i].Status = "failed"
			items[i].Error = fmt.Sprintf("cannot resolve a safe archive source without the Envelope Index (%v)", err)
		case !ok || !strings.EqualFold(loc.Account, item.Account):
			if IsArchiveAlias(item.SourceMailbox) {
				continue
			}
			items[i].Status = "failed"
			items[i].Error = "message not in the Envelope Index; archive from INBOX with --mailbox"
		default:
			items[i] = archiveItemFromLocation(item, loc)
		}
	}
	return items
}

func archiveItemFromLocation(item BatchItem, location MessageLocation) BatchItem {
	item.SourceMailbox = location.ArchiveMailbox
	item.GmailInboxSource = location.IsGmail && strings.EqualFold(location.ArchiveMailbox, "INBOX")
	return item
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
	if r.Failed == 0 && r.Unverified == 0 && r.Skipped == 0 {
		return fmt.Sprintf("%s %s%s", verb, count, target)
	}
	var summary string
	if r.Failed == 0 && r.Succeeded == r.Matched-r.Skipped {
		summary = fmt.Sprintf("%s %s%s", verb, countNoun(r.Succeeded, "message"), target)
	} else {
		summary = fmt.Sprintf("%s %d of %s%s", verb, r.Succeeded, count, target)
	}
	if r.Failed > 0 {
		summary += fmt.Sprintf("; %d failed", r.Failed)
	}
	if r.Unverified > 0 {
		summary += fmt.Sprintf("; %d unverified", r.Unverified)
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

// VerifyMutations verifies a completed mutation batch. State-only mutations
// share one indexed snapshot per account/mailbox instead of enumerating the
// entire Mail.app mailbox once per item.
func VerifyMutations(client *Client, opts BatchOptions, items []BatchItem) []BatchItem {
	verified := append([]BatchItem(nil), items...)
	type groupKey struct{ account, mailbox string }
	groups := make(map[groupKey][]int)
	if opts.Action == "mark" || opts.Action == "flag" || opts.Action == "delete" {
		for i := range verified {
			if verified[i].Status == "succeeded" {
				key := groupKey{verified[i].Account, verified[i].SourceMailbox}
				groups[key] = append(groups[key], i)
			}
		}
		if len(groups) == 0 {
			return verified
		}
		snapshots := make(map[groupKey]map[string]Message)
		snapshotErrors := make(map[groupKey]error)
		for attempt, delay := range verificationBackoff {
			if attempt > 0 {
				if err := waitForVerificationBackoff(client.Context(), delay); err != nil {
					for key := range groups {
						snapshotErrors[key] = err
					}
					break
				}
			}
			for key, indices := range groups {
				ids := make([]string, 0, len(indices))
				for _, index := range indices {
					ids = append(ids, verified[index].ID)
				}
				snapshots[key], snapshotErrors[key] = client.getMessageEnvelopesForVerification(key.account, key.mailbox, ids)
			}
		}
		for key, indices := range groups {
			for _, index := range indices {
				item := &verified[index]
				if err := snapshotErrors[key]; err != nil {
					message, fallbackErr := client.GetMessageStateForVerificationJSON(item.Account, item.SourceMailbox, item.ID)
					if fallbackErr == nil {
						if message != nil {
							snapshots[key] = map[string]Message{item.ID: *message}
						} else {
							snapshots[key] = map[string]Message{}
						}
					} else {
						item.VerifyStatus = "verification-failed"
						if isVerificationTimeout(fallbackErr) {
							item.VerifyStatus = "unknown_after_timeout"
						}
						item.VerifyError = fallbackErr.Error()
						continue
					}
				}
				message, present := snapshots[key][item.ID]
				switch opts.Action {
				case "delete":
					if present {
						item.VerifyStatus = "present-in-source"
						item.VerifyError = fmt.Sprintf("message still present in %s", item.SourceMailbox)
					} else {
						item.VerifyStatus = "absent-from-source"
					}
				case "mark":
					item.VerifyStatus, item.VerifyError = verifyBoolState(present, message.Read, opts.Read, "read")
				case "flag":
					item.VerifyStatus, item.VerifyError = verifyBoolState(present, message.Flagged, opts.Flagged, "flagged")
				}
			}
		}
		return verified
	}

	syncErrors := make(map[string]error)
	successful := 0
	for _, item := range verified {
		if item.Status == "succeeded" {
			successful++
		}
		if item.Status == "succeeded" && isValidGmailInboxTransition(item) {
			if _, seen := syncErrors[item.Account]; !seen {
				syncErrors[item.Account] = client.SyncAccount(item.Account)
			}
		}
	}
	if successful == 0 {
		return verified
	}
	type relocationRef struct {
		index  int
		source bool
	}
	identityGroups := make(map[groupKey][]relocationRef)
	for i, item := range verified {
		if item.Status != "succeeded" || syncErrors[item.Account] != nil {
			continue
		}
		identityGroups[groupKey{item.Account, item.SourceMailbox}] = append(identityGroups[groupKey{item.Account, item.SourceMailbox}], relocationRef{index: i, source: true})
		identityGroups[groupKey{item.Account, item.TargetMailbox}] = append(identityGroups[groupKey{item.Account, item.TargetMailbox}], relocationRef{index: i})
	}
	presences := make([]verificationPresence, len(verified))
	lookupErrors := make([]error, len(verified))
	for attempt, delay := range verificationBackoff {
		if attempt > 0 {
			if err := waitForVerificationBackoff(client.Context(), delay); err != nil {
				for i := range verified {
					if verified[i].Status == "succeeded" {
						lookupErrors[i] = err
					}
				}
				break
			}
		}
		presences = make([]verificationPresence, len(verified))
		lookupErrors = make([]error, len(verified))
		for key, refs := range identityGroups {
			identities := make([]StableIdentity, 0, len(refs))
			for _, ref := range refs {
				identities = append(identities, verified[ref.index].Identity)
			}
			counts, groupErr := client.getIdentityCountsForVerification(key.account, key.mailbox, identities)
			for _, ref := range refs {
				item := verified[ref.index]
				present := false
				err := groupErr
				if err == nil {
					count := counts[item.Identity.fallbackKey()]
					switch count {
					case 0:
					case 1:
						present = true
					default:
						present, err = client.hasMessageIdentityWithWhoseJXA(key.account, key.mailbox, item.Identity)
					}
				} else if !isVerificationTimeout(err) {
					present, err = client.hasMessageIdentityWithWhoseJXA(key.account, key.mailbox, item.Identity)
				}
				if err != nil {
					lookupErrors[ref.index] = err
					continue
				}
				if ref.source {
					presences[ref.index].Source = present
				} else {
					presences[ref.index].Destination = present
				}
			}
		}
	}
	for i := range verified {
		item := &verified[i]
		if item.Status != "succeeded" {
			continue
		}
		if syncErr, ok := syncErrors[item.Account]; ok && syncErr != nil {
			item.VerifyStatus, item.VerifyError = classifyGmailSyncError(syncErr)
			continue
		}
		if err := lookupErrors[i]; err != nil {
			item.VerifyStatus = "applied_destination_unverified"
			if isVerificationTimeout(err) {
				item.VerifyStatus = "unknown_after_timeout"
			}
			item.VerifyError = err.Error()
			continue
		}
		presence := presences[i]
		switch {
		case presence.Source:
			item.VerifyStatus = "present_in_source"
			item.VerifyError = fmt.Sprintf("message still present in %s", item.SourceMailbox)
		case presence.Destination:
			item.VerifyStatus = "confirmed_destination"
		default:
			item.VerifyStatus = "applied_destination_unverified"
			item.VerifyError = fmt.Sprintf("message left %s but was not found in %s", item.SourceMailbox, item.TargetMailbox)
		}
	}
	return verified
}

func verifyBoolState(present, actual, expected bool, field string) (string, string) {
	if !present {
		return "verification-failed", "message not found after mutation"
	}
	if actual == expected {
		return "matched", ""
	}
	return "mismatch", field + " status mismatch"
}

func classifyGmailSyncError(err error) (string, string) {
	wrapped := fmt.Errorf("sync before Gmail archive verification: %w", err)
	if isVerificationTimeout(err) {
		return "unknown_after_timeout", wrapped.Error()
	}
	return "applied_destination_unverified", wrapped.Error()
}

// VerifyMutation verifies one mutation for library callers. RunBatch uses
// VerifyMutations so a whole batch can share indexed snapshots and Gmail sync.
func VerifyMutation(client *Client, opts BatchOptions, item BatchItem) (string, error) {
	if item.Status == "" {
		item.Status = "succeeded"
	}
	return verifyMutation(client, opts, item, true)
}

func verifyMutation(client *Client, opts BatchOptions, item BatchItem, syncGmail bool) (string, error) {
	switch opts.Action {
	case "archive", "move":
		if item.TargetMailbox == "" || strings.EqualFold(item.TargetMailbox, item.SourceMailbox) {
			return "already-in-destination", nil
		}
		if !item.Identity.valid() {
			return "applied_destination_unverified", fmt.Errorf("stable identity was not captured before mutation")
		}
		if syncGmail && isValidGmailInboxTransition(item) {
			// Force Mail.app to reconcile the Gmail labels before the bounded
			// settling loop decides whether INBOX stayed removed.
			if err := client.SyncAccount(item.Account); err != nil {
				if isVerificationTimeout(err) {
					return "unknown_after_timeout", fmt.Errorf("sync before Gmail archive verification: %w", err)
				}
				return "applied_destination_unverified", fmt.Errorf("sync before Gmail archive verification: %w", err)
			}
		}
		return verifyRelocationWithLookup(client.Context(), item, func() (verificationPresence, error) {
			inSource, err := client.hasMessageIdentityForVerification(item.Account, item.SourceMailbox, item.Identity)
			if err != nil {
				return verificationPresence{}, err
			}
			inDestination, err := client.hasMessageIdentityForVerification(item.Account, item.TargetMailbox, item.Identity)
			if err != nil {
				return verificationPresence{}, err
			}
			return verificationPresence{Source: inSource, Destination: inDestination}, nil
		}, waitForVerificationBackoff)
	}
	verified := VerifyMutations(client, opts, []BatchItem{item})
	if len(verified) != 1 {
		return "verification-failed", fmt.Errorf("verification snapshot returned no item")
	}
	if verified[0].VerifyError != "" {
		return verified[0].VerifyStatus, errors.New(verified[0].VerifyError)
	}
	return verified[0].VerifyStatus, nil
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
