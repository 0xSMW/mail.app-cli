package mail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func scanRecipientMessage(id, recipient string) Message {
	status := RecipientFieldStatus{State: "available", Source: "disk"}
	return Message{ID: id, ToRecipients: []string{recipient}, RecipientStatus: RecipientStatus{To: status, Cc: status, Bcc: status}}
}

func TestRecipientScanZeroMatchesAdvanceRawBoundary(t *testing.T) {
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Recipient: "TARGET@example.com", Limit: 1}
	reads := 0
	read := func(_ SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
		reads++
		if reads == 1 {
			if after != nil || limit != 1 {
				t.Fatalf("initial read %v %d", after, limit)
			}
			return scanPage{Messages: []Message{scanRecipientMessage("2", "other@example.com")}, Remaining: 1, Last: &ScanPosition{ReceivedUnix: 20, ID: 2}}, nil
		}
		if after == nil || after.ID != 2 {
			t.Fatalf("zero-match page lost raw boundary: %+v", after)
		}
		return scanPage{Messages: []Message{scanRecipientMessage("1", "target@example.com")}}, nil
	}
	first, err := scanMessages(req, read)
	if err != nil || first.Complete || len(first.Messages) != 0 || first.Coverage[0].Inspected != 1 || first.Coverage[0].Remaining != 1 || first.NextCursor == "" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	req.Cursor = first.NextCursor
	req.Recipient = "target@EXAMPLE.com"
	last, err := scanMessages(req, read)
	if err != nil || !last.Complete || len(last.Messages) != 1 || last.Coverage[0].Count != 1 {
		t.Fatalf("last=%+v err=%v", last, err)
	}
}

func TestRecipientScanUnknownRetriesOriginalPositionAndContinuesScopes(t *testing.T) {
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}, {"Work", "Archive"}}, Recipient: "target@example.com", Limit: 2}
	attempt := 0
	read := func(scope SearchMailbox, _ int, after *ScanPosition) (scanPage, error) {
		if scope.Mailbox == "Archive" {
			return scanPage{Messages: []Message{scanRecipientMessage("9", "target@example.com")}}, nil
		}
		if after != nil {
			t.Fatalf("unknown page skipped original position: %+v", after)
		}
		attempt++
		unknown := Message{ID: "2"}
		if attempt > 1 {
			unknown = scanRecipientMessage("2", "other@example.com")
		}
		return scanPage{Messages: []Message{scanRecipientMessage("3", "target@example.com"), unknown}, Remaining: 4, Last: &ScanPosition{ReceivedUnix: 20, ID: 2}}, nil
	}
	first, err := scanMessages(req, read)
	if err != nil || first.Complete || len(first.Messages) != 2 || first.Coverage[0].Error == "" || first.Coverage[0].Truncated || first.Coverage[0].Exhausted || first.Coverage[0].Remaining != 6 || first.Coverage[0].RecipientUnknown != 1 || first.Coverage[0].UnknownRecipientIDs[0] != "2" || !first.Coverage[1].Exhausted {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	req.Cursor = first.NextCursor
	next, err := scanMessages(req, read)
	if err != nil || next.Coverage[0].Error != "" || next.Coverage[0].RecipientUnknown != 0 || !next.Coverage[0].Truncated || next.Coverage[0].Remaining != 4 {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	cursor, err := decodeScanCursor(next.NextCursor, req, req.Scopes)
	if err != nil || cursor.Scopes[0].After.ID != 2 {
		t.Fatalf("cursor=%+v err=%v", cursor, err)
	}
}

func TestRecipientScanCheckpointRetryDeduplicatesMatchesAndIdentity(t *testing.T) {
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}, {"Work", "Archive"}}, Recipient: "target@example.com", Flagged: true, Checkpoint: filepath.Join(t.TempDir(), "scan.json")}
	known := false
	read := func(scope SearchMailbox, _ int, after *ScanPosition) (scanPage, error) {
		if after != nil {
			t.Fatalf("retry advanced: %+v", after)
		}
		if scope.Mailbox == "Archive" {
			return scanPage{Messages: []Message{scanRecipientMessage("1", "target@example.com")}}, nil
		}
		candidate := Message{ID: "2"}
		if known {
			candidate = scanRecipientMessage("2", "other@example.com")
		}
		return scanPage{Messages: []Message{scanRecipientMessage("1", "target@example.com"), candidate}, Last: &ScanPosition{ID: 2}}, nil
	}
	for i := 0; i < 2; i++ {
		result, err := scanMessages(req, read)
		if err != nil || result.Coverage[0].Count != 1 || result.Coverage[1].Count != 1 || len(result.Messages) != 1 || len(result.Messages[0].Mailboxes) != 2 || len(result.Coverage[0].Errors) != i+1 {
			t.Fatalf("attempt=%d result=%+v err=%v", i, result, err)
		}
	}
	for _, change := range []func(*ScanRequest){func(r *ScanRequest) { r.Recipient = "different@example.com" }, func(r *ScanRequest) { r.Flagged = false }, func(r *ScanRequest) { r.Unread = true }} {
		changed := req
		change(&changed)
		if _, err := scanMessages(changed, read); !errors.Is(err, ErrInvalidScanCheckpoint) {
			t.Fatalf("changed filter err=%v", err)
		}
	}
	req.Recipient = "TARGET@EXAMPLE.COM"
	known = true
	final, err := scanMessages(req, read)
	if err != nil || !final.Complete || final.Coverage[0].Count != 1 || final.Coverage[0].Error != "" || final.Coverage[0].RecipientUnknown != 0 || len(final.Coverage[0].Errors) != 2 || len(final.Messages[0].ToRecipients) != 1 || final.Messages[0].RecipientStatus.To.State != "available" {
		t.Fatalf("final=%+v err=%v", final, err)
	}
}

