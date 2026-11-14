package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// CleanupManifest is an explicit, reusable selection. Reading or applying it
// never discovers additional messages or changes an item's account.
type CleanupManifest struct {
	Version int            `json:"version"`
	Items   []ManifestItem `json:"items"`
}

type ManifestItem struct {
	Account         string         `json:"account"`
	ID              string         `json:"id"`
	SourceMailbox   string         `json:"sourceMailbox"`
	Identity        StableIdentity `json:"identity"`
	Action          string         `json:"action"`
	TargetMailbox   string         `json:"targetMailbox,omitempty"`
	Read            *bool          `json:"read"`
	RequiredPresent []string       `json:"requiredPresent"`
	RequiredAbsent  []string       `json:"requiredAbsent"`
}

type ManifestOutcome struct {
	Account        string              `json:"account"`
	ID             string              `json:"id"`
	Action         string              `json:"action"`
	CurrentIDs     []string            `json:"currentIds"`
	Memberships    map[string][]string `json:"memberships"`
	Status         string              `json:"status"`
	Error          string              `json:"error,omitempty"`
	MutationStatus string              `json:"mutationStatus,omitempty"`
}

type ManifestResult struct {
	Version      int               `json:"version"`
	DryRun       bool              `json:"dryRun"`
	Matched      int               `json:"matched"`
	Verified     int               `json:"verified"`
	Failed       int               `json:"failed"`
	Planned      int               `json:"planned"`
	NotAttempted int               `json:"notAttempted"`
	ReceiptPath  string            `json:"receiptPath,omitempty"`
	Items        []ManifestOutcome `json:"items"`
	Batches      []BatchResult     `json:"batches,omitempty"`
}

func ReadManifest(path string) (CleanupManifest, error) {
	var m CleanupManifest
	f, err := os.Open(path)
	if err != nil {
		return m, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return m, fmt.Errorf("manifest must contain exactly one JSON document")
	}
	return m, ValidateManifest(m)
}

func ValidateManifest(m CleanupManifest) error {
	if m.Version != 1 {
		return fmt.Errorf("unsupported manifest version %d (expected 1)", m.Version)
	}
	if len(m.Items) == 0 {
		return fmt.Errorf("manifest requires at least one item")
	}
	seen := map[string]bool{}
	for i, item := range m.Items {
		fail := func(s string) error { return fmt.Errorf("manifest item %d: %s", i+1, s) }
		if strings.TrimSpace(item.Account) == "" || strings.TrimSpace(item.SourceMailbox) == "" || strings.TrimSpace(item.ID) == "" {
			return fail("account, id, and sourceMailbox are required")
		}
		if n, err := strconv.ParseUint(item.ID, 10, 64); err != nil || n == 0 {
			return fail("id must be a positive local message ID")
		}
		id := item.Identity
		if !id.hasFallback() || canonicalIdentityDate(id.DateSent) != id.DateSent {
			return fail("identity requires sender, subject, canonical sent date, and positive messageSize")
		}
		if _, ok := ParseMessageTime(id.DateSent); !ok {
			return fail("identity dateSent must be a valid timestamp")
		}
		if id.RFCMessageID != "" && validRFCMessageID(id.RFCMessageID) == "" {
			return fail("identity rfcMessageId is invalid")
		}
		if item.Read == nil || item.RequiredPresent == nil || item.RequiredAbsent == nil {
			return fail("read, requiredPresent, and requiredAbsent must be explicit")
		}
		members := map[string]bool{}
		for _, box := range append(append([]string{}, item.RequiredPresent...), item.RequiredAbsent...) {
			if strings.TrimSpace(box) == "" || members[box] {
				return fail("mailbox memberships must be nonempty and disjoint, without duplicates")
			}
			members[box] = true
		}
		if len(item.RequiredPresent) == 0 {
			return fail("requiredPresent must establish an observable final message and read state")
		}
		switch item.Action {
		case "archive", "delete", "move":
			if item.TargetMailbox == "" || item.TargetMailbox == item.SourceMailbox || !manifestContains(item.RequiredPresent, item.TargetMailbox) {
				return fail("relocations require a distinct targetMailbox in requiredPresent")
			}
			if !members[item.SourceMailbox] {
				return fail("sourceMailbox must have an explicit final membership")
			}
			if (item.Action == "archive" || item.Action == "delete") && !manifestContains(item.RequiredAbsent, item.SourceMailbox) {
				return fail("archive and delete require sourceMailbox in requiredAbsent")
			}
		case "mark":
			if item.TargetMailbox != "" || !manifestContains(item.RequiredPresent, item.SourceMailbox) {
				return fail("mark requires sourceMailbox in requiredPresent and no targetMailbox")
			}
		default:
			return fail("action must be archive, delete, move, or mark")
		}
		key := item.Account + "\x00" + id.verificationKey()
		localKey := item.Account + "\x00local:" + item.ID
		if seen[key] || seen[localKey] {
			return fail("duplicate selected identity or local ID in account")
		}
		seen[key], seen[localKey] = true, true
	}
	return nil
}

