package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
)

type labelProviderFixture struct {
	email          string
	labels         []gmail.Label
	ids            []string
	message        gmail.Message
	modify         func([]string, []string) error
	readErr        error
	gets, modifies int
	journal        string
}

func (p *labelProviderFixture) Profile(context.Context) (gmail.Profile, error) {
	return gmail.Profile{EmailAddress: p.email}, nil
}
func (p *labelProviderFixture) ListLabels(context.Context) ([]gmail.Label, error) {
	return p.labels, nil
}
func (p *labelProviderFixture) FindMessages(context.Context, string) ([]string, error) {
	return p.ids, nil
}
func (p *labelProviderFixture) GetMessage(context.Context, string) (gmail.Message, error) {
	p.gets++
	if p.gets > 1 && p.readErr != nil {
		return gmail.Message{}, p.readErr
	}
	return p.message, nil
}
func (p *labelProviderFixture) ModifyLabels(_ context.Context, _ string, add, remove []string) error {
	p.modifies++
	if p.journal != "" {
		b, err := os.ReadFile(p.journal)
		if err != nil || !strings.Contains(string(b), "gmail_label_dispatch") {
			return errors.New("dispatch receipt missing before request")
		}
	}
	if p.modify != nil {
		return p.modify(add, remove)
	}
	for _, id := range add {
		p.message.LabelIDs = desiredLabels(p.message.LabelIDs, id, true)
	}
	for _, id := range remove {
		p.message.LabelIDs = desiredLabels(p.message.LabelIDs, id, false)
	}
	return nil
}

func labelWorkflowFixture(t *testing.T) (*GmailLabelWorkflow, *labelProviderFixture, GmailLabelRequest) {
	t.Helper()
	p := &labelProviderFixture{email: "me@example.com", ids: []string{"provider-1"}, labels: []gmail.Label{{ID: "IMPORTANT", Name: "IMPORTANT", Type: "system"}, {ID: "Label_1", Name: "Projects/One", Type: "user"}, {ID: "Label_2", Name: "Other/One", Type: "user"}, {ID: "INBOX", Name: "INBOX", Type: "system"}}, message: gmail.Message{ID: "provider-1", LabelIDs: []string{"IMPORTANT", "Label_2"}, SizeEstimate: 99999, Payload: gmail.Payload{Headers: []gmail.Header{{Name: "Message-ID", Value: "<real@example.com>"}, {Name: "From", Value: "Provider Name <sender@example.com>"}, {Name: "Subject", Value: "subject"}, {Name: "Date", Value: "Fri, 28 Aug 2026 19:00:00 +0700"}}}}}
	w := &GmailLabelWorkflow{Provider: p}
	w.account = func(context.Context, string) (*Account, error) {
		return &Account{Name: "Work", AccountType: "imap", Enabled: true, EmailAddress: "me@example.com"}, nil
	}
	w.message = func(context.Context, GmailLabelRequest) (*Message, error) {
		return &Message{ID: "1", RFCMessageID: "<real@example.com>", Sender: "Mail Name <sender@example.com>", Subject: "subject", DateSent: "2026-08-28T12:00:00Z", MessageSize: 42, Read: true}, nil
	}
	w.snapshot = func(context.Context, string, StableIdentity) (GmailLabelSnapshot, error) {
		members := []string{"All Mail", "Other/One"}
		if hasLabel(p.message.LabelIDs, "IMPORTANT") {
			members = append(members, "Important")
		}
		if hasLabel(p.message.LabelIDs, "Label_1") {
			members = append(members, "Projects/One")
		}
		return GmailLabelSnapshot{LocalIDs: []string{"99"}, Memberships: members, Read: true}, nil
	}
	j, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	p.journal = j.Path()
	return w, p, GmailLabelRequest{Account: "Work", Mailbox: "Important", MessageID: "1", Label: "IMPORTANT", Receipt: j}
}

