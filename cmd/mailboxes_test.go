package cmd

import (
	"strings"
	"testing"
)

func TestMailboxCacheKeyDistinguishesPunctuation(t *testing.T) {
	withSlash := mailboxCacheKey("Work/Test")
	withHyphen := mailboxCacheKey("Work-Test")

	if withSlash == withHyphen {
		t.Fatalf("mailbox cache keys collided: %q", withSlash)
	}
	if strings.Contains(withSlash, "/") || strings.Contains(withHyphen, "/") {
		t.Fatalf("mailbox cache key must be filename-safe: %q, %q", withSlash, withHyphen)
	}
}

func TestMailboxCacheKeyIsDeterministicAndVersioned(t *testing.T) {
	const account = "Work/Test"
	want := "mailboxes-account-v2-V29yay9UZXN0"

	if got := mailboxCacheKey(account); got != want {
		t.Fatalf("mailboxCacheKey(%q) = %q, want %q", account, got, want)
	}
	if got := mailboxCacheKey(account); got != want {
		t.Fatalf("mailboxCacheKey(%q) changed between calls: %q, want %q", account, got, want)
	}
}