func manifestContains(items []string, value string) bool {
	for _, s := range items {
		if s == value {
			return true
		}
	}
	return false
}

// manifestWorkflow keeps execution boundaries injectable without exposing a
// public arbitrary-action API. Production always uses exact identity reads.
type manifestWorkflow struct {
	snapshot    func([]identityVerificationScope) map[verificationMailboxKey]identityVerificationResult
	read        func(string, string, string) (*Message, error)
	run         func(BatchOptions, []BatchItem, Mutator) (BatchResult, error)
	client      *Client
	destination func(ManifestItem) (string, error)
	mark        func(*Client, *BatchItem, bool) error
	action      func(string) Mutator
	prepare     func(ManifestItem, *BatchItem) error
}

func newManifestWorkflow(c *Client) manifestWorkflow {
	return manifestWorkflow{client: c, snapshot: c.getIdentitySnapshotForVerification, read: c.GetMessageDetailsForVerificationJSON,
		prepare: func(item ManifestItem, b *BatchItem) error {
			if item.Action == "delete" {
				return c.prepareDeleteItem(b, item.TargetMailbox)
			}
			if item.Action == "archive" {
				prepared := c.archiveSources([]BatchItem{*b}, true)[0]
				if prepared.Status == "failed" {
					return fmt.Errorf("resolve archive source: %s", prepared.Error)
				}
				if !manifestContains(item.RequiredAbsent, prepared.SourceMailbox) {
					return fmt.Errorf("resolved archive source %s must be in requiredAbsent", prepared.SourceMailbox)
				}
				*b = prepared
			}
			return nil
		},
		mark: func(c *Client, b *BatchItem, read bool) error { return MarkMutator(read)(c, b) },
		action: func(action string) Mutator {
			switch action {
			case "archive":
				return ArchiveMutator(false)
			case "delete":
				return DeleteMutator
			case "move":
				return MoveMutator(false)
			}
			return nil
		},
		destination: func(item ManifestItem) (string, error) {
			if item.Action == "archive" {
				return c.ResolveArchiveMailbox(item.Account)
			}
			if item.Action == "delete" {
				return c.ResolveTrashMailbox(item.Account)
			}
			return item.TargetMailbox, nil
		},
		run: func(o BatchOptions, items []BatchItem, m Mutator) (BatchResult, error) {
			return RunBatch(c, o, items, m)
		}}
}

func manifestScopes(m CleanupManifest) []identityVerificationScope {
	var scopes []identityVerificationScope
	indices := map[verificationMailboxKey]int{}
	for _, item := range m.Items {
		boxes := append(append([]string{item.SourceMailbox}, item.RequiredPresent...), item.RequiredAbsent...)
		for _, box := range boxes {
			key := verificationMailboxKey{item.Account, box}
			idx, ok := indices[key]
			if !ok {
				idx = len(scopes)
				indices[key] = idx
				scopes = append(scopes, identityVerificationScope{key: key, strict: true})
			}
			scopes[idx].identities = append(scopes[idx].identities, item.Identity)
		}
	}
	return scopes
}