func TestGmailLabelImportantAndCustomExactDelta(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "important remove", true: "nested custom add"}[custom], func(t *testing.T) {
			w, p, r := labelWorkflowFixture(t)
			if custom {
				r.Label = "Projects/One"
				r.Add = true
			}
			out, err := w.Change(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if out.Status != "changed_verified" || !out.ProviderConfirmed || out.MailStatus != "verified" || !out.ProviderAllMail || p.modifies != 1 || p.gets != 2 {
				t.Fatalf("result %+v, modifies %d gets %d", out, p.modifies, p.gets)
			}
			if !hasLabel(out.AfterLabelIDs, "Label_2") || hasLabel(out.AfterLabelIDs, "UNREAD") {
				t.Fatal("unrelated/read state changed")
			}
			if out.MailAfter.LocalIDs[0] != "99" {
				t.Fatal("regenerated Mail ID was lost")
			}
			if custom && !hasLabel(out.AfterLabelIDs, "IMPORTANT") {
				t.Fatal("custom change removed Important")
			}
		})
	}
}

func TestGmailLabelNoopAndDryRunReadFullState(t *testing.T) {
	for _, dry := range []bool{false, true} {
		w, p, r := labelWorkflowFixture(t)
		r.DryRun = dry
		r.Add = !dry
		out, err := w.Change(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
		want := "noop"
		if dry {
			want = "dry_run"
		}
		if out.Status != want || p.modifies != 0 || p.gets != 1 || out.MailBefore == nil || len(out.BeforeLabelIDs) != 2 {
			t.Fatalf("%+v", out)
		}
		wantMail := "verified"
		if dry {
			wantMail = "observed"
		}
		if out.MailStatus != wantMail {
			t.Fatalf("mailStatus = %s, want %s", out.MailStatus, wantMail)
		}
		b, _ := os.ReadFile(p.journal)
		if strings.Contains(string(b), "gmail_label_dispatch") {
			t.Fatal("read-only invocation wrote dispatch receipt")
		}
		if !dry {
			if out.ReceiptPath == "" || !strings.Contains(string(b), "gmail_label_noop") {
				t.Fatal("noop lacks durable receipt")
			}
			reconciled, err := w.Reconcile(context.Background(), p.journal)
			if err != nil || reconciled.Status != "noop" {
				t.Fatalf("noop reconcile: %+v %v", reconciled, err)
			}
		}
	}
}

func TestGmailLabelUnsupportedDoesNotDispatch(t *testing.T) {
	for _, id := range []string{"INBOX", "UNREAD", "STARRED", "TRASH", "SPAM", "SENT", "DRAFT", "All Mail"} {
		w, p, r := labelWorkflowFixture(t)
		p.labels = append(p.labels, gmail.Label{ID: id, Name: id, Type: "system"})
		if id == "INBOX" {
			p.labels = p.labels[:len(p.labels)-1]
		}
		r.Label = id
		if _, err := w.Change(context.Background(), r); err == nil || p.modifies != 0 {
			t.Fatalf("%s dispatched or accepted", id)
		}
	}
	w, p, r := labelWorkflowFixture(t)
	r.Label = "One"
	if _, err := w.Change(context.Background(), r); err == nil || p.modifies != 0 {
		t.Fatal("duplicate leaf accepted")
	}
}

func TestGmailLabelIdentityFailsClosed(t *testing.T) {
	for _, kind := range []string{"missing RFC", "ambiguous provider", "missing provider", "from", "subject", "date", "duplicate header", "account"} {
		t.Run(kind, func(t *testing.T) {
			w, p, r := labelWorkflowFixture(t)
			switch kind {
			case "missing RFC":
				old := w.message
				w.message = func(ctx context.Context, r GmailLabelRequest) (*Message, error) {
					m, e := old(ctx, r)
					m.RFCMessageID = "1"
					return m, e
				}
			case "ambiguous provider":
				p.ids = append(p.ids, "provider-2")
			case "missing provider":
				p.ids = nil
			case "from":
				p.message.Payload.Headers[1].Value = "other@example.com"
			case "subject":
				p.message.Payload.Headers[2].Value = "other"
			case "date":
				p.message.Payload.Headers[3].Value = "Fri, 28 Aug 2026 12:00:01 +0000"
			case "duplicate header":
				p.message.Payload.Headers = append(p.message.Payload.Headers, p.message.Payload.Headers[0])
			case "account":
				p.email = "other@example.com"
			}
			if _, err := w.Change(context.Background(), r); err == nil || p.modifies != 0 {
				t.Fatal("unsafe identity accepted")
			}
		})
	}
}

func TestGmailLabelAcceptanceIsNotVerification(t *testing.T) {
	for _, kind := range []string{"persists", "unrelated removed", "unread added"} {
		t.Run(kind, func(t *testing.T) {
			w, p, r := labelWorkflowFixture(t)
			p.modify = func([]string, []string) error {
				switch kind {
				case "unrelated removed":
					p.message.LabelIDs = nil
				case "unread added":
					p.message.LabelIDs = []string{"Label_2", "UNREAD"}
				}
				return nil
			}
			out, err := w.Change(context.Background(), r)
			if err == nil || out.Status != "failed" || out.ProviderConfirmed || p.gets != 2 {
				t.Fatalf("accepted response treated as effect: %+v %v", out, err)
			}
		})
	}
}

func TestGmailLabelInterruptedMutationReadonlyReconcile(t *testing.T) {
	w, p, r := labelWorkflowFixture(t)
	p.modify = func([]string, []string) error {
		p.message.LabelIDs = []string{"Label_2"}
		p.readErr = errors.New("network unavailable")
		return errors.New("interrupted dispatch")
	}
	out, err := w.Change(context.Background(), r)
	if err == nil || out.Status != "unknown" || p.modifies != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	before, _ := os.ReadFile(p.journal)
	p.readErr = nil
	out, err = w.Reconcile(context.Background(), p.journal)
	if err != nil || out.Status != "changed_verified" || out.MailStatus != "verified" || p.modifies != 1 {
		t.Fatalf("reconcile %+v %v", out, err)
	}
	after, _ := os.ReadFile(p.journal)
	if string(before) != string(after) {
		t.Fatal("reconcile wrote original receipt")
	}
	// A truncated trailing event cannot destroy the durable dispatch baseline.
	f, err := os.OpenFile(p.journal, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"event":`)
	f.Close()
	if _, err := ReadGmailLabelReceipt(p.journal); err != nil {
		t.Fatal(err)
	}
}

func TestGmailLabelProviderConfirmedMailPending(t *testing.T) {
	w, p, r := labelWorkflowFixture(t)
	old := w.snapshot
	calls := 0
	w.snapshot = func(ctx context.Context, a string, id StableIdentity) (GmailLabelSnapshot, error) {
		calls++
		if calls > 1 {
			return GmailLabelSnapshot{}, errors.New("Mail sync pending")
		}
		return old(ctx, a, id)
	}
	out, err := w.Change(context.Background(), r)
	if err != nil || !out.ProviderConfirmed || out.MailStatus != "pending" || p.modifies != 1 {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestGmailLabelSubjectRFC2047AndServerDate(t *testing.T) {
	w, p, r := labelWorkflowFixture(t)
	p.message.Payload.Headers[2].Value = "=?UTF-8?B?c3ViamVjdA==?="
	if _, err := w.Change(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

func TestGmailLabelNoopReconcileRejectsStaleConfirmation(t *testing.T) {
	for _, kind := range []string{"changed", "unavailable", "account mismatch"} {
		t.Run(kind, func(t *testing.T) {
			w, p, r := labelWorkflowFixture(t)
			r.Add = true
			out, err := w.Change(context.Background(), r)
			if err != nil || out.Status != "noop" || !out.ProviderConfirmed {
				t.Fatalf("noop %+v %v", out, err)
			}
			switch kind {
			case "changed":
				p.message.LabelIDs = []string{"Label_2"}
			case "unavailable":
				p.readErr = errors.New("unavailable")
			case "account mismatch":
				p.email = "different@example.com"
			}
			out, err = w.Reconcile(context.Background(), p.journal)
			if err == nil || out.ProviderConfirmed || p.modifies != 0 {
				t.Fatalf("stale noop confirmation %+v %v", out, err)
			}
		})
	}
}

func TestGmailLabelNoopReportsStaleMailMembershipPending(t *testing.T) {
	w, p, r := labelWorkflowFixture(t)
	p.message.LabelIDs = []string{"Label_2"}
	w.snapshot = func(context.Context, string, StableIdentity) (GmailLabelSnapshot, error) {
		return GmailLabelSnapshot{LocalIDs: []string{"99"}, Memberships: []string{"All Mail", "Important", "Other/One"}, Read: true}, nil
	}
	out, err := w.Change(context.Background(), r)
	if err != nil || out.Status != "noop" || !out.ProviderConfirmed || out.MailStatus != "pending" || out.MailAfter == nil || !hasLabel(out.MailAfter.Memberships, "Important") || p.modifies != 0 {
		t.Fatalf("stale local noop %+v %v", out, err)
	}
}

func TestGmailLabelReceiptFailurePreventsDispatch(t *testing.T) {
	w, p, r := labelWorkflowFixture(t)
	if err := r.Receipt.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := w.Change(context.Background(), r)
	if err == nil || p.modifies != 0 || out.Status != "failed" || out.ProviderConfirmed {
		t.Fatalf("receipt failure allowed mutation: %+v %v", out, err)
	}
}

func TestGmailLabelAccountAddressesAliasFirst(t *testing.T) {
	addresses := gmailAccountAddresses(&Account{EmailAddress: "Alias <Alias@example.com>", EmailAddresses: []string{"ALIAS@example.com", "primary@example.com", "Primary <PRIMARY@EXAMPLE.COM>", "invalid"}})
	if len(addresses) != 2 || addresses[0] != "alias@example.com" || addresses[1] != "primary@example.com" {
		t.Fatalf("addresses: %+v", addresses)
	}
	c := NewClient()
	c.PrimeAccounts([]Account{{Name: "Work", EmailAddress: "alias@example.com", EmailAddresses: []string{"primary@example.com"}}})
	got, err := GmailAccountEmails(context.Background(), c, "Work")
	if err != nil || len(got) != 2 || got[1] != "primary@example.com" {
		t.Fatalf("public helper %+v %v", got, err)
	}
}

func gmailLabelSQLiteFixture(t *testing.T, data string) (*Client, string) {
	t.Helper()
	c, log := snapshotSQLiteFixture(t, data)
	// Resolve the real fixture before the query shim runs. The test must never
	// discover the user's Mail directory, including on hosts without FDA.
	t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", os.Getenv("SNAPSHOT_SQLITE_DATABASE"))
	return c, log
}

func TestGmailLabelRealSQLiteSnapshotMembershipsAndRegeneratedID(t *testing.T) {
	c, log := gmailLabelSQLiteFixture(t, `
insert into mailboxes(rowid,url) values (1,'imap://ABC/%5BGmail%5D/All%20Mail'), (2,'imap://ABC/%5BGmail%5D/Important'),(3,'imap://ABC/Projects/One'),(4,'imap://ABC/Other/One'),(5,'imap://DEF/%5BGmail%5D/All%20Mail');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (901,1,1,1,1787961600,42,1,0),(902,5,1,1,1787961600,42,0,0);
insert into labels values(901,2),(901,3),(901,4);
`)
	id := snapshotTestIdentity()
	id.RFCMessageID = "<real@example.com>"
	reads := 0
	got, err := c.gmailLabelSnapshotWithRead("Work", id, func(account, box, localID string) (*Message, error) {
		reads++
		if account != "Work" || box != "[Gmail]/All Mail" || localID != "901" {
			t.Fatalf("new local identity not used: %s %s %s", account, box, localID)
		}
		return &Message{RFCMessageID: strings.Trim(id.RFCMessageID, "<>"), Sender: id.Sender, Subject: id.Subject, DateSent: id.DateSent, MessageSize: id.MessageSize}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Read || reads != 1 || len(got.LocalIDs) != 1 || got.LocalIDs[0] != "901" || !sameLabels(got.Memberships, []string{"All Mail", "Important", "Projects/One", "Other/One"}) {
		t.Fatalf("snapshot: %+v", got)
	}
	query, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(query), "select m.ROWID as ID") != 1 {
		t.Fatalf("membership snapshot not one SQL statement: %s", query)
	}
}

func TestGmailLabelSQLiteSnapshotAmbiguityAndHeaderMismatch(t *testing.T) {
	for _, duplicate := range []bool{false, true} {
		t.Run(map[bool]string{false: "header mismatch", true: "ambiguous rows"}[duplicate], func(t *testing.T) {
			data := `
insert into mailboxes(rowid,url) values(1,'imap://ABC/%5BGmail%5D/All%20Mail');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values(901,1,1,1,1787961600,42,1,0);
`
			if duplicate {
				data += `insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values(902,1,1,1,1787961600,42,1,0);`
			}
			c, _ := gmailLabelSQLiteFixture(t, data)
			id := snapshotTestIdentity()
			id.RFCMessageID = "<real@example.com>"
			_, err := c.gmailLabelSnapshotWithRead("Work", id, func(string, string, string) (*Message, error) {
				return &Message{RFCMessageID: "<different@example.com>", Sender: id.Sender, Subject: id.Subject, DateSent: id.DateSent, MessageSize: id.MessageSize}, nil
			})
			if err == nil {
				t.Fatal("ambiguous/mismatched Mail identity accepted")
			}
		})
	}
}

func TestGmailLabelBareMailMessageID(t *testing.T) {
	w, _, r := labelWorkflowFixture(t)
	old := w.message
	w.message = func(ctx context.Context, r GmailLabelRequest) (*Message, error) {
		m, err := old(ctx, r)
		m.RFCMessageID = "real@example.com"
		return m, err
	}
	out, err := w.Change(context.Background(), r)
	if err != nil || out.Status != "changed_verified" || out.Identity.RFCMessageID != "<real@example.com>" {
		t.Fatalf("Mail bare message-id was not canonicalized: %+v %v", out, err)
	}
	for _, invalid := range []string{"262225", "<262225>", "Name <real@example.com>", "real@example.com\r\nInjected: true", "<real@example.com", "real@example.com>", "<real@ex ample.com>", "<real@example.com><other@example.com>"} {
		if gmailRFCMessageID(invalid) != "" {
			t.Fatalf("accepted invalid RFC identity %q", invalid)
		}
	}
	if got := validRFCMessageID("real@example.com"); got != "<real@example.com>" {
		t.Fatalf("Mail bare identity must share validated canonical form: %q", got)
	}
	for _, invalid := range []string{"262225", "<262225>", "Name <real@example.com>", "real@example.com\r\nInjected: true"} {
		if validRFCMessageID(invalid) != "" {
			t.Fatalf("global identity accepted invalid Mail ID %q", invalid)
		}
	}
}

func TestGmailLabelCaptureIndexedSenderWithIndependentMailGuard(t *testing.T) {
	c, _ := gmailLabelSQLiteFixture(t, `
alter table messages add column date_received integer;
alter table messages add column flagged integer default 0;
update addresses set address='sender@EXAMPLE.COM',comment='Index Name' where rowid=1;
insert into mailboxes(rowid,url) values (1,'imap://ABC/%5BGmail%5D/All%20Mail'),(2,'imap://ABC/%5BGmail%5D/Important');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (901,1,1,1,1787961600,42,1,0);
insert into labels values(901,2);
`)
	r := GmailLabelRequest{Account: "Work", Mailbox: "Important", MessageID: "901"}
	live := Message{ID: "901", RFCMessageID: "real@example.com", Sender: "Mail Display <SENDER@example.com>", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42, Read: true}
	read := func(string, string, string) (*Message, error) { copy := live; return &copy, nil }
	source, err := c.gmailLabelMessageWithRead(r, read)
	if err != nil {
		t.Fatal(err)
	}
	if source.Sender != "Index Name <sender@EXAMPLE.COM>" || source.RFCMessageID != "<real@example.com>" {
		t.Fatalf("indexed identity not retained: %+v", source)
	}
	id := gmailLabelIdentity(*source)
	snapshot, err := c.gmailLabelSnapshotWithRead("Work", id, read)
	if err != nil || !snapshot.Read || snapshot.LocalIDs[0] != "901" || !hasLabel(snapshot.Memberships, "Important") {
		t.Fatalf("semantic sender guard blocked exact indexed snapshot %+v %v", snapshot, err)
	}
	live.Sender = "Other Display <different@example.com>"
	if _, err := c.gmailLabelMessageWithRead(r, read); err == nil {
		t.Fatal("different mailbox address accepted during capture")
	}
	if _, err := c.gmailLabelSnapshotWithRead("Work", id, read); err == nil {
		t.Fatal("different mailbox address accepted during independent snapshot read")
	}
}
