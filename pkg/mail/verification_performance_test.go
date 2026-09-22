package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStateVerificationRetriesOnlyUnresolvedIDs(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "queries")
	t.Setenv("VERIFY_QUERY_LOG", log)
	script := `#!/bin/sh
case "$4" in
 *"m.ROWID in (1, 2)"*)
   printf 'first\n' >> "$VERIFY_QUERY_LOG"
   printf '[{"ID":1,"Read":1},{"ID":2,"Read":0}]\n' ;;
 *"m.ROWID in (2)"*)
   printf 'pending\n' >> "$VERIFY_QUERY_LOG"
   printf '[{"ID":2,"Read":1}]\n' ;;
 *"from mailboxes"*) printf '[{"ID":9,"URL":"imap://ABC/INBOX","TotalCount":2,"UnreadCount":1}]\n' ;;
 *) printf 'unexpected query' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "sqlite3"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	previous := verificationBackoff
	verificationBackoff = []time.Duration{0, 0, 0, 0, 0, 0}
	t.Cleanup(func() { verificationBackoff = previous })
	client := NewClient()
	client.PrimeAccounts([]Account{{ID: "ABC", Name: "Work"}})
	result := VerifyMutations(client, BatchOptions{Action: "mark", Read: true}, []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX", Status: "succeeded"}, {ID: "2", Account: "Work", SourceMailbox: "INBOX", Status: "succeeded"}})
	if result[0].VerifyStatus != "matched" || result[1].VerifyStatus != "matched" {
		t.Fatalf("result = %+v", result)
	}
	data, err := os.ReadFile(log)
	if err != nil || strings.TrimSpace(string(data)) != "first\npending" {
		t.Fatalf("queries = %q, %v; completed IDs must not be re-read", data, err)
	}
}
