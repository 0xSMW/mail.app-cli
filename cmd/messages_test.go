package cmd

import (
	"encoding/json"
	"testing"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
)

func TestScopedUnifiedFilters(t *testing.T) {
	tests := []struct {
		mailboxType string
		unreadOnly  bool
		flaggedOnly bool
	}{
		{mailboxType: "inbox"},
		{mailboxType: "unread", unreadOnly: true},
		{mailboxType: "flagged", flaggedOnly: true},
		{mailboxType: "sent"},
	}

	for _, tt := range tests {
		gotUnread, gotFlagged := scopedUnifiedFilters(tt.mailboxType)
		if gotUnread != tt.unreadOnly || gotFlagged != tt.flaggedOnly {
			t.Fatalf("scopedUnifiedFilters(%q) = (%v, %v), want (%v, %v)",
				tt.mailboxType, gotUnread, gotFlagged, tt.unreadOnly, tt.flaggedOnly)
		}
	}
}

func TestUniqueStringsPreservesOrder(t *testing.T) {
	got := uniqueStrings([]string{"3", "1", "3", "2", "1"})
	want := []string{"3", "1", "2"}
	if len(got) != len(want) {
		t.Fatalf("uniqueStrings length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uniqueStrings = %v, want %v", got, want)
		}
	}
}

func TestNormalizeThreadSubject(t *testing.T) {
	tests := map[string]string{
		"Re: Invoice":       "invoice",
		"Fwd: RE:  Invoice": "invoice",
		"  Project   Plan ": "project plan",
		"":                  "",
	}
	for input, want := range tests {
		if got := normalizeThreadSubject(input); got != want {
			t.Fatalf("normalizeThreadSubject(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestThreadArchiveAllowed(t *testing.T) {
	tests := []struct {
		name   string
		thread threadSummary
		want   bool
	}{
		{name: "subject only group", thread: threadSummary{ID: "invoice", Synthetic: true, Count: 2}, want: false},
		{name: "single synthetic message", thread: threadSummary{ID: "invoice", Synthetic: true, Count: 1}, want: true},
		{name: "non synthetic group", thread: threadSummary{ID: "message-1", Count: 2}, want: true},
	}
	for _, tt := range tests {
		if got := threadArchiveAllowed(tt.thread); got != tt.want {
			t.Fatalf("%s: threadArchiveAllowed = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestDeterministicAttachmentNameHandlesCollisions(t *testing.T) {
	used := map[string]int{}
	message := mail.Message{ID: "42", DateReceived: "2026-06-28T10:00:00Z"}
	first := deterministicAttachmentName(message, "my file.pdf", used)
	second := deterministicAttachmentName(message, "my file.pdf", used)
	if first != "2026-06-28-42-my-file.pdf" {
		t.Fatalf("first attachment name = %q", first)
	}
	if second != "2026-06-28-42-my-file-2.pdf" {
		t.Fatalf("second attachment name = %q", second)
	}
}

func TestAttachmentExportFailed(t *testing.T) {
	if !attachmentExportFailed(1) {
		t.Fatal("attachmentExportFailed(1) = false")
	}
	if attachmentExportFailed(0) {
		t.Fatal("attachmentExportFailed(0) = true")
	}
}

func TestParseImportMessagesAcceptsWrapperAndDirectArray(t *testing.T) {
	direct, err := json.Marshal([]mail.Message{{ID: "1", Subject: "Direct"}})
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := json.Marshal(map[string][]mail.Message{
		"messages": {{ID: "2", Subject: "Wrapped"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	directMessages, err := parseImportMessages(direct)
	if err != nil {
		t.Fatalf("parseImportMessages direct array returned error: %v", err)
	}
	if len(directMessages) != 1 || directMessages[0].ID != "1" {
		t.Fatalf("direct messages = %+v", directMessages)
	}

	wrappedMessages, err := parseImportMessages(wrapped)
	if err != nil {
		t.Fatalf("parseImportMessages wrapper returned error: %v", err)
	}
	if len(wrappedMessages) != 1 || wrappedMessages[0].ID != "2" {
		t.Fatalf("wrapped messages = %+v", wrappedMessages)
	}
}

func TestParseImportMessagesRejectsMissingMessages(t *testing.T) {
	if _, err := parseImportMessages([]byte(`{"items":[]}`)); err == nil {
		t.Fatal("parseImportMessages accepted object without messages")
	}
}
