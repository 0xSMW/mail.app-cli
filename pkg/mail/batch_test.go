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

func TestSummarySeparatesAppliedMutationFromUnverifiedDestination(t *testing.T) {
	result := BatchResult{Action: "move", Matched: 1, Attempted: 1, Succeeded: 1, Unverified: 1}
	got := result.Summary(BatchOptions{Action: "move", TargetMailbox: "Archive"})
	if got != "Moved 1 message to Archive; 1 unverified" {
		t.Fatalf("Summary = %q", got)
	}
}

func TestVerificationTimeoutKeepsReceiptOutcomeUnknown(t *testing.T) {
	items := []BatchItem{{Status: "succeeded", VerifyStatus: "unknown_after_timeout"}}
	if !hasUnknownItem(items) {
		t.Fatal("unknown_after_timeout verification should make the receipt interrupted")
	}
}

func TestBatchJournalSurvivesInterruptedRunAfterCompletedItems(t *testing.T) {
	writeFakeOsaScript(t, "case \"$*\" in *mail-app-cli-native-archive-account*) printf true ;; *) printf ready ;; esac\n")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	journalPath := filepath.Join(t.TempDir(), "receipt.jsonl")
	journal, err := CreateBatchJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	ctx, cancel := context.WithCancel(context.Background())
	client := NewClient().WithContext(ctx)
	items := []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "All Mail"}, {ID: "2", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "All Mail"}, {ID: "3", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "All Mail"}}
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
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\ncase \"$*\" in *mail-app-cli-native-archive-account*) printf true ;; *AXIsProcessTrusted*) printf ready ;; *) exit 0 ;; esac\n"), 0o755); err != nil {
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

	_, err = RunBatch(NewClient(), BatchOptions{Action: "archive", MarkReadBefore: true, Receipt: journal, TrustSource: true}, []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "All Mail"}}, func(*Client, *BatchItem) error { return nil })
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
	writeFakeOsaScript(t, "case \"$*\" in *mail-app-cli-native-archive-account*) printf true ;; *) printf ready ;; esac\n")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	configDir := t.TempDir()
	previousConfigDir := userConfigDir
	userConfigDir = func() (string, error) { return configDir, nil }
	t.Cleanup(func() { userConfigDir = previousConfigDir })
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", TrustSource: true}, []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "All Mail"}}, func(*Client, *BatchItem) error { return nil })
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

func TestNativeArchivePermissionPreflightStopsBeforeMarkRead(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("BATCH_NATIVE_LOG", log)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `
case "$*" in
 *mail-app-cli-native-archive-account*) printf true ;;
 *AXIsProcessTrusted*) printf 'permission\n' >> "$BATCH_NATIVE_LOG"; printf 'Accessibility denied' >&2; exit 1 ;;
 *) printf 'unexpected mutation\n' >> "$BATCH_NATIVE_LOG"; exit 1 ;;
esac
`)
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	mutated := false
	item := verificationItem()
	item.ID = "7"
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", MarkReadBefore: true, TrustSource: true, Receipt: journal}, []BatchItem{item}, func(*Client, *BatchItem) error { mutated = true; return nil })
	if err == nil || mutated || result.Attempted != 1 || len(result.Items) != 1 || result.Items[0].MarkedRead || result.Items[0].Status != "failed" {
		t.Fatalf("result=%+v err=%v mutated=%v", result, err, mutated)
	}
	events := readJournalEvents(t, journal.Path())
	if countJournalEvents(events, "mark_read_started") != 0 || countJournalEvents(events, "mutation_started") != 0 {
		t.Fatalf("mutation before permission: %#v", events)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "permission\n" {
		t.Fatalf("calls=%q err=%v", calls, err)
	}
}

func TestNativeArchiveUncertainActionStopsBatchWithoutReplay(t *testing.T) {
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("BATCH_NATIVE_LOG", log)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `
case "$*" in
 *mail-app-cli-native-archive-account*) printf true ;;
 *"const owner ="*) printf 'cleanup\n' >> "$BATCH_NATIVE_LOG"; printf closed ;;
 *"const request ="*)
  printf 'execute\n' >> "$BATCH_NATIVE_LOG"
  `+nativeArchiveOwnedEvent+nativeArchivePressEvent+`
  printf 'AXPress failed' >&2; exit 1 ;;
 *AXIsProcessTrusted*) printf ready ;;
 *) printf 'unexpected invocation' >&2; exit 1 ;;
esac
`)

	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	first, second := verificationItem(), verificationItem()
	first.ID = "7"
	second.ID = "8"
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", TrustSource: true, Receipt: journal}, []BatchItem{first, second}, ArchiveMutator(false))
	var uncertain *NativeArchiveError
	if !errors.As(err, &uncertain) || result.Attempted != 1 || len(result.Items) != 1 || result.Items[0].ID != "7" || result.Items[0].Status != "unknown" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertNativeArchiveInvocations(t, log, true)
	events := readJournalEvents(t, journal.Path())
	if countJournalEvents(events, "mutation_started") != 1 || countJournalEvents(events, "mutation_unknown") != 1 || countJournalEvents(events, "interrupted") != 1 {
		t.Fatalf("events=%#v", events)
	}
}
