package mail

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

type outgoingFactory func(*nativeMail, OutgoingInput, string, string) (ae.Value, error)

// Each composition owns exactly one outgoing descriptor until dispatch. The
// automation gate remains held through readback and detached bounded cleanup.
type nativeOutgoingRun struct {
	n                   *nativeMail
	input               OutgoingInput
	action              string
	create              outgoingFactory
	result              *OutgoingResult
	owned, box          ae.Value
	before              map[string]bool
	body, step, cleanup string
	attempted, saved    bool
	attachmentProofs    []outgoingAttachmentProof
	preSaveOwnedBody    string
	preparedDraft       *draftSnapshot
	preparedAccountID   string
}

func (r *nativeOutgoingRun) cleanupOwned(err error) {
	if err == nil || !nativeReference(r.owned) || r.attempted {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.n.ctx), 5*time.Second)
	defer cancel()
	clean := *r.n
	clean.ctx = ctx
	if _, e := clean.command("core", "clos", map[string]ae.Value{"----": r.owned, "savo": ae.Enum("no  ")}); e != nil {
		r.cleanup = e.Error()
	}
}

// submittedCleanup closes only the exact composition Mail returned to this run.
// A send acknowledgement remains authoritative even if cleanup cannot complete.
func (r *nativeOutgoingRun) submittedCleanup(expected map[string]ae.Value) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.n.ctx), 5*time.Second)
	defer cancel()
	clean := *r.n
	clean.ctx = ctx
	// A missing owned backend does not establish absence of its saved copy.
	_, err := clean.get(ae.Property("subj", r.owned))
	if err != nil && !nativeMissing(err) {
		return err
	}
	if err == nil {
		current, err := clean.ownedComposeSnapshot(r.owned)
		if err != nil {
			if nativeMissing(err) {
				_, rootErr := clean.get(ae.Property("subj", r.owned))
				if nativeMissing(rootErr) {
					return r.cleanupPreparedDraft(&clean)
				}
				if rootErr != nil {
					return fmt.Errorf("recheck submitted composition root: %w", rootErr)
				}
			}
			return fmt.Errorf("read submitted composition snapshot: %w", err)
		}
		if _, available := expected["files"]; !available {
			delete(current, "files")
		}
		if !reflect.DeepEqual(expected, current) {
			return fmt.Errorf("submitted composition changed; retained")
		}
		if _, err = clean.command("core", "clos", map[string]ae.Value{"----": r.owned, "savo": ae.Enum("no  ")}); err != nil {
			if !nativeMissing(err) {
				return fmt.Errorf("close submitted composition: %w", err)
			}
			_, rootErr := clean.get(ae.Property("subj", r.owned))
			if rootErr == nil {
				return fmt.Errorf("close submitted composition while root remains present: %w", err)
			}
			if !nativeMissing(rootErr) {
				return fmt.Errorf("recheck submitted composition after close: %w", rootErr)
			}
		}
	}
	return r.cleanupPreparedDraft(&clean)
}

func (n *nativeMail) ownedComposeSnapshot(owned ae.Value) (map[string]ae.Value, error) {
	fields, err := n.getFields(owned, "sndr", "subj", "ctnt")
	if err != nil {
		return nil, err
	}
	for _, code := range []string{"sndr", "subj", "ctnt"} {
		if _, err = nativeString(fields[code], "owned compose "+code); err != nil {
			return nil, err
		}
	}
	for _, class := range []string{"trcp", "crcp", "brcp"} {
		recipients, err := n.outgoingRecipients(owned, class)
		if err != nil {
			return nil, err
		}
		values := make([]ae.Value, len(recipients))
		for i, address := range recipients {
			values[i] = ae.String(address)
		}
		fields[class] = ae.List(values...)
	}
	count, err := n.outgoingAttachmentCount(owned)
	if err != nil {
		return nil, err
	}
	fields["atts"] = ae.Int(count)
	// Mail does not expose attachment filenames on every outgoing backend.
	// Where available, retain them too so same-count replacements are guarded.
	if names, nameErr := n.outgoingAttachmentNames(owned, false); nameErr == nil && int64(len(names)) == count {
		values := make([]ae.Value, len(names))
		for i, name := range names {
			values[i] = ae.String(name)
		}
		fields["files"] = ae.List(values...)
	}
	return fields, nil
}

