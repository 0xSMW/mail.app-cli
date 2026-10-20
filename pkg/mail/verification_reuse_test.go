package mail

import (
	"reflect"
	"testing"
)

func TestVerificationSnapshotIdentityReusePreservesRowsAndScope(t *testing.T) {
	identity := snapshotTestIdentity()
	scope := snapshotScope("Work", "INBOX")
	scope.identities = []StableIdentity{identity, identity}
	rows := []Message{
		{ID: "205", Sender: " sender@example.com ", Subject: " subject ", DateSent: "2026-08-29T07:00:00+07:00", MessageSize: 42, Account: "old", Mailbox: "old"},
		{ID: "203", Sender: identity.Sender, Subject: identity.Subject, DateSent: identity.DateSent, MessageSize: 43},
		{ID: "205", Sender: identity.Sender, Subject: identity.Subject, DateSent: identity.DateSent, MessageSize: 42, Read: true},
	}
	original := append([]Message(nil), rows...)
	matches, err := NewClient().matchVerificationSnapshot(scope, "imap://ABC/Parent/INBOX", rows)
	if err != nil {
		t.Fatal(err)
	}
	want := []Message{rows[0], rows[2]}
	for i := range want {
		want[i].Account, want[i].Mailbox = "Work", "INBOX"
	}
	if len(matches) != 1 || !reflect.DeepEqual(matches[identity.verificationKey()], want) {
		t.Fatalf("matches=%#v; want ordered duplicates with their original read states %#v", matches, want)
	}
	if !reflect.DeepEqual(rows, original) {
		t.Fatal("snapshot matching mutated the source rows")
	}
}

func TestVerificationSnapshotInvalidRowPrecedesRequestedIdentityError(t *testing.T) {
	scope := snapshotScope("Work", "INBOX")
	scope.identities[0].RFCMessageID = "stable@example.com"
	scope.identities[0].Sender = "invalid sender"
	for _, row := range []Message{
		{ID: "0", Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42},
		{ID: "1", Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z"},
	} {
		matches, err := NewClient().matchVerificationSnapshot(scope, "imap://ABC/INBOX", []Message{row})
		if matches != nil || err == nil || err.Error() != "verification snapshot message lacks indexed identity" {
			t.Fatalf("row=%#v: matches=%#v error=%v", row, matches, err)
		}
	}
}
