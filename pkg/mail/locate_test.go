package mail

import "testing"

func TestPreferredMessageMailbox(t *testing.T) {
	tests := []struct {
		name    string
		backing string
		labels  []string
		want    string
	}{
		{name: "gmail inbox label wins", backing: "All Mail", labels: []string{"Newsletter", "INBOX"}, want: "INBOX"},
		{name: "gmail label beats all mail", backing: "All Mail", labels: []string{"Receipts"}, want: "Receipts"},
		{name: "archived gmail message stays in all mail", backing: "All Mail", labels: nil, want: "All Mail"},
		{name: "plain imap mailbox", backing: "Sent Messages", labels: nil, want: "Sent Messages"},
		{name: "imap inbox", backing: "INBOX", labels: nil, want: "INBOX"},
	}
	for _, tt := range tests {
		if got := preferredMessageMailbox(tt.backing, tt.labels); got != tt.want {
			t.Fatalf("%s: preferredMessageMailbox = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestArchiveSourceMailbox(t *testing.T) {
	if got := archiveSourceMailbox("All Mail", []string{"Receipts", "INBOX"}); got != "INBOX" {
		t.Fatalf("archive source = %q, want INBOX", got)
	}
	if got := archiveSourceMailbox("All Mail", []string{"Receipts"}); got != "All Mail" {
		t.Fatalf("archive source with user label = %q, want All Mail", got)
	}
}

func TestIsGmailSystemLabelURL(t *testing.T) {
	if !isGmailSystemLabelURL("imap://ABC/%5BGmail%5D/Important") {
		t.Fatal("Important should be a system label")
	}
	if isGmailSystemLabelURL("imap://ABC/INBOX") || isGmailSystemLabelURL("imap://ABC/Receipts") {
		t.Fatal("user mailboxes should not be system labels")
	}
}

func TestIndexMailboxAccountID(t *testing.T) {
	if got := indexMailboxAccountID("imap://ABC-123/INBOX"); got != "ABC-123" {
		t.Fatalf("account id = %q", got)
	}
	if got := indexMailboxAccountID("ews://X/Folder/Child"); got != "X" {
		t.Fatalf("ews account id = %q", got)
	}
	if got := indexMailboxDisplayName("imap://ABC/%5BGmail%5D/All%20Mail"); got != "All Mail" {
		t.Fatalf("all mail name = %q", got)
	}
	if got := indexMailboxDisplayName("imap://ABC/Projects/Client%20A"); got != "Client A" {
		t.Fatalf("leaf name = %q", got)
	}
}

func TestNotFoundErrorClassification(t *testing.T) {
	err := notFound("message", "42")
	if !IsNotFound(err) {
		t.Fatal("typed not-found error was not recognised")
	}
	if err.Error() != "message not found: 42" {
		t.Fatalf("message = %q", err.Error())
	}
	if !IsNotFound(errString("Error: Message not found")) {
		t.Fatal("bridge string was not recognised")
	}
	if IsNotFound(errString("jxa timed out")) {
		t.Fatal("unrelated error was classified as not found")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
