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
	IdentityCaptureMillis   int64          `json:"identityCaptureMillis,omitempty"`
	IdentityEnrichmentError string         `json:"identityEnrichmentError,omitempty"`
	MarkReadOnSkip          bool           `json:"markReadOnSkip,omitempty"`
	Identity                StableIdentity `json:"identity,omitempty"`
	Status                  string         `json:"status"`
	Error                   string         `json:"error,omitempty"`
	MarkedRead              bool           `json:"markedRead,omitempty"`
	VerifyStatus            string         `json:"verifyStatus,omitempty"`
	VerifyError             string         `json:"verifyError,omitempty"`
	// Observed is what an archive or move verification found by stable
	// identity once the mailboxes settled.
	Observed *RelocationState `json:"observed,omitempty"`
}

// RelocationState reports the source and the destination separately, because
// a move Mail.app accepted does not prove the source copy is gone. The IDs
// are the current local IDs, which differ from the ID the mutation was given
// when Mail.app regenerated the message.
type RelocationState struct {
	ObservedAt            string   `json:"observedAt"`
	VerificationScope     string   `json:"verificationScope,omitempty"`
	SyncRequestedAt       string   `json:"syncRequestedAt,omitempty"`
	Account               string   `json:"account"`
	SourceMailbox         string   `json:"sourceMailbox"`
	DestinationMailbox    string   `json:"destinationMailbox"`
	SourceRemovalRequired bool     `json:"sourceRemovalRequired"`
	SourcePresent         bool     `json:"sourcePresent"`
	DestinationPresent    bool     `json:"destinationPresent"`
	DestinationRead       *bool    `json:"destinationRead,omitempty"`
	SourceIDs             []string `json:"sourceIds,omitempty"`
	DestinationIDs        []string `json:"destinationIds,omitempty"`
}

// BatchResult is the mutation receipt.
type BatchResult struct {
	Action            string      `json:"action"`
	MarkReadRequested bool        `json:"markReadRequested,omitempty"`
	DryRun            bool        `json:"dryRun"`
	StartedAt         string      `json:"startedAt,omitempty"`
	EndedAt           string      `json:"endedAt,omitempty"`
	ReceiptPath       string      `json:"receiptPath,omitempty"`
	JournalError      string      `json:"journalError,omitempty"`
	Matched           int         `json:"matched"`
	Attempted         int         `json:"attempted"`
	Succeeded         int         `json:"succeeded"`
	Failed            int         `json:"failed"`
	Unverified        int         `json:"unverified,omitempty"`
	Skipped           int         `json:"skipped"`
	Chunks            int         `json:"chunks,omitempty"`
	Items             []BatchItem `json:"items"`
}

// BatchOptions controls one mutation run.
type BatchOptions struct {
	Action               string
	TargetMailbox        string
	DryRun               bool
	Verify               bool
	MarkReadBefore       bool
	MarkReadOnSkip       bool
	readOnlyVerification bool
	// Settle extends the final-observation window; zero preserves the default.
	Settle time.Duration
	// VerifyBudget bounds all post-mutation checks; zero uses the default.
	VerifyBudget time.Duration
	ChunkSize    int
	// ReuseBridge is for built-in JXA mutators, never custom mutators.
	ReuseBridge bool
	Read        bool
	Flagged     bool
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
		if item.TargetMailbox == "" {
			destination, err := client.ResolveArchiveMailbox(item.Account)
			if err != nil {
				return err
			}
			item.TargetMailbox = destination
		}
		if err := client.ArchiveMessageToDestination(item.Account, item.SourceMailbox, item.ID, item.TargetMailbox); err != nil {
			return err
		}
		if journal {
			_ = UpdateRecentMessageLocation(item.Account, item.ID, item.TargetMailbox, "archive")
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
		Action:            opts.Action,
		MarkReadRequested: opts.MarkReadBefore,
		DryRun:            opts.DryRun,
		StartedAt:         time.Now().Format(time.RFC3339),
		Matched:           len(items),
		Items:             make([]BatchItem, 0, len(items)),
	}
	if err := ValidateBatchOptions(opts); err != nil {
		return result, err
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
		"action": opts.Action, "dryRun": opts.DryRun, "matched": len(items), "markReadBefore": opts.MarkReadBefore, "markReadOnSkip": opts.MarkReadOnSkip, "settle": opts.Settle.String(), "verifyBudget": opts.VerifyBudget.String(),
	}); err != nil {
		return result, err
	}
	r := batchRunner{client: client, opts: opts, result: &result, items: items, autoReceipt: autoReceipt, archiveDestinations: make(map[string]string)}
	defer r.finish(&retErr)
	if err := r.prepare(); err != nil {
		return result, err
	}
	if r.opts.DryRun {
		err := r.preview()
		return result, err
	}
	if r.opts.Verify && (r.opts.Action == "archive" || r.opts.Action == "move") {
		if err := captureBatchIdentities(client, r.opts, r.items); err != nil {
			return result, err
		}
	}
	if err := r.dispatch(mutate); err != nil {
		return result, err
	}
	if err := r.verify(); err != nil {
		return result, err
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
	var nativeArchiveErr *NativeArchiveError
	if errors.As(err, &nativeArchiveErr) {
		return true
	}
	if client.Done() != nil {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errSessionInterrupted) || isAutomationTimeout(err) {
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
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if len(ids) == 0 {
		return items
	}
	located, err := c.LocateMessages(ids)
	for i, item := range items {
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
			if opts.MarkReadOnSkip {
				summary += " (would mark read)"
			}
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
		marked := 0
		for _, item := range r.Items {
			if item.MarkReadOnSkip && item.MarkedRead {
				marked++
			}
		}
		if marked > 0 {
			summary += fmt.Sprintf(" (%d marked read)", marked)
		}
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
	if err := ValidateBatchOptions(opts); err != nil {
		for i := range verified {
			if verified[i].Status == "succeeded" {
				verified[i].VerifyStatus, verified[i].VerifyError = "verification-failed", err.Error()
			}
		}
		return verified
	}
	ctx, cancel := context.WithTimeout(client.Context(), effectiveVerifyBudget(opts))
	defer cancel()
	client = client.WithContext(ctx)
	schedule := verificationSchedule(relocationSettle(opts, items))
	for i := range verified {
		if verified[i].Status == "succeeded" {
			verified[i].VerifyStatus, verified[i].VerifyError, verified[i].Observed = "", "", nil
		}
	}
	if opts.Action == "mark" || opts.Action == "flag" || opts.Action == "delete" {
		verifyMutationStates(client, opts, verified, schedule)
	} else {
		verifyMutationRelocations(client, opts, verified, schedule)
	}
	return verified
}

// agreedReadState is the read status shared by every matching row, or nil
// when there is no row or the rows disagree.
func agreedReadState(messages []Message) *bool {
	if len(messages) == 0 {
		return nil
	}
	read := messages[0].Read
	for _, message := range messages[1:] {
		if message.Read != read {
			return nil
		}
	}
	return &read
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
