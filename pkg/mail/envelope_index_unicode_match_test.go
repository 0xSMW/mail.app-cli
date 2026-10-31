package mail

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/mailindex"
)

// SQL lookups retain literal spelling and also support the composed and
// decomposed forms commonly reported by IMAP servers.
func TestEscapeMailboxURLPathFormsCoversBothNormalizations(t *testing.T) {
	const nfc = "Entwürfe"                  // precomposed, as a caller would type it
	const wantNFCEscaped = "Entw%C3%BCrfe"  // percent-encoded NFC
	const wantNFDEscaped = "Entwu%CC%88rfe" // percent-encoded NFD, as observed in a real index

	got := escapeMailboxURLPathForms(nfc)

	if len(got) != 2 {
		t.Fatalf("escapeMailboxURLPathForms(%q) = %v, want 2 forms (NFC and NFD differ for this name)", nfc, got)
	}
	foundNFC, foundNFD := false, false
	for _, form := range got {
		if form == wantNFCEscaped {
			foundNFC = true
		}
		if form == wantNFDEscaped {
			foundNFD = true
		}
	}
	if !foundNFC || !foundNFD {
		t.Fatalf("escapeMailboxURLPathForms(%q) = %v, want both %q and %q", nfc, got, wantNFCEscaped, wantNFDEscaped)
	}
}

// TestEscapeMailboxURLPathFormsCollapsesForASCII keeps the common case (no
// combining marks) to a single comparison, so the exact/broad SQL
// conditions stay exactly as they were before this existed.
func TestEscapeMailboxURLPathFormsCollapsesForASCII(t *testing.T) {
	got := escapeMailboxURLPathForms("INBOX")
	if len(got) != 1 || got[0] != "INBOX" {
		t.Fatalf("escapeMailboxURLPathForms(\"INBOX\") = %v, want a single form [\"INBOX\"]", got)
	}
}

