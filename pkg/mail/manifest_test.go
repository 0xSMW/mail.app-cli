package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func manifestFixture() CleanupManifest {
	read := true
	return CleanupManifest{Version: 1, Items: []ManifestItem{{Account: "Work", ID: "205", SourceMailbox: "INBOX", Identity: StableIdentity{Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42}, Action: "move", TargetMailbox: "Processed", Read: &read, RequiredPresent: []string{"Processed"}, RequiredAbsent: []string{"INBOX"}}}}
}

// Exercise the actual RunBatch phases and receipt writes, replacing only the
// external metadata/index and Mail mutation boundaries.
func manifestHarness(t *testing.T, m CleanupManifest) (manifestWorkflow, map[string]map[string]Message, *[]string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	c := NewClient()
	w := newManifestWorkflow(c)
	state := map[string]map[string]Message{}
	key := func(account, box string) string { return account + "/" + box }
	for _, item := range m.Items {
		k := key(item.Account, item.SourceMailbox)
		if state[k] == nil {
			state[k] = map[string]Message{}
		}
		state[k][item.ID] = Message{ID: item.ID, Account: item.Account, Mailbox: item.SourceMailbox, Sender: item.Identity.Sender, Subject: item.Identity.Subject, DateSent: item.Identity.DateSent, MessageSize: item.Identity.MessageSize, RFCMessageID: item.Identity.RFCMessageID}
	}
	w.snapshot = func(scopes []identityVerificationScope) map[verificationMailboxKey]identityVerificationResult {
		out := map[verificationMailboxKey]identityVerificationResult{}
		for _, scope := range scopes {
			matches := map[string][]Message{}
			for _, id := range scope.identities {
				if _, done := matches[id.verificationKey()]; done {
					continue
				}
				matches[id.verificationKey()] = nil
				for _, msg := range state[key(scope.key.account, scope.key.mailbox)] {
					if id.matchesEnvelope(msg) {
						matches[id.verificationKey()] = append(matches[id.verificationKey()], msg)
					}
				}
			}
			out[scope.key] = identityVerificationResult{matches: matches}
		}
		return out
	}
	w.read = func(account, box, id string) (*Message, error) {
		msg, ok := state[key(account, box)][id]
		if !ok {
			return nil, fmt.Errorf("missing selected message")
		}
		return &msg, nil
	}
	var calls []string
	w.mark = func(_ *Client, b *BatchItem, read bool) error {
		k := key(b.Account, b.SourceMailbox)
		msg := state[k][b.ID]
		msg.Read = read
		state[k][b.ID] = msg
		calls = append(calls, "mark:"+b.ID)
		return nil
	}
	w.action = func(action string) Mutator {
		return func(_ *Client, b *BatchItem) error {
			calls = append(calls, action+":"+b.ID)
			src, dst := key(b.Account, b.SourceMailbox), key(b.Account, b.TargetMailbox)
			msg := state[src][b.ID]
			delete(state[src], b.ID)
			msg.ID = "9" + b.ID
			msg.Mailbox = b.TargetMailbox
			if state[dst] == nil {
				state[dst] = map[string]Message{}
			}
			state[dst][msg.ID] = msg
			return nil
		}
	}
	w.destination = func(item ManifestItem) (string, error) { return item.TargetMailbox, nil }
	w.prepare = func(ManifestItem, *BatchItem) error { return nil }
	return w, state, &calls
}