func (r *nativeOutgoingRun) draftBaseline(account ae.Value) error {
	var err error
	r.box, err = r.n.outgoingDraftMailbox(account)
	if err != nil {
		return err
	}
	v, e := r.n.get(ae.Property("ID  ", ae.All("mssg", r.box)))
	if e != nil {
		return e
	}
	ids, e := nativeList(v, "draft IDs")
	if e != nil {
		return e
	}
	for _, id := range ids {
		if id.Kind != "int" || id.Number <= 0 {
			return fmt.Errorf("draft baseline ID unavailable")
		}
		key := outgoingID(id)
		if r.before[key] {
			return fmt.Errorf("draft baseline ID is ambiguous")
		}
		r.before[key] = true
	}
	return nil
}

func (r *nativeOutgoingRun) compose() error {
	var e error
	if r.create == nil {
		r.owned, e = r.n.command("core", "crel", map[string]ae.Value{"kocl": ae.TypeCode("bcke"), "prdt": ae.Record(map[string]ae.Value{"subj": ae.String(r.input.Subject), "ctnt": ae.String(r.body), "sndr": ae.String(r.result.Sender), "pvis": ae.Bool(false)})})
	} else {
		r.owned, e = r.create(r.n, r.input, r.result.Sender, r.body)
	}
	if e != nil {
		var transport *ae.Error
		if errors.As(e, &transport) && transport.Attempted && (r.create == nil || r.n.phaseName == "threaded_create") && !nativeReference(r.owned) {
			r.cleanup = "outgoing creation may have completed; owned object descriptor unavailable"
		}
		return e
	}
	if !nativeReference(r.owned) {
		r.cleanup = "outgoing creation receipt did not identify an owned object"
		return fmt.Errorf("Mail did not return an owned outgoing object")
	}
	if r.create != nil {
		for code, value := range map[string]ae.Value{"subj": ae.String(r.input.Subject), "ctnt": ae.String(r.body), "pvis": ae.Bool(false)} {
			if e = r.n.set(ae.Property(code, r.owned), value); e != nil {
				return e
			}
		}
		for _, class := range []string{"trcp", "crcp", "brcp"} {
			v, e := r.n.get(ae.All(class, r.owned))
			if e != nil {
				return e
			}
			refs, e := nativeList(v, "inherited recipients")
			if e != nil {
				return e
			}
			for _, ref := range refs {
				if _, e = r.n.command("core", "delo", map[string]ae.Value{"----": ref}); e != nil {
					return e
				}
			}
		}
	}
	if e = r.n.set(ae.Property("sndr", r.owned), ae.String(r.result.Sender)); e != nil {
		return e
	}
	for i, class := range []string{"trcp", "crcp", "brcp"} {
		for _, address := range [][]string{r.input.To, r.input.Cc, r.input.Bcc}[i] {
			_, e = r.n.command("core", "crel", map[string]ae.Value{"kocl": ae.TypeCode(class), "insh": r.owned, "prdt": ae.Record(map[string]ae.Value{"radd": ae.String(address)})})
			if e != nil {
				return e
			}
		}
	}
	r.step = "recipient_verification"
	if e = r.n.outgoingVerify(r.owned, r.input, r.result.AccountID, r.result.Sender, r.body, true); e != nil {
		return e
	}
	r.step = "attachments"
	if e = r.n.outgoingAttach(r.owned, r.input.Attachments); e != nil {
		return e
	}
	if r.create != nil && len(r.input.Attachments) == 0 {
		count, e := r.n.outgoingAttachmentCount(r.owned)
		if e != nil {
			return e
		}
		if count != 0 {
			return fmt.Errorf("outgoing contains unrequested inherited attachments")
		}
	}
	r.step = "sender_verification"
	return r.n.outgoingVerify(r.owned, r.input, r.result.AccountID, r.result.Sender, r.body, len(r.input.Attachments) == 0)
}

