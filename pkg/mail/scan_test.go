package mail

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanPreservesMembershipAndAccountIdentity(t *testing.T) {
	scopes := []SearchMailbox{{"Work", "INBOX"}, {"Work", "All Mail"}, {"Home", "INBOX"}, {"Work", "INBOX"}}
	calls := 0
	result, err := scanMessages(ScanRequest{Scopes: scopes, Limit: 5}, func(scope SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
		calls++
		if limit != 5 || after != nil {
			t.Fatalf("limit = %d, after = %+v", limit, after)
		}
		return scanPage{Messages: []Message{{ID: "1", Subject: "same local id", Account: scope.Account}}}, nil
	})
	if err != nil || calls != 3 || len(result.Messages) != 2 || !result.Complete || result.NextCursor != "" || len(result.Messages[0].Mailboxes) != 2 {
		t.Fatalf("calls = %d, result = %+v, %v", calls, result, err)
	}
	for _, coverage := range result.Coverage {
		if !coverage.Exhausted || coverage.Remaining != 0 {
			t.Fatalf("coverage = %+v, want exhausted", coverage)
		}
	}
	data, err := json.Marshal(result)
	if err != nil || !strings.Contains(string(data), `"mailboxes":["INBOX","All Mail"]`) || !strings.Contains(string(data), `"toRecipients":[]`) {
		t.Fatalf("membership/recipient shape lost: %s, %v", data, err)
	}
}

func TestScanCannotClaimCompleteOnTruncationOrFailure(t *testing.T) {
	result, err := scanMessages(ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}, {"Work", "Spam"}, {"Work", "Archive"}}, Limit: 1}, func(scope SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
		switch scope.Mailbox {
		case "INBOX":
			return scanPage{Messages: []Message{{ID: "1"}}, Remaining: 4, Last: &ScanPosition{ReceivedUnix: 100, ID: 1}}, nil
		case "Spam":
			return scanPage{Messages: []Message{{ID: "unsafe-partial"}}}, errors.New("unavailable")
		default:
			return scanPage{Messages: []Message{{ID: "3"}}}, nil
		}
	})
	if err != nil || result.Complete || !result.Coverage[0].Truncated || result.Coverage[0].Remaining != 4 || result.Coverage[0].Exhausted || result.Coverage[1].Error == "" || result.Coverage[2].Truncated || !result.Coverage[2].Exhausted || len(result.Messages) != 2 {
		t.Fatalf("result = %+v, %v", result, err)
	}
	if result.NextCursor == "" {
		t.Fatal("an incomplete scan returned no cursor")
	}
}

func TestScanCursorTraversesEveryMailboxToExhaustion(t *testing.T) {
	// Newest first. Two messages share a receipt second, so the position
	// must order by local ID as well as by date.
	mailboxes := map[string][]ScanPosition{
		"INBOX":    {{300, 9}, {200, 8}, {200, 7}, {100, 2}, {50, 1}},
		"All Mail": {{300, 9}, {40, 3}},
	}
	read := func(scope SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
		var rows []ScanPosition
		for _, row := range mailboxes[scope.Mailbox] {
			if after == nil || row.ReceivedUnix < after.ReceivedUnix || (row.ReceivedUnix == after.ReceivedUnix && row.ID < after.ID) {
				rows = append(rows, row)
			}
		}
		page := scanPage{}
		for i, row := range rows {
			if i == limit {
				break
			}
			row := row
			page.Messages = append(page.Messages, Message{ID: string(rune('0' + row.ID))})
			page.Last = &row
		}
		page.Remaining = len(rows) - len(page.Messages)
		return page, nil
	}
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}, {"Work", "All Mail"}}, Limit: 2}
	seen := map[string]int{}
	var inboxReads int
	for page := 0; ; page++ {
		if page > 5 {
			t.Fatal("traversal did not finish")
		}
		result, err := scanMessages(req, func(scope SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
			if scope.Mailbox == "INBOX" {
				inboxReads++
			}
			return read(scope, limit, after)
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range result.Messages {
			for _, mailbox := range message.Mailboxes {
				seen[mailbox+"/"+message.ID]++
			}
		}
		if result.NextCursor == "" {
			if !result.Complete {
				t.Fatalf("final page = %+v, want complete", result)
			}
			break
		}
		if result.Complete {
			t.Fatalf("page %d claimed complete with a cursor: %+v", page, result)
		}
		req.Cursor = result.NextCursor
	}
	if len(seen) != 7 {
		t.Fatalf("seen = %v, want all seven memberships", seen)
	}
	for key, count := range seen {
		if count != 1 {
			t.Fatalf("%s returned %d times", key, count)
		}
	}
	if inboxReads != 3 {
		t.Fatalf("INBOX read %d times, want 3", inboxReads)
	}
}

func TestScanCursorRetriesFailedMailboxAndSkipsExhausted(t *testing.T) {
	fail := true
	reads := map[string]int{}
	read := func(scope SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
		reads[scope.Mailbox]++
		if scope.Mailbox == "Spam" && fail {
			return scanPage{}, errors.New("unavailable")
		}
		return scanPage{Messages: []Message{{ID: scope.Mailbox}}}, nil
	}
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}, {"Work", "Spam"}}, Limit: 5}
	first, err := scanMessages(req, read)
	if err != nil || first.Complete || first.NextCursor == "" {
		t.Fatalf("first = %+v, %v", first, err)
	}
	fail = false
	req.Cursor = first.NextCursor
	second, err := scanMessages(req, read)
	if err != nil || !second.Complete || second.NextCursor != "" || reads["INBOX"] != 1 || reads["Spam"] != 2 {
		t.Fatalf("second = %+v, %v, reads = %v", second, err, reads)
	}
	if !second.Coverage[0].Exhausted || second.Coverage[0].Count != 0 || len(second.Messages) != 1 {
		t.Fatalf("second = %+v", second)
	}
}

