package mail

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDryRunSummaryCountsOnlyPreviewedItems(t *testing.T) {
	result := BatchResult{
		Action: "move", DryRun: true, Matched: 2, Skipped: 2,
		Items: []BatchItem{
			{ID: "1", Status: "dry-run", TargetMailbox: "Receipts"},
			{ID: "2", Status: "skipped", Error: "already in Receipts"},
		},
	}
	got := result.Summary(BatchOptions{Action: "move", TargetMailbox: "Receipts"})
	if got != "Dry run: would have moved 1 message to Receipts; 1 already there" {
		t.Fatalf("Summary = %q", got)
	}
}

func TestBatchJournalSurvivesInterruptedRunAfterCompletedItems(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "receipt.jsonl")
	journal, err := CreateBatchJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient().WithContext(ctx)
	items := []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX"}, {ID: "2", Account: "Work", SourceMailbox: "INBOX"}, {ID: "3", Account: "Work", SourceMailbox: "INBOX"}}
	result, err := RunBatch(client, BatchOptions{Action: "archive", Receipt: journal, TrustSource: true}, items, func(_ *Client, item *BatchItem) error {
		if item.ID == "2" {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunBatch() error = %v, want context.Canceled", err)
	}
	if result.Attempted != 2 || len(result.Items) != 2 {
		t.Fatalf("result = %+v, want exactly two durable attempted items", result)
	}
	events := readJournalEvents(t, journalPath)
	if got := countJournalEvents(events, "mutation_succeeded"); got != 2 {
		t.Fatalf("mutation_succeeded events = %d, want 2; events = %#v", got, events)
	}
	if got := events[len(events)-1]["event"]; got != "interrupted" {
		t.Fatalf("terminal event = %v, want interrupted", got)
	}
}

func TestBatchJournalPersistsPhasesWithoutReceiptStdout(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	journalPath := filepath.Join(t.TempDir(), "receipt.jsonl")
	journal, err := CreateBatchJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	_, err = RunBatch(NewClient(), BatchOptions{Action: "archive", MarkReadBefore: true, Receipt: journal, TrustSource: true}, []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX"}}, func(*Client, *BatchItem) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	events := readJournalEvents(t, journalPath)
	for _, event := range []string{"started", "mark_read_succeeded", "mutation_started", "mutation_succeeded", "completed"} {
		if countJournalEvents(events, event) != 1 {
			t.Fatalf("events missing %q: %#v", event, events)
		}
	}
}

func TestCreateBatchJournalRejectsInvalidPathBeforeMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "receipt.jsonl")
	if _, err := CreateBatchJournal(path); err == nil {
		t.Fatalf("CreateBatchJournal(%q) error = nil", path)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("receipt path state = %v, want nonexistent", err)
	}
}

func TestRunBatchCreatesPersistentReceiptWhenCallerDoesNotConfigureOne(t *testing.T) {
	configDir := t.TempDir()
	previousConfigDir := userConfigDir
	userConfigDir = func() (string, error) { return configDir, nil }
	t.Cleanup(func() { userConfigDir = previousConfigDir })
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", TrustSource: true}, []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX"}}, func(*Client, *BatchItem) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(result.ReceiptPath) {
		t.Fatalf("ReceiptPath = %q, want absolute persistent path", result.ReceiptPath)
	}
	if _, err := os.Stat(result.ReceiptPath); err != nil {
		t.Fatalf("receipt %q: %v", result.ReceiptPath, err)
	}
	events := readJournalEvents(t, result.ReceiptPath)
	if got := events[len(events)-1]["event"]; got != "completed" {
		t.Fatalf("terminal event = %v, want completed", got)
	}
}

func readJournalEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid journal line %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func countJournalEvents(events []map[string]any, want string) int {
	count := 0
	for _, event := range events {
		if event["event"] == want {
			count++
		}
	}
	return count
}