func (r *nativeOutgoingRun) dispatch() error {
	if r.action == "send" {
		expected, err := r.n.ownedComposeSnapshot(r.owned)
		if err != nil {
			return err
		}
		err = r.n.phase("outgoing_send", func() error {
			if e := r.n.ctx.Err(); e != nil {
				return e
			}
			r.attempted = true
			v, e := r.n.command("emsg", "send", map[string]ae.Value{"----": r.owned})
			if e != nil {
				return e
			}
			if v.Kind != "bool" || !v.Boolean {
				return fmt.Errorf("Mail did not accept send (expected true)")
			}
			r.result.Submitted = true
			r.result.Status = "submitted"
			return nil
		})
		if err != nil {
			return err
		}
		r.step = "submitted_cleanup"
		if err = r.submittedCleanup(expected); err != nil {
			r.result.Status = "submitted_cleanup_pending"
			r.cleanup = err.Error()
			return err
		}
		return nil
	}
	snapshot, err := r.n.ownedComposeSnapshot(r.owned)
	if err != nil {
		return err
	}
	r.preSaveOwnedBody = snapshot["ctnt"].Text
	return r.n.phase("outgoing_save", func() error {
		if e := r.n.ctx.Err(); e != nil {
			return e
		}
		r.attempted = true
		if _, e := r.n.command("core", "save", map[string]ae.Value{"----": r.owned}); e != nil {
			return e
		}
		r.saved = true
		current, err := r.n.ownedComposeSnapshot(r.owned)
		if err != nil {
			return err
		}
		if _, available := snapshot["files"]; !available {
			delete(current, "files")
		}
		if !reflect.DeepEqual(snapshot, current) {
			return fmt.Errorf("owned composition changed during save; retained")
		}
		_, e := r.n.command("core", "clos", map[string]ae.Value{"----": r.owned, "savo": ae.Enum("yes ")})
		return e
	})
}

