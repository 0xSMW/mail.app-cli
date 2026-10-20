package mail

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func installSearchCoverageBridge(t *testing.T, config map[string]any) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for production JXA search coverage tests")
	}
	mock, err := filepath.Abs("testdata/search_coverage_mock.js")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEARCH_COVERAGE_CONFIG", string(raw))
	t.Setenv("HOME", t.TempDir())
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	writeFakeOsaScript(t, "exec "+quote(node)+" "+quote(mock)+" \"$@\"")
}

func TestSearchJXACoverage(t *testing.T) {
	tests := []struct {
		name      string
		config    map[string]any
		mailbox   string
		limit     int
		since     string
		count     int
		truncated bool
		failures  int
	}{
		{name: "scan cutoff retains observed match", config: map[string]any{"count": 501, "messages": []any{map[string]any{"subject": "invoice"}}}, limit: 50, count: 1, truncated: true},
		{name: "Exchange null inbox accessor uses listed Inbox", config: map[string]any{"nullInbox": true, "mailbox": "Inbox", "messages": []any{map[string]any{"subject": "invoice"}}}, limit: 50, count: 1},
		{name: "output limit is success", config: map[string]any{"count": 501, "messages": []any{map[string]any{"subject": "invoice"}}}, limit: 1, count: 1},
		{name: "content required failure retains other match", config: map[string]any{"count": 2, "messages": []any{map[string]any{"subject": "invoice"}, map[string]any{"fail": []string{"content"}}}}, limit: 50, count: 1, failures: 1},
		{name: "subject failure with unresolved match", config: map[string]any{"messages": []any{map[string]any{"fail": []string{"subject"}}}}, limit: 50, failures: 1},
		{name: "known sender match avoids irrelevant failures", config: map[string]any{"messages": []any{map[string]any{"sender": "invoice", "fail": []string{"subject", "content", "dateSent", "read", "flagged", "messageSize"}}}}, limit: 50, count: 1},
		{name: "matching result needs identifier", config: map[string]any{"messages": []any{map[string]any{"subject": "invoice", "fail": []string{"id"}}}}, limit: 50, failures: 1},
		{name: "date filter needs date", config: map[string]any{"messages": []any{map[string]any{"subject": "invoice", "fail": []string{"dateReceived"}}}}, limit: 50, since: "2026-01-01", failures: 1},
		{name: "date filter needs non-null date", config: map[string]any{"messages": []any{map[string]any{"subject": "invoice", "dateReceived": nil}}}, limit: 50, since: "2026-01-01", failures: 1},
		{name: "irrelevant date failure is harmless", config: map[string]any{"messages": []any{map[string]any{"fail": []string{"dateReceived"}}}}, limit: 50},
		{name: "body only archive match", config: map[string]any{"mailbox": "Archive", "messages": []any{map[string]any{"content": "invoice"}}}, mailbox: "Archive", limit: 50, count: 1},
		{name: "archive cutoff cannot become whose success", config: map[string]any{"count": 10001, "mailbox": "Archive"}, mailbox: "Archive", limit: 50, truncated: true},
		{name: "archive read failure cannot become whose success", config: map[string]any{"mailbox": "Archive", "messages": []any{map[string]any{"fail": []string{"content"}}}}, mailbox: "Archive", limit: 50, failures: 1},
		{name: "unlimited still obeys scan budget", config: map[string]any{"count": 501}, limit: -1, truncated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installSearchCoverageBridge(t, tt.config)
			mailbox := tt.mailbox
			if mailbox == "" {
				mailbox = "INBOX"
			}
			messages, err := NewClient().searchMessagesInSingleMailboxJXA("invoice", "Work", mailbox, tt.limit, tt.since)
			if len(messages) != tt.count {
				t.Fatalf("messages=%v want %d", messages, tt.count)
			}
			var coverage *mailboxSearchCoverageError
			if tt.truncated || tt.failures > 0 {
				if !errors.As(err, &coverage) {
					t.Fatalf("error=%v want coverage", err)
				}
				if coverage.Truncated != tt.truncated || coverage.ReadFailures != tt.failures {
					t.Fatalf("coverage=%+v", coverage)
				}
				target := searchTarget{"Work", mailbox}
				result := collectSearchResults([]searchTarget{target}, []mailboxSearchResult{{target: target, messages: messages, err: err}}, tt.limit)
				if result.Complete || len(result.Messages) != tt.count || result.FailedMailboxes[0].Truncated != tt.truncated || result.FailedMailboxes[0].ReadFailures != tt.failures {
					t.Fatalf("result=%+v", result)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExplicitMailboxSearchCoverageOptions(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "fail closed", true: "allow partial"}[allow], func(t *testing.T) {
			installSearchCoverageBridge(t, map[string]any{"count": 501, "messages": []any{map[string]any{"subject": "invoice"}}})
			client := NewClient()
			client.shared.accounts = []Account{{Name: "Work", ID: "synthetic"}}
			client.shared.accountsLoaded = true
			result, err := client.SearchMessagesJSONSinceWithOptions("invoice", "Work", "INBOX", 50, "", SearchOptions{AllowPartial: allow})
			if result.Complete || len(result.Messages) != 1 || len(result.FailedMailboxes) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			var partial *PartialSearchError
			if allow {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.As(err, &partial) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestAccountSearchTargetsKeepUnknownCountsAndSingleInbox(t *testing.T) {
	for _, boxes := range [][]Mailbox{
		{{Name: "INBOX", Account: "Work", TotalCount: 1}},
		{{Name: "Archive", Account: "Work", TotalCount: 2}, {Name: "INBOX", Account: "Work", TotalCount: 1}, {Name: "Empty", Account: "Work"}, {Name: "Unknown", Account: "Work", TotalCount: -1}},
	} {
		targets := accountScopedSearchTargets(boxes, "Work")
		inboxes := 0
		unknown := false
		for _, target := range targets {
			if strings.EqualFold(target.MailboxName, "INBOX") {
				inboxes++
			}
			if target.MailboxName == "Empty" {
				t.Fatal("empty mailbox included")
			}
			if target.MailboxName == "Unknown" {
				unknown = true
			}
		}
		if inboxes != 1 {
			t.Fatalf("targets=%v", targets)
		}
		if len(boxes) > 1 && !unknown {
			t.Fatalf("unknown mailbox excluded: %v", targets)
		}
	}
}

func TestSearchJXARejectsMalformedCoverage(t *testing.T) {
	for _, response := range []string{`null`, `{}`, `[]`, `{"messages":[],"truncated":false,"readFailures":0,"scannedMessages":0,"totalMessages":1}`, `{"messages":[],"truncated":false,"readFailures":-1,"scannedMessages":0,"totalMessages":0}`, `{"messages":null,"truncated":false,"readFailures":0,"scannedMessages":0,"totalMessages":0}`} {
		t.Run(response, func(t *testing.T) {
			writeFakeOsaScript(t, "printf '%s' '"+response+"'")
			if _, err := NewClient().searchMessagesInSingleMailboxJXA("invoice", "Work", "INBOX", 50, ""); err == nil {
				t.Fatal("malformed coverage accepted")
			}
		})
	}
}

func TestSingleDiscoveredMailboxSearchCoverageOptions(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(map[bool]string{false: "fail closed", true: "allow partial"}[allow], func(t *testing.T) {
			installSearchCoverageBridge(t, map[string]any{"count": 501, "messages": []any{map[string]any{"subject": "invoice"}}})
			bridge, err := exec.LookPath("osascript")
			if err != nil {
				t.Fatal(err)
			}
			sqlite := filepath.Join(filepath.Dir(bridge), "sqlite3")
			script := `#!/bin/sh
case "$*" in
 *"order by url;"*) printf '%s' '[{"ID":1,"URL":"imap://synthetic/INBOX","TotalCount":501}]' ;;
 *) printf '%s' '[]' ;;
esac
`
			if err := os.WriteFile(sqlite, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			client := NewClient()
			client.shared.accounts = []Account{{Name: "Work", ID: "synthetic", Enabled: true}}
			client.shared.accountsLoaded = true
			result, err := client.SearchMessagesJSONSinceWithOptions("invoice", "Work", "", 50, "", SearchOptions{AllowPartial: allow})
			if result.Complete || len(result.Messages) != 1 || len(result.SearchedMailboxes) != 1 || len(result.FailedMailboxes) != 1 {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			var partial *PartialSearchError
			if allow {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.As(err, &partial) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}
