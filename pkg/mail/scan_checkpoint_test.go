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

func TestScanCheckpointExclusiveWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scan.json")
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Limit: 1, Checkpoint: path}
	_, err := scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) {
		return scanPage{Messages: []Message{{ID: "3"}}, Remaining: 2, Last: &ScanPosition{ReceivedUnix: 100, ID: 3}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Pause A after loading its checkpoint and inside its read callback. B must
	// fail before reading, both for a resume and for an explicit restart.
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) {
			close(entered)
			<-release
			return scanPage{Messages: []Message{{ID: "2"}}, Remaining: 1, Last: &ScanPosition{ReceivedUnix: 90, ID: 2}}, nil
		})
		done <- err
	}()
	released := false
	defer func() {
		if !released {
			close(release)
			<-done
		}
	}()
	<-entered
	for _, restart := range []bool{false, true} {
		second := req
		second.Limit, second.RestartCheckpoint = 2, restart
		called := false
		_, err := scanMessages(second, func(SearchMailbox, int, *ScanPosition) (scanPage, error) {
			called = true
			return scanPage{Messages: []Message{{ID: "2"}, {ID: "1"}}}, nil
		})
		if !errors.Is(err, ErrInvalidScanCheckpoint) || !strings.Contains(err.Error(), "already in use") || called {
			t.Errorf("concurrent writer (restart=%t): read=%t error=%v", restart, called, err)
		}
		after, err := os.ReadFile(path)
		if err != nil || string(before) != string(after) {
			t.Errorf("concurrent writer replaced durable progress: %v", err)
		}
	}
	independent := req
	independent.Checkpoint = filepath.Join(filepath.Dir(path), "independent.json")
	if _, err := scanMessages(independent, func(SearchMailbox, int, *ScanPosition) (scanPage, error) { return scanPage{}, nil }); err != nil {
		t.Errorf("different checkpoint was blocked: %v", err)
	}
	close(release)
	released = true
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, err := scanMessages(req, func(_ SearchMailbox, _ int, after *ScanPosition) (scanPage, error) {
		if after == nil || after.ID != 2 {
			t.Errorf("resume lost first writer's cursor: %+v", after)
		}
		return scanPage{Messages: []Message{{ID: "1"}}}, nil
	})
	if err != nil || !final.Complete || len(final.Messages) != 3 {
		t.Fatalf("writer lock was not released or progress was lost: result=%+v err=%v", final, err)
	}
}

func TestScanCheckpointReleasesAfterReadFailure(t *testing.T) {
	req := ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Limit: 1, Checkpoint: filepath.Join(t.TempDir(), "scan.json")}
	_, err := scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) {
		return scanPage{}, errors.New("read failed")
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := scanMessages(req, func(SearchMailbox, int, *ScanPosition) (scanPage, error) { return scanPage{}, nil })
	if err != nil || !got.Complete || !got.Checkpoint.Resumed {
		t.Fatalf("failed read left checkpoint locked or skipped retry: result=%+v err=%v", got, err)
	}
}

func TestScanCheckpointRejectsUnsafeLock(t *testing.T) {
	for _, kind := range []string{"public", "directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "scan.json")
			lock := path + ".lock"
			var err error
			switch kind {
			case "public":
				err = os.WriteFile(lock, nil, 0644)
			case "directory":
				err = os.Mkdir(lock, 0700)
			case "symlink":
				target := filepath.Join(filepath.Dir(path), "target")
				if err = os.WriteFile(target, []byte("untouched"), 0600); err == nil {
					err = os.Symlink(target, lock)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			called := false
			_, err = scanMessages(ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Checkpoint: path}, func(SearchMailbox, int, *ScanPosition) (scanPage, error) {
				called = true
				return scanPage{}, nil
			})
			if !errors.Is(err, ErrInvalidScanCheckpoint) || called {
				t.Fatalf("unsafe lock accepted: read=%t error=%v", called, err)
			}
		})
	}
}
