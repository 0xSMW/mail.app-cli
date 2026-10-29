package mail

import "testing"

func TestGmailSnapshotMailboxPreservesUnicodePath(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"Entwu%CC%88rfe", "Entwu\u0308rfe"},
		{"Entw%C3%BCrfe", "Entwürfe"},
		{"Caf%C3%A9/Entwu%CC%88rfe", "Café/Entwu\u0308rfe"},
	} {
		if got := gmailSnapshotMailbox("imap://00000000-0000-0000-0000-000000000000/" + tc.path); got != tc.want {
			t.Fatalf("gmailSnapshotMailbox(%q) = %q, want literal path %q", tc.path, got, tc.want)
		}
	}
}
