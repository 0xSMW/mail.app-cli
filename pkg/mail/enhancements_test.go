package mail

import (
	"testing"
	"time"
)

func TestShouldDeleteDraftAutosaveSkipsReplacementAndUnrelatedDrafts(t *testing.T) {
	since := time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)
	target := draftCleanupTarget{
		Account: "iCloud",
		Subject: "Review",
		Content: "Original body",
		KeepIDs: map[string]bool{"replacement": true},
	}

	tests := []struct {
		name    string
		message Message
		want    bool
	}{
		{
			name: "autosave copy",
			message: Message{
				ID: "copy", Account: "iCloud", Subject: "Review",
				Content: "\nOriginal body\n", DateReceived: "2026-06-28T15:00:02Z",
			},
			want: true,
		},
		{
			name: "replacement draft id",
			message: Message{
				ID: "replacement", Account: "iCloud", Subject: "Review",
				Content: "Updated body", DateReceived: "2026-06-28T15:00:03Z",
			},
			want: false,
		},
		{
			name: "same subject unrelated content",
			message: Message{
				ID: "other", Account: "iCloud", Subject: "Review",
				Content: "Different body", DateReceived: "2026-06-28T15:00:04Z",
			},
			want: false,
		},
		{
			name: "older same subject and content",
			message: Message{
				ID: "old", Account: "iCloud", Subject: "Review",
				Content: "Original body", DateReceived: "2026-06-28T14:59:00Z",
			},
			want: false,
		},
	}

	for _, tt := range tests {
		if got := shouldDeleteDraftAutosave(tt.message, target, since); got != tt.want {
			t.Fatalf("%s: shouldDeleteDraftAutosave = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestNormalizeDraftContent(t *testing.T) {
	got := normalizeDraftContent("\r\nBody\r\n")
	if got != "Body" {
		t.Fatalf("normalizeDraftContent = %q, want Body", got)
	}
}

func TestShouldDeleteDraftAutosaveRequiresMatchingContent(t *testing.T) {
	since := time.Date(2026, 6, 28, 15, 0, 0, 0, time.UTC)
	target := draftCleanupTarget{
		Account: "iCloud",
		Subject: "Same subject",
		Content: "Body A",
	}
	message := Message{
		ID: "other", Account: "iCloud", Subject: "Same subject",
		Content: "Body B", DateReceived: "2026-06-28T15:00:02Z",
	}
	if shouldDeleteDraftAutosave(message, target, since) {
		t.Fatal("cleanup matched same-subject draft with different content")
	}
}
