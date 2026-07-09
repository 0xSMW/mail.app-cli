package cmd

import "testing"

func TestRecentMailboxCandidatesDedupesArchiveAliases(t *testing.T) {
	got := recentMailboxCandidates("Archive")
	want := []string{"Archive", "All Mail", "INBOX"}
	if len(got) != len(want) {
		t.Fatalf("recentMailboxCandidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recentMailboxCandidates = %v, want %v", got, want)
		}
	}
}