func TestManifestApplyRegeneratedIDsAndRepeat(t *testing.T) {
	m := manifestFixture()
	w, _, calls := manifestHarness(t, m)
	r, err := w.apply(m, false)
	if err != nil || r.Verified != 1 || r.Failed != 0 || len(*calls) != 2 || r.Items[0].CurrentIDs[0] != "9205" || r.Items[0].MutationStatus != "succeeded" {
		t.Fatalf("result=%+v calls=%v err=%v", r, *calls, err)
	}
	data, err := os.ReadFile(r.ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"manifest_started", "manifest_batch_started", "manifest_batch_completed", "manifest_completed"} {
		if !strings.Contains(string(data), event) {
			t.Errorf("receipt lacks %s", event)
		}
	}
	child, err := os.ReadFile(r.Batches[0].ReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"mutation_started", "manifest_before_read", "manifest_after_read", "mutation_succeeded"} {
		if !strings.Contains(string(child), event) {
			t.Errorf("child receipt lacks %s", event)
		}
	}
	r, err = w.apply(m, false)
	if err != nil || r.Verified != 1 || len(*calls) != 2 || len(r.Batches) != 0 {
		t.Fatalf("repeat: %+v calls=%v err=%v", r, *calls, err)
	}
}

func TestManifestPreflightAllBeforeMutation(t *testing.T) {
	for _, failure := range []string{"wrong identity", "ambiguous", "missing required mailbox", "wrong account", "wrong trash"} {
		t.Run(failure, func(t *testing.T) {
			m := manifestFixture()
			other := m.Items[0]
			other.ID = "206"
			other.Identity.Subject = "second"
			m.Items = append(m.Items, other)
			w, state, calls := manifestHarness(t, m)
			switch failure {
			case "wrong identity":
				old := w.read
				w.read = func(a, b, id string) (*Message, error) {
					msg, e := old(a, b, id)
					if id == "206" {
						msg.Subject = "different"
					}
					return msg, e
				}
			case "wrong account":
				old := w.read
				w.read = func(a, b, id string) (*Message, error) {
					msg, e := old(a, b, id)
					if id == "206" {
						msg.Account = "Personal"
					}
					return msg, e
				}
			case "ambiguous":
				msg := state["Work/INBOX"]["206"]
				msg.ID = "999"
				state["Work/INBOX"]["999"] = msg
			case "missing required mailbox":
				old := w.snapshot
				w.snapshot = func(s []identityVerificationScope) map[verificationMailboxKey]identityVerificationResult {
					r := old(s)
					r[verificationMailboxKey{"Work", "Processed"}] = identityVerificationResult{err: errors.New("mailbox unavailable")}
					return r
				}
			case "wrong trash":
				m.Items[1].Action = "delete"
				w.destination = func(ManifestItem) (string, error) { return "Trash", nil }
			}
			r, err := w.apply(m, false)
			if err == nil || len(*calls) != 0 || len(r.Batches) != 0 || r.Failed == 0 {
				t.Fatalf("r=%+v calls=%v err=%v", r, *calls, err)
			}
		})
	}
}

func TestManifestPreviewAndReadOnlyObservation(t *testing.T) {
	m := manifestFixture()
	w, state, calls := manifestHarness(t, m)
	r, err := w.apply(m, true)
	if err != nil || r.Planned != 1 || r.DryRun != true || len(*calls) != 0 || len(state["Work/INBOX"]) != 1 {
		t.Fatalf("%+v calls=%v err=%v", r, *calls, err)
	}
	observed := manifestObserve(m, w.snapshot(manifestScopes(m)))
	if observed.Failed != 1 || observed.Verified != 0 || !strings.Contains(observed.Items[0].Error, "required message absent") {
		t.Fatalf("unexpected observation %+v", observed)
	}
	if len(*calls) != 0 {
		t.Fatal("observation mutated mail")
	}
}

func TestManifestPartialFailureAndStop(t *testing.T) {
	m := manifestFixture()
	for i := 1; i < 5; i++ {
		item := m.Items[0]
		item.ID = fmt.Sprint(205 + i)
		item.Identity.Subject = fmt.Sprint(i)
		m.Items = append(m.Items, item)
	}
	w, _, calls := manifestHarness(t, m)
	w.action = func(string) Mutator {
		return func(_ *Client, b *BatchItem) error {
			*calls = append(*calls, "failed:"+b.ID)
			return errors.New("provider refused")
		}
	}
	r, err := w.apply(m, false)
	if err == nil || len(r.Batches) != 1 || r.Batches[0].Attempted != 3 || r.NotAttempted != 2 || r.Verified != 0 || r.Failed != 5 {
		t.Fatalf("%+v err=%v", r, err)
	}
	if r.Items[3].Status != "not_attempted" || r.Items[0].MutationStatus != "failed" {
		t.Fatalf("outcomes=%+v", r.Items)
	}
}

