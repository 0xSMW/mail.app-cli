package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveTimeoutRetainsPreflightDestinationInRecoverableJournal(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "bridge.log")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> '%s'
case "$*" in
 *"move targetMessage"*) printf 'applescript timed out after 30s' >&2; exit 1 ;;
 *) printf 'All Mail\n' ;;
esac
`, log)
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	item := verificationItem()
	item.ID, item.TargetMailbox = "123", ""
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", TrustSource: true, Receipt: journal}, []BatchItem{item}, ArchiveMutator(false))
	if err == nil || len(result.Items) != 1 || result.Items[0].Status != "unknown" || result.Items[0].TargetMailbox != "All Mail" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	recovered, err := ReadRelocationReceipt(journal.Path())
	if err != nil || recovered.Items[0].Status != "unknown" || recovered.Items[0].TargetMailbox != "All Mail" {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
	// Recover a crash immediately after the real mutation_started event too.
	var crashed []byte
	for _, event := range readJournalEvents(t, journal.Path()) {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		crashed = append(append(crashed, line...), '\n')
		if event["event"] == "mutation_started" {
			break
		}
	}
	crashPath := filepath.Join(t.TempDir(), "crash.jsonl")
	if err := os.WriteFile(crashPath, crashed, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadRelocationReceipt(crashPath); err != nil || got.Items[0].TargetMailbox != "All Mail" || got.Items[0].Status != "unknown" {
		t.Fatalf("crash recovery=%+v err=%v", got, err)
	}
	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(logged), "move targetMessage") != 1 || strings.Count(string(logged), `set archiveMailbox to my findMailboxByName(mailboxes of targetAccount, "Archive")`) != 1 {
		t.Fatalf("archive was retried or destination reselected: %s", logged)
	}
	// Read-only reconciliation preserves unknown application status.
	client := fakeRelocationIndex(t, "INBOX", "All Mail", "imap://ABC/INBOX", "[]", "["+fakeIdentityRow+"]", false)
	got, err := ReconcileMutations(client, BatchOptions{}, recovered)
	if err != nil || got.Items[0].Status != "unknown" || got.Items[0].VerifyStatus != "confirmed_destination" {
		t.Fatalf("reconcile=%+v err=%v", got, err)
	}
}

func TestArchivePreflightFailureDoesNotMarkOrMove(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "calls")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'call\\n' >> '%s'\nprintf 'Archive mailbox not found' >&2\nexit 1\n", log)
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	item := verificationItem()
	item.TargetMailbox = ""
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", TrustSource: true, MarkReadBefore: true, Receipt: journal}, []BatchItem{item}, ArchiveMutator(false))
	if err == nil || result.Items[0].Status != "failed" || result.Items[0].MarkedRead {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(log)
	if err != nil || strings.Count(string(data), "call") != 1 {
		t.Fatalf("calls=%s err=%v", data, err)
	}
	events := readJournalEvents(t, journal.Path())
	if countJournalEvents(events, "mutation_started") != 0 || countJournalEvents(events, "mark_read_started") != 0 {
		t.Fatalf("mutated after failed preflight: %+v", events)
	}
}

func TestGmailArchiveDefaultWindowAndExplicitBudget(t *testing.T) {
	item := verificationItem()
	item.GmailInboxSource = true
	if got := relocationSettle(BatchOptions{Action: "archive"}, []BatchItem{item}); got != 10*time.Second {
		t.Fatalf("settle=%s", got)
	}
	if got := relocationSettle(BatchOptions{Action: "archive", Settle: 20 * time.Second}, []BatchItem{item}); got != 20*time.Second {
		t.Fatalf("settle=%s", got)
	}
	if got := relocationSettle(BatchOptions{Action: "archive", readOnlyVerification: true}, []BatchItem{item}); got != 0 {
		t.Fatalf("read-only default=%s", got)
	}
	mutated := false
	_, err := RunBatch(NewClient(), BatchOptions{Action: "archive", Verify: true, VerifyBudget: 5 * time.Second, TrustSource: true, Receipt: nil}, []BatchItem{item}, func(*Client, *BatchItem) error { mutated = true; return nil })
	if err == nil || mutated {
		t.Fatalf("insufficient budget: err=%v mutated=%t", err, mutated)
	}
}

// A regenerated INBOX membership arriving after the previous 3.85-second
// default must still be rejected by the new default observation window.
func TestGmailArchiveDefaultRejectsRecurrenceAfterOldWindow(t *testing.T) {
	client := fakeRelocationIndex(t, "INBOX", "All Mail", "imap://ABC/INBOX", "["+fakeIdentityRow+"]", "["+fakeIdentityRow+"]", true)
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	path := filepath.Join(bin, "sqlite3")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The seventh lookup is after 5.85 seconds with the production schedule.
	data = []byte(strings.Replace(string(data), `[ "$n" -lt 3 ]`, `[ "$n" -lt 7 ]`, 1))
	if err := os.WriteFile(path, data, 0755); err != nil {
		t.Fatal(err)
	}
	verificationBackoff = []time.Duration{0, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	item := verificationItem()
	item.Status, item.GmailInboxSource = "succeeded", true
	started := time.Now()
	got := VerifyMutations(client, BatchOptions{Action: "archive"}, []BatchItem{item})[0]
	if got.VerifyStatus != "present_in_source" || got.Observed == nil || !got.Observed.SourcePresent {
		t.Fatalf("late recurrence accepted: %+v", got)
	}
	if time.Since(started) < DefaultGmailArchiveSettle {
		t.Fatal("default settling window ended early")
	}
}

func TestGmailArchiveDetectsRecurrenceAfterExplicitSyncRequest(t *testing.T) {
	client := fakeRelocationIndex(t, "INBOX", "All Mail", "imap://ABC/INBOX", "["+fakeIdentityRow+"]", "["+fakeIdentityRow+"]", true)
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	log := filepath.Join(t.TempDir(), "sync-script")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> '%s'\n", log)
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	item := verificationItem()
	item.Status, item.GmailInboxSource = "succeeded", true
	got := VerifyMutations(client, BatchOptions{Action: "archive", Settle: time.Nanosecond}, []BatchItem{item})[0]
	if got.VerifyStatus != "present_in_source" || got.Observed == nil || !got.Observed.SourcePresent || !got.Observed.DestinationPresent {
		t.Fatalf("recurrence accepted: %+v", got)
	}
	if got.Observed.VerificationScope != "mail-local-after-sync-request" || got.Observed.SyncRequestedAt == "" {
		t.Fatalf("missing observation scope: %+v", got.Observed)
	}
	data, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(data), "synchronize with targetAccount") || strings.Count(string(data), `if name of acc is "Work"`) != 1 {
		t.Fatalf("did not request one explicit scoped synchronization: %s (%v)", data, err)
	}
}