func TestIndexMailboxUnicodeResolutionAndSnapshotParity(t *testing.T) {
	const nfc = "Entw\u00fcrfe"
	const nfd = "Entwu\u0308rfe"
	const mixedPath = "caf\u00e9/re\u0301sume\u0301"
	const mixedLeaf = "caf\u00e9 re\u0301sume\u0301"
	url := func(path string) string { return "imap://ABC/" + escapeMailboxURLPath(path) }
	tests := []struct {
		name, mailbox string
		urls          []string
		want          int
		ambiguous     bool
	}{
		{name: "NFC request NFD root", mailbox: nfc, urls: []string{url(nfd)}, want: 1},
		{name: "NFD request NFC root", mailbox: nfd, urls: []string{url(nfc)}, want: 1},
		{name: "NFC request NFD nested leaf", mailbox: nfc, urls: []string{url("Parent/" + nfd)}, want: 1},
		{name: "NFD request NFC nested leaf", mailbox: nfd, urls: []string{url("Parent/" + nfc)}, want: 1},
		{name: "NFC request NFD full path", mailbox: "caf\u00e9/" + nfc, urls: []string{url("cafe\u0301/" + nfd)}, want: 1},
		{name: "NFD request NFC full path", mailbox: "cafe\u0301/" + nfd, urls: []string{url("caf\u00e9/" + nfc)}, want: 1},
		{name: "original mixed path", mailbox: mixedPath, urls: []string{url(mixedPath)}, want: 1},
		{name: "original mixed component", mailbox: mixedLeaf, urls: []string{url(mixedLeaf)}, want: 1},
		{name: "original mixed nested fallback", mailbox: mixedPath, urls: []string{url("Parent/" + mixedPath)}, want: 1},
		{name: "raw NFC exact wins over lower rowid", mailbox: nfc, urls: []string{url(nfd), url(nfc)}, want: 2},
		{name: "raw NFD exact wins over lower rowid", mailbox: nfd, urls: []string{url(nfc), url(nfd)}, want: 2},
		{name: "normalized exact before raw broad", mailbox: nfc, urls: []string{url("Parent/" + nfc), url(nfd)}, want: 2},
		{name: "raw broad before normalized broad", mailbox: nfc, urls: []string{url("Parent/" + nfd), url("Other/" + nfc)}, want: 2},
		{name: "raw broad keeps rowid tie policy", mailbox: nfc, urls: []string{url("First/" + nfc), url("Second/" + nfc)}, want: 1},
		{name: "normalized exact ambiguity", mailbox: mixedLeaf, urls: []string{url("caf\u00e9 r\u00e9sum\u00e9"), url("cafe\u0301 re\u0301sume\u0301")}, ambiguous: true},
		{name: "normalized broad ambiguity", mailbox: nfc, urls: []string{url("First/" + nfd), url("Second/" + nfd)}, ambiguous: true},
		{name: "normalized exact wins over ambiguous broad", mailbox: nfc, urls: []string{url("First/" + nfd), url("Second/" + nfd), url(nfd)}, want: 3},
		{name: "other accounts excluded", mailbox: nfc, urls: []string{"imap://DEF/" + escapeMailboxURLPath(nfd), "imap://ABCD/" + escapeMailboxURLPath(nfd), url(nfd)}, want: 3},
		{name: "only other accounts remains absent", mailbox: nfc, urls: []string{"imap://DEF/" + escapeMailboxURLPath(nfd)}},
		{name: "percent and underscore are literal", mailbox: nfc + "_%", urls: []string{url("Parent/" + nfd + "X%"), url("Parent/" + nfd + "_X"), url("Parent/" + nfd + "_%")}, want: 3},
		{name: "wildcard lookalikes remain absent", mailbox: nfc + "_%", urls: []string{url("Parent/" + nfd + "X%"), url("Parent/" + nfd + "_X")}},
	}
	for _, backend := range []string{BackendLegacy, BackendNative} {
		t.Run(backend, func(t *testing.T) {
			if backend == BackendNative && !mailindex.Available() {
				t.Skip("embedded SQLite unavailable")
			}
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					t.Setenv("MAIL_APP_CLI_BACKEND", backend)
					var data strings.Builder
					data.WriteString("alter table mailboxes add column total_count integer default 0; alter table mailboxes add column unread_count integer default 0;\n")
					for i, raw := range tc.urls {
						fmt.Fprintf(&data, "insert into mailboxes(rowid,url,total_count,unread_count) values(%d,%s,%d,%d);\n", i+1, sqlQuote(raw), i+11, i+1)
						fmt.Fprintf(&data, "insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values(%d,%d,1,1,1787961600,42,1,0);\n", i+101, i+1)
					}
					client, _ := snapshotSQLiteFixture(t, data.String())
					// Both engines are confined to the same synthetic database, including
					// the native engine which bypasses the subprocess fixture shim.
					t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", os.Getenv("SNAPSHOT_SQLITE_DATABASE"))
					box, ok, err := client.resolveIndexMailbox("Work", tc.mailbox)
					scope := snapshotScope("Work", tc.mailbox)
					results := client.getIdentitySnapshotForVerification([]identityVerificationScope{scope})
					if tc.ambiguous {
						if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambig") || box != nil || ok {
							t.Errorf("ambiguous resolver must fail closed: box=%+v ok=%v error=%v", box, ok, err)
						}
						result, exists := results[scope.key]
						if !exists || result.err == nil || !strings.Contains(strings.ToLower(result.err.Error()), "ambig") || len(result.matches) != 0 {
							t.Errorf("ambiguous snapshot must fail closed: exists=%v result=%+v", exists, result)
						}
						return
					}
					if err != nil || ok != (tc.want != 0) {
						t.Fatalf("resolver box=%+v ok=%v error=%v; want row %d", box, ok, err, tc.want)
					}
					if tc.want != 0 && (box == nil || box.ID != tc.want || box.URL != tc.urls[tc.want-1] || box.TotalCount != tc.want+10 || box.UnreadCount != tc.want) {
						t.Fatalf("resolver identity/counts=%+v; want row %d", box, tc.want)
					}
					if tc.want == 0 {
						result, exists := results[scope.key]
						if !exists || result.err == nil || len(result.matches) != 0 || box != nil {
							t.Fatalf("absent scope must remain unavailable: box=%+v exists=%v snapshot=%+v", box, exists, result)
						}
						return
					}
					got := snapshotMessages(t, results, scope)
					if len(got) != 1 || got[0].ID != fmt.Sprint(tc.want+100) || got[0].Account != "Work" || !got[0].Read {
						t.Fatalf("snapshot must preserve resolver identity and metadata: %+v; want message %d", got, tc.want+100)
					}
				})
			}
		})
	}
}
