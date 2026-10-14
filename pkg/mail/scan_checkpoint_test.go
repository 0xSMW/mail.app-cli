package mail

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanCheckpointResumeMembershipFailureAndLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan.json")
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}, {"Work", "Archive"}}, Limit: 1, Checkpoint: path, Query: "receipt"}
	first, err := scanMessages(req, func(scope SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
		if scope.Mailbox == "Archive" {
			return scanPage{}, errors.New("deadline")
		}
		return scanPage{Messages: []Message{{ID: "2", Subject: "kept"}}, Remaining: 1, Last: &ScanPosition{ReceivedUnix: 100, ID: 2}}, nil
	})
	if err != nil || first.Complete || first.Checkpoint.Resumed || len(first.Messages) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	// Only the file crosses the reconstruction boundary; no in-memory result or cursor is reused.
	req.Limit = 50
	second, err := scanMessages(req, func(scope SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
		if limit != 50 {
			t.Fatalf("limit=%d", limit)
		}
		if scope.Mailbox == "INBOX" {
			if after == nil || after.ID != 2 {
				t.Fatalf("after=%+v", after)
			}
			return scanPage{Messages: []Message{{ID: "1"}}}, nil
		}
		if after != nil {
			t.Fatalf("failed scope skipped: %+v", after)
		}
		return scanPage{Messages: []Message{{ID: "2"}}}, nil
	})
	if err != nil || !second.Complete || !second.Checkpoint.Resumed || second.Checkpoint.Pages != 2 || len(second.Messages) != 2 || len(second.Messages[0].Mailboxes) != 2 || second.Messages[0].Subject != "kept" || second.Coverage[0].Count != 2 || second.Coverage[1].Count != 1 || len(second.Coverage[1].Errors) != 1 || second.Coverage[1].Error != "" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if second.StartedAt != first.StartedAt || second.Checkpoint.PageStartedAt == first.StartedAt {
		t.Fatalf("observation bounds=%+v", second.Checkpoint)
	}
	called := false
	if _, err := scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) { called = true; return scanPage{}, nil }); !errors.Is(err, ErrInvalidScanCheckpoint) || called {
		t.Fatalf("completed checkpoint read again: %v called=%v", err, called)
	}
	req.RestartCheckpoint = true
	fresh, err := scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) { return scanPage{}, nil })
	if err != nil || fresh.Checkpoint.Pages != 1 || fresh.Checkpoint.Resumed {
		t.Fatalf("restart=%+v err=%v", fresh, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("permissions=%v err=%v", info, err)
	}
}

func TestScanCheckpointRejectsChangedRequestBeforeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan.json")
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Limit: 1, Checkpoint: path}
	_, err := scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) { return scanPage{}, errors.New("timeout") })
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*ScanRequest){func(r *ScanRequest) { r.Query = "new" }, func(r *ScanRequest) { r.Unread = true }, func(r *ScanRequest) { r.Since = "2026-01-01" }, func(r *ScanRequest) { r.Scopes = []SearchMailbox{{"Work", "Archive"}} }, func(r *ScanRequest) { r.Cursor = "anything" }} {
		changed := req
		change(&changed)
		called := false
		_, err := scanMessages(changed, func(SearchMailbox, int, *ScanPosition) (scanPage, error) { called = true; return scanPage{}, nil })
		if !errors.Is(err, ErrInvalidScanCheckpoint) || called {
			t.Fatalf("changed=%+v err=%v called=%v", changed, err, called)
		}
	}
}

func TestScanCheckpointRejectsCorruptionAndUnsafePaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan.json")
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Checkpoint: path}
	read := func(SearchMailbox, int, *ScanPosition) (scanPage, error) { return scanPage{}, errors.New("timeout") }
	if _, err := scanMessages(req, read); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(path)
	var state scanCheckpoint
	if err := json.Unmarshal(original, &state); err != nil {
		t.Fatal(err)
	}
	state.Result.NextCursor = "garbage"
	bad, _ := json.Marshal(state)
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := scanMessages(req, read); !errors.Is(err, ErrInvalidScanCheckpoint) {
		t.Fatalf("corrupt cursor=%v", err)
	}
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := scanMessages(req, read); !errors.Is(err, ErrInvalidScanCheckpoint) {
		t.Fatalf("public file=%v", err)
	}
	req.Checkpoint = t.TempDir()
	if _, err := scanMessages(req, read); !errors.Is(err, ErrInvalidScanCheckpoint) {
		t.Fatalf("directory=%v", err)
	}
	req.Checkpoint = filepath.Join(t.TempDir(), "missing", "scan.json")
	if _, err := scanMessages(req, read); !errors.Is(err, ErrInvalidScanCheckpoint) {
		t.Fatalf("missing parent=%v", err)
	}
}

func TestScanCheckpointFailedWritePreservesOriginal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan.json")
	original := []byte("previous checkpoint")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	state := scanCheckpoint{Result: ScanResult{Messages: []ScanMessage{{Message: Message{ID: "1", Content: strings.Repeat("x", maxScanCheckpointBytes)}}}}}
	if err := saveScanCheckpoint(path, state); err == nil {
		t.Fatal("oversized write succeeded")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("failed write replaced checkpoint")
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}