func TestRecipientScanCursorBindsFiltersAndValidatesBeforeRead(t *testing.T) {
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Recipient: "target@example.com", Flagged: true}
	read := func(SearchMailbox, int, *ScanPosition) (scanPage, error) {
		return scanPage{Messages: []Message{scanRecipientMessage("2", "target@example.com")}, Remaining: 1, Last: &ScanPosition{ID: 2}}, nil
	}
	first, err := scanMessages(req, read)
	if err != nil {
		t.Fatal(err)
	}
	req.Cursor = first.NextCursor
	for _, change := range []func(*ScanRequest){func(r *ScanRequest) { r.Recipient = "different@example.com" }, func(r *ScanRequest) { r.Flagged = false }, func(r *ScanRequest) { r.Unread = true }} {
		changed := req
		change(&changed)
		if _, err := scanMessages(changed, read); !errors.Is(err, ErrInvalidScanCursor) {
			t.Fatalf("changed filter err=%v", err)
		}
	}
	req.Recipient = "invalid address"
	if _, err := scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) {
		t.Fatal("read with invalid filter")
		return scanPage{}, nil
	}); err == nil {
		t.Fatal("invalid recipient accepted")
	}
}

func TestRecipientScanIndexFlaggedFiltersCandidatesAndRemaining(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "queries")
	script := `#!/bin/sh
printf '%s\n' "$4" >> "$MAIL_APP_CLI_SQL_LOG"
case "$4" in
 *"count(*) as Remaining"*) printf '%s' '[{"Remaining":1}]';;
 *) printf '%s' '[{"ID":3,"ReceivedUnix":30}]';;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "sqlite3"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", dir)
	t.Setenv("MAIL_APP_CLI_SQL_LOG", log)
	page, err := NewClient().scanMessagesFromIndex("Work", &indexMailbox{ID: 4, Name: "INBOX"}, ScanRequest{Flagged: true, Unread: true}, 1, nil)
	if err != nil || page.Remaining != 1 || page.Last == nil || page.Last.ID != 3 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "m.flagged != 0") != 2 || strings.Count(string(data), "m.read = 0") != 2 {
		t.Fatalf("filters absent from candidate/count query: %s", data)
	}
}

func TestRecipientScanUnknownRetriesAfterEstablishedBoundary(t *testing.T) {
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Recipient: "target@example.com", Limit: 1}
	reads := 0
	read := func(_ SearchMailbox, _ int, after *ScanPosition) (scanPage, error) {
		reads++
		if reads == 1 {
			return scanPage{Messages: []Message{scanRecipientMessage("3", "other@example.com")}, Remaining: 2, Last: &ScanPosition{ID: 3, ReceivedUnix: 30}}, nil
		}
		if after == nil || after.ID != 3 {
			t.Fatalf("retry boundary=%+v", after)
		}
		if reads == 2 {
			return scanPage{Messages: []Message{{ID: "2"}}, Remaining: 1, Last: &ScanPosition{ID: 2, ReceivedUnix: 20}}, nil
		}
		return scanPage{Messages: []Message{scanRecipientMessage("2", "target@example.com")}, Remaining: 1, Last: &ScanPosition{ID: 2, ReceivedUnix: 20}}, nil
	}
	first, err := scanMessages(req, read)
	if err != nil {
		t.Fatal(err)
	}
	req.Cursor = first.NextCursor
	unknown, err := scanMessages(req, read)
	if err != nil || unknown.Coverage[0].Remaining != 2 || unknown.Coverage[0].Error == "" {
		t.Fatalf("unknown=%+v err=%v", unknown, err)
	}
	if unknown.NextCursor != first.NextCursor {
		t.Fatalf("unknown page changed cursor")
	}
	req.Cursor = unknown.NextCursor
	known, err := scanMessages(req, read)
	if err != nil || len(known.Messages) != 1 || known.NextCursor == unknown.NextCursor {
		t.Fatalf("known=%+v err=%v", known, err)
	}
}
