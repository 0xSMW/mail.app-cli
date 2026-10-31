package mail

import "testing"

func TestMailboxLeafFromURLPreservesUnicodeSpelling(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"Entwu%CC%88rfe", "Entwu\u0308rfe"},
		{"Entw%C3%BCrfe", "Entwürfe"},
		{"Caf%C3%A9/Entwu%CC%88rfe", "Entwu\u0308rfe"},
	} {
		if got := mailboxLeafFromURL("imap://00000000-0000-0000-0000-000000000000/" + tc.path); got != tc.want {
			t.Fatalf("mailboxLeafFromURL(%q) = %q, want literal spelling %q", tc.path, got, tc.want)
		}
	}
}
