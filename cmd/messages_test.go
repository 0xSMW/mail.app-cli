package cmd

import (
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
