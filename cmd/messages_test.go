package cmd

import "testing"

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
