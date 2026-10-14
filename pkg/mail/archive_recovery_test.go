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
	t.Setenv("ARCHIVE_TEST_LOG", log)
	script := `#!/bin/sh
case "$*" in
 *"mail-app-cli-native-archive-account"*) printf 'account-probe\n' >> "$ARCHIVE_TEST_LOG"; printf true ;;
 *"const owner ="*) printf 'cleanup\n' >> "$ARCHIVE_TEST_LOG"; printf closed ;;
 *"const request ="*)
  printf 'press\n' >> "$ARCHIVE_TEST_LOG"
  ` + nativeArchiveOwnedEvent + nativeArchivePressEvent + `
  printf 'native Archive Apple Event timed out (-1712)' >&2; exit 1 ;;
 *"AXIsProcessTrusted"*) printf 'permission\n' >> "$ARCHIVE_TEST_LOG"; printf ready ;;
 *) printf 'destination\n' >> "$ARCHIVE_TEST_LOG"; printf 'All Mail\n' ;;
esac
`
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
	if string(logged) != "destination\naccount-probe\npermission\npress\ncleanup\n" {
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

func TestArchivePreflightIsSharedPerAccountAcrossChunks(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "bridge.log")
	t.Setenv("ARCHIVE_TEST_LOG", log)
	script := `#!/bin/sh
case "$*" in
 *"mail-app-cli-native-archive-account"*) printf 'account-probe\n' >> "$ARCHIVE_TEST_LOG"; printf true ;;
 *"const owner ="*) printf 'cleanup\n' >> "$ARCHIVE_TEST_LOG"; printf closed ;;
 *"const request ="*)
  case "$*" in *'"account":"Alpha"'*) ;; *) printf 'wrong native account' >&2; exit 1 ;; esac
  case "$*" in *'"id":1'*) id=1 ;; *'"id":2'*) id=2 ;; *) exit 1 ;; esac
  printf 'native:Alpha:%s\n' "$id" >> "$ARCHIVE_TEST_LOG"
  ` + nativeArchiveOwnedEvent + nativeArchivePressEvent + `
  printf '%s' '{"prepared":true,"applied":true}' ;;
 *"AXIsProcessTrusted"*) printf 'permission\n' >> "$ARCHIVE_TEST_LOG"; printf ready ;;
 *)
  case "$*" in *'account "Beta"'*) destination=Archive ;; *) destination='All Mail' ;; esac
  case "$*" in *"move targetMessage"*) phase=move ;; *) phase=preflight ;; esac
  printf '%s:%s\n' "$phase" "$destination" >> "$ARCHIVE_TEST_LOG"
  printf '%s\n' "$destination" ;;
esac
`
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
	items := make([]BatchItem, 3)
	for i, account := range []string{"Alpha", "Alpha", "Beta"} {
		items[i] = verificationItem()
		items[i].ID, items[i].Account, items[i].TargetMailbox = fmt.Sprint(i+1), account, ""
	}
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", TrustSource: true, ChunkSize: 1, Receipt: journal}, items, ArchiveMutator(false))
	if err != nil || result.Succeeded != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "preflight:All Mail\naccount-probe\npermission\nnative:Alpha:1\ncleanup\nnative:Alpha:2\ncleanup\npreflight:Archive\nmove:Archive\n"
	if string(data) != want {
		t.Fatalf("calls=%s want=%s", data, want)
	}
	for _, event := range readJournalEvents(t, journal.Path()) {
		if event["event"] != "mutation_started" {
			continue
		}
		item := event["item"].(map[string]any)
		want := "All Mail"
		if item["account"] == "Beta" {
			want = "Archive"
		}
		if item["targetMailbox"] != want {
			t.Fatalf("wrong persisted account destination: %+v", item)
		}
	}
}

func TestArchiveDryRunResolvesOncePerAccountAndPreservesPinnedOrSkippedItems(t *testing.T) {
	log := filepath.Join(t.TempDir(), "bridge.log")
	t.Setenv("ARCHIVE_DRY_RUN_LOG", log)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `printf '%s\n' "$*" >> "$ARCHIVE_DRY_RUN_LOG"
case "$*" in *'account "Beta"'*) printf 'Archive\n' ;; *) printf 'All Mail\n' ;; esac
`)
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	items := []BatchItem{
		{ID: "1", Account: "Alpha", SourceMailbox: "INBOX"},
		{ID: "2", Account: "Alpha", SourceMailbox: "INBOX"},
		{ID: "3", Account: "Beta", SourceMailbox: "INBOX"},
		{ID: "4", Account: "Pinned", SourceMailbox: "INBOX", TargetMailbox: "Archive"},
		{ID: "5", Account: "Skipped", SourceMailbox: "All Mail"},
	}
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", DryRun: true, TrustSource: true, MarkReadBefore: true, Receipt: journal}, items, func(*Client, *BatchItem) error {
		t.Fatal("dry-run invoked a mutator")
		return nil
	})
	if err != nil || result.Attempted != 0 || result.Failed != 0 || len(result.Items) != len(items) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for i, want := range []string{"All Mail", "All Mail", "Archive", "Archive"} {
		if result.Items[i].Status != "dry-run" || result.Items[i].TargetMailbox != want || result.Items[i].MarkedRead {
			t.Fatalf("item %d=%+v, want dry-run destination %q", i, result.Items[i], want)
		}
	}
	if result.Items[4].Status != "skipped" || result.Items[4].TargetMailbox != "" {
		t.Fatalf("skipped archive changed: %+v", result.Items[4])
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	if strings.Count(script, `set targetAccount to account "Alpha"`) != 1 || strings.Count(script, `set targetAccount to account "Beta"`) != 1 || strings.Contains(script, `account "Pinned"`) || strings.Contains(script, `account "Skipped"`) || strings.Contains(script, "move targetMessage") || strings.Contains(script, "set read status") {
		t.Fatalf("unexpected dry-run bridge calls: %s", script)
	}
	allMail := strings.Index(script, `findMailboxByName(mailboxes of targetAccount, "All Mail")`)
	archive := strings.Index(script, `findMailboxByName(mailboxes of targetAccount, "Archive")`)
	if allMail < 0 || archive < allMail {
		t.Fatalf("dry-run did not use the recursive archive resolver: %s", script)
	}
	events := readJournalEvents(t, journal.Path())
	if countJournalEvents(events, "mutation_started") != 0 || countJournalEvents(events, "mark_read_started") != 0 {
		t.Fatalf("dry-run recorded a mutation attempt: %+v", events)
	}
}