func TestManifestMarkUnreadAndFinalMemberships(t *testing.T) {
	m := manifestFixture()
	unread := false
	m.Items[0].Read = &unread
	m.Items[0].Action = "mark"
	m.Items[0].TargetMailbox = ""
	m.Items[0].RequiredPresent = []string{"INBOX", "Label"}
	m.Items[0].RequiredAbsent = []string{}
	w, state, calls := manifestHarness(t, m)
	msg := state["Work/INBOX"]["205"]
	msg.Read = true
	state["Work/INBOX"]["205"] = msg
	// Model one logical message carrying a second membership.
	w.mark = func(_ *Client, b *BatchItem, read bool) error {
		msg := state["Work/INBOX"][b.ID]
		msg.Read = read
		state["Work/INBOX"][b.ID] = msg
		state["Work/Label"] = map[string]Message{b.ID: msg}
		*calls = append(*calls, "mark")
		return nil
	}
	r, err := w.apply(m, false)
	if err != nil || r.Verified != 1 || len(*calls) != 1 || state["Work/INBOX"]["205"].Read {
		t.Fatalf("%+v err=%v", r, err)
	}
}

func TestManifestRejectsInvalidSelection(t *testing.T) {
	for _, name := range []string{"version", "id", "identity", "date", "action", "read", "memberships", "duplicate", "conflict", "target", "archive retained source", "delete retained source"} {
		t.Run(name, func(t *testing.T) {
			m := manifestFixture()
			switch name {
			case "version":
				m.Version = 2
			case "id":
				m.Items[0].ID = "-2"
			case "identity":
				m.Items[0].Identity.MessageSize = 0
			case "date":
				m.Items[0].Identity.DateSent = "invalid"
			case "action":
				m.Items[0].Action = "exec"
			case "read":
				m.Items[0].Read = nil
			case "memberships":
				m.Items[0].RequiredAbsent = nil
			case "duplicate":
				m.Items = append(m.Items, m.Items[0])
			case "conflict":
				m.Items[0].RequiredPresent = append(m.Items[0].RequiredPresent, "INBOX")
			case "target":
				m.Items[0].TargetMailbox = "Other"
			case "archive retained source", "delete retained source":
				m.Items[0].Action = strings.Split(name, " ")[0]
				m.Items[0].RequiredAbsent = []string{}
				m.Items[0].RequiredPresent = append(m.Items[0].RequiredPresent, "INBOX")
			}
			if err := ValidateManifest(m); err == nil {
				t.Fatal("accepted invalid manifest")
			}
			w, _, calls := manifestHarness(t, m)
			if _, err := w.apply(m, false); err == nil || len(*calls) != 0 {
				t.Fatal("invalid manifest reached mutation")
			}
		})
	}
}