func manifestObserve(m CleanupManifest, snapshot map[verificationMailboxKey]identityVerificationResult) ManifestResult {
	r := ManifestResult{Version: 1, Matched: len(m.Items), Items: make([]ManifestOutcome, len(m.Items))}
	for i, item := range m.Items {
		o := ManifestOutcome{Account: item.Account, ID: item.ID, Action: item.Action, Status: "verified", CurrentIDs: []string{}, Memberships: map[string][]string{}}
		ids := map[string]bool{}
		var failures []string
		boxes := append(append([]string{item.SourceMailbox}, item.RequiredPresent...), item.RequiredAbsent...)
		for _, box := range boxes {
			if _, done := o.Memberships[box]; done {
				continue
			}
			o.Memberships[box] = []string{}
			s, ok := snapshot[verificationMailboxKey{item.Account, box}]
			if !ok || s.err != nil {
				failures = append(failures, fmt.Sprintf("%s: snapshot unavailable (%v)", box, s.err))
				continue
			}
			matches := s.matches[item.Identity.verificationKey()]
			for _, msg := range matches {
				o.Memberships[box] = append(o.Memberships[box], msg.ID)
				ids[msg.ID] = true
				if StableIdentityFromMessage(msg).fallbackKey() != item.Identity.fallbackKey() {
					failures = append(failures, box+": selected metadata differs")
				}
			}
			if len(matches) > 1 {
				failures = append(failures, box+": ambiguous stable identity")
				continue
			}
			if manifestContains(item.RequiredPresent, box) {
				if len(matches) != 1 {
					failures = append(failures, box+": required message absent")
				} else if matches[0].Read != *item.Read {
					failures = append(failures, box+": read state differs")
				}
			}
			if manifestContains(item.RequiredAbsent, box) && len(matches) != 0 {
				failures = append(failures, box+": message must be absent")
			}
		}
		for id := range ids {
			o.CurrentIDs = append(o.CurrentIDs, id)
		}
		sort.Strings(o.CurrentIDs)
		if len(failures) > 0 {
			o.Status = "unverified"
			o.Error = strings.Join(failures, "; ")
			r.Failed++
		} else {
			r.Verified++
		}
		r.Items[i] = o
	}
	return r
}

// VerifyManifest observes explicit mailbox memberships and read state in one
// indexed snapshot. It does not synchronize accounts or perform mutations.
func VerifyManifest(c *Client, m CleanupManifest) (ManifestResult, error) {
	return newManifestWorkflow(c).verify(m)
}

func (w manifestWorkflow) validateTarget(item ManifestItem) error {
	if item.Action != "archive" && item.Action != "delete" {
		return nil
	}
	target, err := w.destination(item)
	if err != nil {
		return err
	}
	if target != item.TargetMailbox {
		return fmt.Errorf("targetMailbox differs from the account's actual %s destination", item.Action)
	}
	return nil
}

func (w manifestWorkflow) verify(m CleanupManifest) (ManifestResult, error) {
	if err := ValidateManifest(m); err != nil {
		return ManifestResult{}, err
	}
	r := manifestObserve(m, w.snapshot(manifestScopes(m)))
	for i, item := range m.Items {
		if err := w.validateTarget(item); err != nil {
			if r.Items[i].Status == "verified" {
				r.Verified--
				r.Failed++
			}
			r.Items[i].Status = "failed"
			r.Items[i].Error = strings.Trim(strings.Join([]string{r.Items[i].Error, err.Error()}, "; "), "; ")
		}
	}
	if r.Failed > 0 {
		return r, &BatchFailedError{Action: "manifest", Unverified: r.Failed, Attempted: r.Matched}
	}
	return r, nil
}

// ApplyManifest resolves every selected identity before the first mutation.
// Only an exact verified end state permits a repeat invocation to skip work.
func ApplyManifest(c *Client, m CleanupManifest, dryRun bool) (ManifestResult, error) {
	return newManifestWorkflow(c).apply(m, dryRun)
}

func (w manifestWorkflow) exact(item ManifestItem, id string) error {
	return w.exactAt(item, item.SourceMailbox, id)
}

func (w manifestWorkflow) exactAt(item ManifestItem, box, id string) error {
	msg, err := w.read(item.Account, box, id)
	if err != nil {
		return err
	}
	if msg == nil || msg.Deleted || msg.ID != id || msg.Account != item.Account || !item.Identity.matchesEnvelope(*msg) {
		return fmt.Errorf("live selected identity/account mismatch")
	}
	actual := StableIdentityFromMessage(*msg)
	if actual.fallbackKey() != item.Identity.fallbackKey() {
		return fmt.Errorf("live selected metadata mismatch")
	}
	return nil
}