func (r *nativeOutgoingRun) savedDraftCandidate(strictReadiness bool) (*Message, error) {
	// Read IDs together so polling never reads every preexisting draft.
	// Only new account-scoped IDs receive the strict fingerprint read.
	v, e := r.n.get(ae.Property("ID  ", ae.All("mssg", r.box)))
	if e != nil {
		return nil, e
	}
	ids, e := nativeList(v, "saved draft IDs")
	if e != nil {
		return nil, e
	}
	var candidates []*Message
	seen := map[string]bool{}
	for _, id := range ids {
		if id.Kind != "int" || id.Number <= 0 {
			return nil, fmt.Errorf("saved draft ID unavailable")
		}
		key := outgoingID(id)
		if seen[key] {
			return nil, fmt.Errorf("saved draft ID is ambiguous")
		}
		seen[key] = true
		if r.before[key] {
			continue
		}
		ref := ae.ByID("mssg", id, r.box)
		m, e := r.n.outgoingSnapshotMessage(ref, r.input.Account, r.box)
		if e != nil {
			if nativeMissing(e) {
				if strictReadiness {
					return nil, nil
				}
				continue
			}
			var inactive *NotFoundError
			if strictReadiness && errors.As(e, &inactive) && inactive.Kind == "draft" {
				continue
			}
			return nil, e
		}
		if m.ID != key {
			return nil, fmt.Errorf("saved draft ID readback mismatch")
		}
		names, e := r.n.outgoingAttachmentNames(ref, true)
		if e != nil {
			return nil, e
		}
		sender, e := draftSenderAddress(m.Sender)
		if e != nil {
			continue
		}
		if m.Subject == r.input.Subject && r.savedDraftBodyMatches(m.Content) && strings.EqualFold(sender, r.result.Sender) && outgoingEqual(m.ToRecipients, r.input.To) && outgoingEqual(m.CcRecipients, r.input.Cc) && outgoingEqual(m.BccRecipients, r.input.Bcc) && outgoingEqual(names, outgoingExpectedNames(r.input.Attachments)) {
			candidates = append(candidates, m)
		}
	}
	if len(candidates) > 1 {
		return nil, fmt.Errorf("saved draft identity is ambiguous")
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	return nil, nil
}

func (r *nativeOutgoingRun) verifySavedDraft() error {
	for i := 0; i < 80; i++ {
		candidate, err := r.savedDraftCandidate(false)
		if err != nil {
			return err
		}
		if candidate != nil {
			r.result.Draft = candidate
			r.result.Status = "saved"
			return nil
		}
		if err = r.n.outgoingPause(); err != nil {
			return err
		}
	}
	return fmt.Errorf("saved draft could not be verified in target account Drafts")
}

// Exchange can replace a newly saved draft's local ID during synchronization.
// Reconcile against the original pre-save baseline, never by subject alone,
// and never repeat the save or close that created the stored draft.
func (r *nativeOutgoingRun) freshSavedDraftCandidate() (*Message, error) {
	account, err := r.n.account(r.input.Account)
	if err != nil {
		return nil, err
	}
	accountID, err := r.n.stringProperty("ID  ", account)
	if err != nil {
		return nil, err
	}
	if accountID != r.result.AccountID {
		return nil, fmt.Errorf("saved draft account identity changed")
	}
	box, err := r.n.outgoingDraftMailbox(account)
	if err != nil {
		return nil, err
	}
	r.box = box
	return r.savedDraftCandidate(true)
}

func savedDraftReadinessTransient(err error) bool {
	if nativeMissing(err) {
		return true
	}
	var missing *NotFoundError
	return errors.As(err, &missing) && missing.Kind == "message"
}

func (r *nativeOutgoingRun) verifySavedDraftAttachmentReadiness() error {
	var pending error
	for i := 0; i < 80; i++ {
		err := r.verifySavedDraftAttachmentObservation()
		if err == nil {
			return nil
		}
		if !errors.Is(err, errSavedDraftAttachmentPending) && !savedDraftReadinessTransient(err) {
			return err
		}
		pending = err
		if i < 79 {
			if err := r.n.outgoingPause(); err != nil {
				return err
			}
		}
	}
	return fmt.Errorf("saved draft attachment readiness could not be verified: %w", pending)
}

var errSavedDraftAttachmentPending = errors.New("saved draft attachment identity not stable")

// Each observation uses only getters and local hashing. Both snapshots must
// identify the same unique active account-owned draft, with unchanged MIME.
func (r *nativeOutgoingRun) verifySavedDraftAttachmentObservation() error {
	before, err := r.freshSavedDraftCandidate()
	if err != nil {
		return err
	}
	if before == nil {
		return errSavedDraftAttachmentPending
	}
	r.result.Draft = before
	ref, err := r.n.scopedDraft(before.Account, before.Mailbox, before.ID)
	if err != nil {
		return err
	}
	source, err := r.n.stringProperty("raso", ref)
	if err != nil {
		return err
	}
	if err = proveOutgoingMIMEAttachments(r.n.ctx, source, r.attachmentProofs); err != nil {
		return err
	}
	after, err := r.freshSavedDraftCandidate()
	if err != nil {
		return err
	}
	if after == nil || after.ID != before.ID {
		return errSavedDraftAttachmentPending
	}
	if !sameSavedAttachmentSnapshot(before, after) {
		return fmt.Errorf("saved draft changed during attachment verification")
	}
	current, err := r.n.scopedDraft(after.Account, after.Mailbox, after.ID)
	if err != nil {
		return err
	}
	finalSource, err := r.n.stringProperty("raso", current)
	if err != nil {
		return err
	}
	if finalSource != source {
		return fmt.Errorf("saved draft source changed during attachment verification")
	}
	final, err := r.freshSavedDraftCandidate()
	if err != nil {
		return err
	}
	if final == nil || final.ID != after.ID {
		return errSavedDraftAttachmentPending
	}
	if !sameSavedAttachmentSnapshot(final, after) {
		return fmt.Errorf("saved draft changed after attachment verification")
	}

	inputs, err := hashOutgoingAttachmentInputs(r.n.ctx, r.input.Attachments)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(inputs, r.attachmentProofs) {
		return fmt.Errorf("attachment input files changed during composition")
	}
	r.result.Draft = after
	r.result.Status = "saved"
	return nil
}

// Mail can add a display name or change address casing while synchronizing a
// saved draft. Only the already-validated parsed sender address is canonical;
// every other snapshot field remains exact for attached-save verification.
func sameSavedAttachmentSnapshot(a, b *Message) bool {
	if a == nil || b == nil {
		return a == b
	}
	senderA, err := draftSenderAddress(a.Sender)
	if err != nil {
		return false
	}
	senderB, err := draftSenderAddress(b.Sender)
	if err != nil || !strings.EqualFold(senderA, senderB) {
		return false
	}
	left, right := *a, *b
	left.Sender, right.Sender = "", ""
	return reflect.DeepEqual(left, right)
}