func TestReadManifestStrictJSON(t *testing.T) {
	data, _ := json.Marshal(manifestFixture())
	for _, suffix := range []string{" {}", ""} {
		path := filepath.Join(t.TempDir(), "manifest.json")
		if err := os.WriteFile(path, append(data, []byte(suffix)...), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := ReadManifest(path)
		if (err != nil) != (suffix != "") {
			t.Fatalf("suffix=%q err=%v", suffix, err)
		}
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	bad := strings.Replace(string(data), `"version":1`, `"version":1,"query":"all"`, 1)
	_ = os.WriteFile(path, []byte(bad), 0600)
	if _, err := ReadManifest(path); err == nil {
		t.Fatal("accepted unknown selection field")
	}
}

func TestManifestProductionSnapshotObservesRegeneratedID(t *testing.T) {
	c, _ := snapshotSQLiteFixture(t, `
insert into mailboxes(rowid,url) values(1,'imap://ABC/INBOX'),(2,'imap://ABC/Processed');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values(205,2,1,1,1787961600,42,1,0);
`)
	t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", os.Getenv("SNAPSHOT_SQLITE_DATABASE"))
	m := manifestFixture()
	r, err := VerifyManifest(c, m)
	if err != nil || r.Verified != 1 || len(r.Items[0].CurrentIDs) != 1 || r.Items[0].CurrentIDs[0] != "205" {
		t.Fatalf("%+v err=%v", r, err)
	}
	// The original local ID is informational once the complete stable identity
	// is verified in every required destination and absent source.
	m.Items[0].ID = "100"
	r, err = VerifyManifest(c, m)
	if err != nil || r.Verified != 1 || r.Items[0].ID != "100" || r.Items[0].CurrentIDs[0] != "205" {
		t.Fatalf("%+v err=%v", r, err)
	}
}

func TestManifestDuplicateLeafCannotProveRequiredAbsence(t *testing.T) {
	c, _ := snapshotSQLiteFixture(t, `
insert into mailboxes(rowid,url) values(1,'imap://ABC/INBOX'),(2,'imap://ABC/Processed'),(3,'imap://ABC/A/Projects'),(4,'imap://ABC/B/Projects');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values(205,2,1,1,1787961600,42,1,0),(206,4,1,1,1787961600,42,1,0);
`)
	t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", os.Getenv("SNAPSHOT_SQLITE_DATABASE"))
	m := manifestFixture()
	m.Items[0].RequiredAbsent = append(m.Items[0].RequiredAbsent, "Projects")
	r, err := VerifyManifest(c, m)
	if err == nil || r.Verified != 0 || r.Failed != 1 || !strings.Contains(r.Items[0].Error, "ambiguous") {
		t.Fatalf("ambiguous absent scope accepted: %+v err=%v", r, err)
	}
	// A fully qualified physical path identifies the intended empty scope.
	m.Items[0].RequiredAbsent[1] = "A/Projects"
	r, err = VerifyManifest(c, m)
	if err != nil || r.Verified != 1 {
		t.Fatalf("explicit empty scope rejected: %+v err=%v", r, err)
	}
}

func TestManifestAlreadyCompleteCannotAuthorizeWrongSpecialDestination(t *testing.T) {
	for _, action := range []string{"archive", "delete"} {
		t.Run(action, func(t *testing.T) {
			m := manifestFixture()
			m.Items[0].Action = action
			m.Items[0].TargetMailbox = "Records"
			m.Items[0].RequiredPresent = []string{"Records"}
			w, state, calls := manifestHarness(t, m)
			msg := state["Work/INBOX"]["205"]
			msg.Read = true
			msg.Mailbox = "Records"
			msg.ID = "901"
			delete(state["Work/INBOX"], "205")
			state["Work/Records"] = map[string]Message{"901": msg}
			w.destination = func(ManifestItem) (string, error) {
				if action == "delete" {
					return "Trash", nil
				}
				return "Archive", nil
			}
			// The mailbox snapshot alone matches the requested state. The
			// action's actual special destination must still be proved.
			if observed := manifestObserve(m, w.snapshot(manifestScopes(m))); observed.Verified != 1 {
				t.Fatalf("fixture not complete: %+v", observed)
			}
			for _, dryRun := range []bool{false, true} {
				r, err := w.apply(m, dryRun)
				if err == nil || r.Verified != 0 || r.Failed != 1 || r.Items[0].Status != "failed" || !strings.Contains(r.Items[0].Error, "actual "+action+" destination") || len(*calls) != 0 || len(r.Batches) != 0 {
					t.Fatalf("apply dryRun=%t: %+v calls=%v err=%v", dryRun, r, *calls, err)
				}
			}
			r, err := w.verify(m)
			if err == nil || r.Verified != 0 || r.Failed != 1 || r.Items[0].Status != "failed" || !strings.Contains(r.Items[0].Error, "actual "+action+" destination") || len(*calls) != 0 {
				t.Fatalf("verify: %+v calls=%v err=%v", r, *calls, err)
			}
		})
	}
}