func (w manifestWorkflow) apply(m CleanupManifest, dryRun bool) (result ManifestResult, retErr error) {
	if err := ValidateManifest(m); err != nil {
		return result, err
	}
	snapshot := w.snapshot(manifestScopes(m))
	result = manifestObserve(m, snapshot)
	result.DryRun = dryRun
	resolved := make([]BatchItem, len(m.Items))
	invalid := false
	for i, item := range m.Items {
		if err := w.validateTarget(item); err != nil {
			invalid = true
			result.Items[i].Status = "failed"
			result.Items[i].Error = err.Error()
			continue
		}
		if result.Items[i].Status == "verified" {
			for _, box := range item.RequiredPresent {
				matches := snapshot[verificationMailboxKey{item.Account, box}].matches[item.Identity.verificationKey()]
				if err := w.exactAt(item, box, matches[0].ID); err != nil {
					invalid = true
					result.Items[i].Status = "failed"
					result.Items[i].Error = err.Error()
					break
				}
			}
			continue
		}
		var err error
		// Any unreadable or ambiguous scope invalidates the entire selection,
		// including absence constraints; a source hit cannot override it.
		for _, box := range append(append([]string{item.SourceMailbox}, item.RequiredPresent...), item.RequiredAbsent...) {
			s, ok := snapshot[verificationMailboxKey{item.Account, box}]
			if !ok || s.err != nil {
				err = fmt.Errorf("cannot resolve mailbox %s: %v", box, s.err)
				break
			}
			if len(s.matches[item.Identity.verificationKey()]) > 1 {
				err = fmt.Errorf("ambiguous identity in %s", box)
				break
			}
			for _, msg := range s.matches[item.Identity.verificationKey()] {
				if StableIdentityFromMessage(msg).fallbackKey() != item.Identity.fallbackKey() {
					err = fmt.Errorf("selected metadata differs in %s", box)
					break
				}
			}
			if err != nil {
				break
			}
		}
		s := snapshot[verificationMailboxKey{item.Account, item.SourceMailbox}]
		matches := s.matches[item.Identity.verificationKey()]
		if err == nil && len(matches) != 1 {
			err = fmt.Errorf("selected stable identity is not unique in sourceMailbox")
		}
		if err == nil {
			err = w.exact(item, matches[0].ID)
		}
		if err != nil {
			invalid = true
			result.Items[i].Status = "failed"
			result.Items[i].Error = err.Error()
			continue
		}
		resolved[i] = BatchItem{ID: matches[0].ID, Account: item.Account, SourceMailbox: item.SourceMailbox, TargetMailbox: item.TargetMailbox, Identity: item.Identity, Subject: item.Identity.Subject, Sender: item.Identity.Sender, DateSent: item.Identity.DateSent, MessageSize: item.Identity.MessageSize}
		if err := w.prepare(item, &resolved[i]); err != nil {
			invalid = true
			result.Items[i].Status = "failed"
			result.Items[i].Error = err.Error()
			continue
		}
		result.Items[i].Status = "planned"
		result.Items[i].Error = ""
	}
	if invalid {
		result.Failed, result.Verified = 0, 0
		for i := range result.Items {
			if result.Items[i].Status == "planned" {
				result.Items[i].Status = "not_attempted"
				result.NotAttempted++
			}
			if result.Items[i].Status == "failed" {
				result.Failed++
			}
			if result.Items[i].Status == "verified" {
				result.Verified++
			}
		}
		return result, fmt.Errorf("manifest preflight failed; no messages changed")
	}
	journal, err := CreateDefaultBatchJournal()
	if err != nil {
		return result, err
	}
	defer func() { retErr = errors.Join(retErr, journal.Close()) }()
	result.ReceiptPath = journal.Path()
	if err := journal.Record("manifest_started", map[string]any{"manifest": m, "dryRun": dryRun}); err != nil {
		return result, err
	}
	defer func() {
		retErr = errors.Join(retErr, journal.Record("manifest_completed", map[string]any{"result": result}))
	}()
	// Group by exact execution scope, retaining manifest order inside each
	// group. Every group and each Mail operation runs sequentially.
	type group struct {
		item    ManifestItem
		indices []int
	}
	var groups []group
	groupIndex := map[string]int{}
	for i, item := range m.Items {
		if result.Items[i].Status != "planned" {
			continue
		}
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%t", item.Account, item.Action, item.SourceMailbox, item.TargetMailbox, *item.Read)
		idx, ok := groupIndex[key]
		if !ok {
			idx = len(groups)
			groupIndex[key] = idx
			groups = append(groups, group{item: item})
		}
		groups[idx].indices = append(groups[idx].indices, i)
	}
	stopped := false
	for _, g := range groups {
		if stopped {
			for _, i := range g.indices {
				result.Items[i].Status = "not_attempted"
				result.NotAttempted++
			}
			continue
		}
		items := make([]BatchItem, 0, len(g.indices))
		byID := map[string]ManifestItem{}
		for _, i := range g.indices {
			items = append(items, resolved[i])
			byID[resolved[i].ID] = m.Items[i]
		}
		child, err := CreateDefaultBatchJournal()
		if err != nil {
			return result, errors.Join(retErr, err)
		}
		if err := journal.Record("manifest_batch_started", map[string]any{"receiptPath": child.Path(), "indices": g.indices}); err != nil {
			_ = child.Close()
			return result, errors.Join(retErr, err)
		}
		mutate := func(c *Client, b *BatchItem) error {
			item := byID[b.ID]
			if item.Action == "archive" && !manifestContains(item.RequiredAbsent, b.SourceMailbox) {
				return fmt.Errorf("resolved archive source %s must be in requiredAbsent", b.SourceMailbox)
			}
			// Archive source resolution and recoverable delete may pin a more
			// specific physical path; identity and account remain unchanged.
			item.SourceMailbox = b.SourceMailbox
			if err := w.exact(item, b.ID); err != nil {
				return err
			}
			if item.Action == "mark" {
				return w.mark(c, b, *item.Read)
			}
			if err := child.Record("manifest_before_read", map[string]any{"item": *b, "read": *item.Read}); err != nil {
				return err
			}
			if err := w.mark(c, b, *item.Read); err != nil {
				return err
			}
			if err := child.Record("manifest_after_read", map[string]any{"item": *b, "read": *item.Read}); err != nil {
				return err
			}
			if action := w.action(item.Action); action != nil {
				return action(c, b)
			}
			return fmt.Errorf("unsupported manifest action")
		}
		opts := BatchOptions{Action: g.item.Action, TargetMailbox: g.item.TargetMailbox, Read: *g.item.Read, DryRun: dryRun, TrustSource: g.item.Action != "archive", ExplicitSource: true, MaxConsecutiveFailures: 3, Receipt: child}
		batch, err := w.run(opts, items, mutate)
		closeErr := child.Close()
		err = errors.Join(err, closeErr)
		result.Batches = append(result.Batches, batch)
		if e := journal.Record("manifest_batch_completed", map[string]any{"receiptPath": child.Path(), "result": batch}); e != nil {
			return result, errors.Join(retErr, err, e)
		}
		retErr = errors.Join(retErr, err)
		for n, i := range g.indices {
			if n >= len(batch.Items) {
				result.Items[i].Status = "not_attempted"
				result.NotAttempted++
				continue
			}
			b := batch.Items[n]
			if b.Status == "not_attempted" {
				result.NotAttempted++
			}
			result.Items[i].MutationStatus = b.Status
			if dryRun && b.Status == "dry-run" {
				result.Planned++
				continue
			}
			result.Items[i].Status = b.Status
			result.Items[i].Error = b.Error
		}
		stopped = batch.StoppedReason != "" || batch.NotAttempted > 0 || hasUnknownItem(batch.Items) || batch.JournalError != "" || closeErr != nil || w.client.Done() != nil || (err != nil && batch.Attempted == 0)
	}
	if dryRun {
		result.Failed = 0
		for _, o := range result.Items {
			if o.Status == "failed" {
				result.Failed++
			}
		}
		return result, retErr
	}
	observed := manifestObserve(m, w.snapshot(manifestScopes(m)))
	result.Verified, result.Failed = observed.Verified, observed.Failed
	for i := range result.Items {
		status := result.Items[i].Status
		mutation := result.Items[i].MutationStatus
		mutationErr := result.Items[i].Error
		result.Items[i] = observed.Items[i]
		result.Items[i].MutationStatus = mutation
		if status == "not_attempted" && result.Items[i].Status != "verified" {
			result.Items[i].Status = status
		}
		if mutationErr != "" {
			result.Items[i].Error = strings.Trim(strings.Join([]string{mutationErr, result.Items[i].Error}, "; "), "; ")
		}
	}
	if result.Failed > 0 {
		retErr = errors.Join(retErr, &BatchFailedError{Action: "manifest", Unverified: result.Failed, Attempted: result.Matched})
	}
	return result, retErr
}
