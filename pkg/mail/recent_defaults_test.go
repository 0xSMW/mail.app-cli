package mail

import "testing"

func TestRecentEnvelopeDefaultsAndBooleanUpdates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	initial := Message{ID: "42", Account: "Work", Mailbox: "INBOX", Subject: "Original", Sender: "sender@example.com", DateReceived: "received", DateSent: "sent", MessageSize: 7, Read: true, Flagged: true, Deleted: true}
	if err := RecordRecentMessage(initial, " "); err != nil {
		t.Fatal(err)
	}
	if err := RecordRecentMessage(Message{ID: "42", Account: "Work", Mailbox: "Archive"}, "move"); err != nil {
		t.Fatal(err)
	}
	entries, err := loadRecentMessages()
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	got := entries[0]
	if got.Mailbox != "Archive" || got.OriginalMailbox != "INBOX" || got.Subject != initial.Subject || got.Sender != initial.Sender || got.DateReceived != initial.DateReceived || got.DateSent != initial.DateSent || got.MessageSize != 7 || !got.Read || !got.Flagged || !got.Deleted || got.LastAction != "move" || got.LastSeenAt == "" {
		t.Fatalf("skeletal update = %+v", got)
	}
	// A fresh envelope supplies authoritative false booleans while keeping the
	// same independent fallback rules for strings, size, and original mailbox.
	if err := RecordRecentMessage(Message{ID: "42", Account: "Work", Subject: "Fresh", MessageSize: -1}, " "); err != nil {
		t.Fatal(err)
	}
	entries, err = loadRecentMessages()
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	got = entries[0]
	if got.Mailbox != "Archive" || got.OriginalMailbox != "INBOX" || got.Subject != "Fresh" || got.Sender != initial.Sender || got.DateReceived != initial.DateReceived || got.DateSent != initial.DateSent || got.MessageSize != -1 || got.Read || got.Flagged || got.Deleted || got.LastAction != "seen" {
		t.Fatalf("fresh envelope update = %+v", got)
	}
}