func TestScanCursorRejectsDifferentRequest(t *testing.T) {
	read := func(SearchMailbox, int, *ScanPosition) (scanPage, error) {
		return scanPage{Messages: []Message{{ID: "1"}}, Remaining: 1, Last: &ScanPosition{ReceivedUnix: 1, ID: 1}}, nil
	}
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Limit: 1, Unread: true}
	first, err := scanMessages(req, read)
	if err != nil {
		t.Fatal(err)
	}
	for name, changed := range map[string]ScanRequest{
		"filter":  {Scopes: req.Scopes, Limit: 1, Cursor: first.NextCursor},
		"mailbox": {Scopes: []SearchMailbox{{"Work", "Spam"}}, Limit: 1, Unread: true, Cursor: first.NextCursor},
		"garbage": {Scopes: req.Scopes, Limit: 1, Unread: true, Cursor: "not a cursor"},
	} {
		if _, err := scanMessages(changed, read); !errors.Is(err, ErrInvalidScanCursor) {
			t.Fatalf("%s: err = %v, want invalid cursor", name, err)
		}
	}
	// The page size is free to change between pages.
	req.Cursor, req.Limit = first.NextCursor, 50
	if _, err := scanMessages(req, read); err != nil {
		t.Fatalf("changed limit rejected: %v", err)
	}
}

func TestScanIndexQueryFiltersBeforeLimitAndCountsRemaining(t *testing.T) {
	binDir := t.TempDir()
	sqlLog := filepath.Join(t.TempDir(), "sqlite.log")
	sqlite := `#!/bin/sh
printf '%s\n---\n' "$4" >> "$MAIL_APP_CLI_SQL_LOG"
case "$4" in
  *"count(*) as Remaining"*) printf '%s\n' '[{"Remaining":7}]' ;;
  *) printf '%s\n' '[{"ID":12,"ReceivedUnix":500,"Subject":"a"},{"ID":11,"ReceivedUnix":500,"Subject":"b"}]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "sqlite3"), []byte(sqlite), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_SQL_LOG", sqlLog)
	page, err := NewClient().scanMessagesFromIndex("Work", &indexMailbox{ID: 4, Name: "INBOX"}, ScanRequest{Query: "invoice", Unread: true}, 2, &ScanPosition{ReceivedUnix: 900, ID: 40})
	if err != nil || len(page.Messages) != 2 || page.Remaining != 7 || page.Last == nil || *page.Last != (ScanPosition{ReceivedUnix: 500, ID: 11}) {
		t.Fatalf("page = %+v, %v", page, err)
	}
	logged, err := os.ReadFile(sqlLog)
	if err != nil {
		t.Fatal(err)
	}
	queries := strings.Split(string(logged), "---\n")
	for _, want := range []string{"m.read = 0", "invoice", "m.ROWID < 40", "order by coalesce(m.date_received, 0) desc, m.ROWID desc", "limit 2"} {
		if !strings.Contains(queries[0], want) {
			t.Fatalf("page query missing %q:\n%s", want, queries[0])
		}
	}
	for _, want := range []string{"m.read = 0", "invoice", "m.ROWID < 11"} {
		if !strings.Contains(queries[1], want) {
			t.Fatalf("count query missing %q:\n%s", want, queries[1])
		}
	}
}