func TestArchiveDryRunFailsClosedOnDestinationResolutionError(t *testing.T) {
	for _, body := range []string{
		`printf 'Archive mailbox not found' >&2; exit 1`,
		`printf 'applescript timed out after 30s' >&2; exit 1`,
		`printf 'unexpected mailbox\n'`,
	} {
		t.Run(body, func(t *testing.T) {
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
			writeFakeOsaScript(t, body)
			journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", DryRun: true, TrustSource: true, MarkReadBefore: true, Receipt: journal}, []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX"}}, func(*Client, *BatchItem) error {
				t.Fatal("failed preview invoked a mutator")
				return nil
			})
			if err == nil || result.Attempted != 0 || result.Failed != 1 || len(result.Items) != 1 || result.Items[0].Status != "failed" || result.Items[0].TargetMailbox != "" || result.Items[0].MarkedRead {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			events := readJournalEvents(t, journal.Path())
			if countJournalEvents(events, "mutation_started") != 0 || countJournalEvents(events, "mark_read_started") != 0 {
				t.Fatalf("failed preview recorded a mutation attempt: %+v", events)
			}
		})
	}
}

func TestArchiveDryRunTimeoutStopsBeforeNextDestinationLookup(t *testing.T) {
	log := filepath.Join(t.TempDir(), "bridge.log")
	t.Setenv("ARCHIVE_DRY_RUN_LOG", log)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `printf 'lookup\n' >> "$ARCHIVE_DRY_RUN_LOG"
printf 'applescript timed out after 30s' >&2
exit 1
`)
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	items := []BatchItem{
		{ID: "1", Account: "Alpha", SourceMailbox: "INBOX"},
		{ID: "2", Account: "Beta", SourceMailbox: "INBOX"},
	}
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", DryRun: true, TrustSource: true, MarkReadBefore: true, Receipt: journal}, items, func(*Client, *BatchItem) error {
		t.Fatal("timed-out preview invoked a mutator")
		return nil
	})
	if !isVerificationTimeout(err) || result.Failed != 1 || result.Attempted != 0 || len(result.Items) != 1 || result.Items[0].Status != "failed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "lookup\n" {
		t.Fatalf("lookup log=%q err=%v", data, err)
	}
}

func TestGmailArchiveDefaultWindowAndExplicitBudget(t *testing.T) {
	item := verificationItem()
	item.GmailInboxSource = true
	if got := relocationSettle(BatchOptions{Action: "archive"}, []BatchItem{item}); got != 3850*time.Millisecond {
		t.Fatalf("settle=%s", got)
	}
	if got := relocationSettle(BatchOptions{Action: "archive", Settle: 20 * time.Second}, []BatchItem{item}); got != 20*time.Second {
		t.Fatalf("settle=%s", got)
	}
	if got := relocationSettle(BatchOptions{Action: "archive", readOnlyVerification: true}, []BatchItem{item}); got != 0 {
		t.Fatalf("read-only default=%s", got)
	}
	mutated := false
	_, err := RunBatch(NewClient(), BatchOptions{Action: "archive", Verify: true, Settle: 10 * time.Second, VerifyBudget: 5 * time.Second, TrustSource: true, Receipt: nil}, []BatchItem{item}, func(*Client, *BatchItem) error { mutated = true; return nil })
	if err == nil || mutated {
		t.Fatalf("insufficient budget: err=%v mutated=%t", err, mutated)
	}
}

// An explicit longer window continues observing identities after the default
// horizon, even when an early observation already confirmed the destination.
func TestGmailArchiveExplicitWindowRejectsRecurrenceAfterDefaultWindow(t *testing.T) {
	client := fakeRelocationIndex(t, "INBOX", "All Mail", "imap://ABC/INBOX", "["+fakeIdentityRow+"]", "["+fakeIdentityRow+"]", true)
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	path := filepath.Join(bin, "sqlite3")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The seventh lookup is after 5.85 seconds with the production schedule.
	data = []byte(strings.Replace(string(data), `[ "$n" -lt 3 ]`, `[ "$n" -lt 7 ]`, -1))
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
	got := VerifyMutations(client, BatchOptions{Action: "archive", Settle: 10 * time.Second}, []BatchItem{item})[0]
	if got.VerifyStatus != "present_in_source" || got.Observed == nil || !got.Observed.SourcePresent {
		t.Fatalf("late recurrence accepted: %+v", got)
	}
	if time.Since(started) < 10*time.Second {
		t.Fatal("explicit settling window ended early")
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
