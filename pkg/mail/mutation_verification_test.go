package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVerificationObservationCountsElapsedQueryTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// The query has already consumed the full interval. Adding another wait
	// would exhaust the budget even though the next observation is due now.
	if err := waitForVerificationObservation(ctx, time.Now().Add(-time.Second), time.Second); err != nil {
		t.Fatalf("elapsed observation waited again: %v", err)
	}
}

func TestVerificationObservationRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForVerificationObservation(ctx, time.Now().Add(-time.Second), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("elapsed observation ignored cancellation: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := waitForVerificationObservation(ctx, time.Now(), time.Second); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("future observation ignored deadline: %v", err)
	}
}

func TestStableIdentityAcceptsOnlyRFCFormattedHeader(t *testing.T) {
	if got := validRFCMessageID("<stable@example.com>"); got != "<stable@example.com>" {
		t.Fatalf("valid RFC Message-ID = %q", got)
	}
	if got := validRFCMessageID("12345"); got != "" {
		t.Fatalf("numeric local id accepted as RFC Message-ID: %q", got)
	}
}

func TestCompleteStableIdentityKeepsFallbackWhenRFCEnrichmentTimesOut(t *testing.T) {
	fallback := StableIdentity{Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42}
	got, err := completeStableIdentity(fallback, nil, &AutomationTimeoutError{Engine: "jxa", Timeout: 30 * time.Second})
	if err != nil || got != fallback {
		t.Fatalf("complete identity = (%+v, %v), want fallback preserved", got, err)
	}
}

func TestDryRunVerifySkipsLiveIdentityCapture(t *testing.T) {
	binDir := t.TempDir()
	called := filepath.Join(t.TempDir(), "called")
	script := "#!/bin/sh\ntouch \"$MAIL_APP_CLI_TEST_CALLED\"\nprintf null\n"
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_TEST_CALLED", called)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	result, err := RunBatch(NewClient(), BatchOptions{Action: "move", TargetMailbox: "Processed", DryRun: true, Verify: true}, []BatchItem{verificationItem()}, func(*Client, *BatchItem) error {
		t.Fatal("dry run called mutator")
		return nil
	})
	if err != nil || len(result.Items) != 1 || result.Items[0].Status != "dry-run" {
		t.Fatalf("dry run result = %+v, %v", result, err)
	}
	if _, err := os.Stat(called); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry run called osascript: %v", err)
	}
}

func TestRFCOnlyIdentityRefusesMailboxWideLookup(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "osascript.log")
	script := "#!/bin/sh\nprintf '%s\n' \"$*\" >> \"$MAIL_APP_CLI_TEST_LOG\"\nprintf true\n"
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	t.Setenv("MAIL_APP_CLI_TEST_LOG", logPath)
	previous := verificationBackoff
	verificationBackoff = []time.Duration{0}
	t.Cleanup(func() { verificationBackoff = previous })
	item := BatchItem{ID: "1", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Processed", Status: "succeeded", Identity: StableIdentity{RFCMessageID: "<unique@example.com>"}}
	got := VerifyMutations(NewClient(), BatchOptions{Action: "move"}, []BatchItem{item})[0]
	if got.VerifyStatus != "applied_destination_unverified" || got.VerifyError == "" {
		t.Fatalf("RFC-only verification accepted insufficient identity: %+v", got)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, broad := range []string{"mbox.messages()", "mbox.messages.id()", "msg.content()"} {
		if strings.Contains(string(logged), broad) {
			t.Fatalf("RFC-only verification requested %s: %s", broad, logged)
		}
	}
}

func TestCompleteIdentityUsesEnvelopeIndexWithoutRFCMailboxScan(t *testing.T) {
	client := fakeRelocationIndex(t, "INBOX", "Archive", "imap://ABC/INBOX", "[]", "["+fakeIdentityRow+"]", false)
	item := verificationItem()
	item.TargetMailbox, item.Status = "Archive", "succeeded"
	item.Identity.RFCMessageID = "<stable@example.com>"
	got := VerifyMutations(client, BatchOptions{Action: "move"}, []BatchItem{item})[0]
	if got.VerifyStatus != "confirmed_destination" || got.Observed == nil || len(got.Observed.DestinationIDs) != 1 || got.Observed.DestinationIDs[0] != "205" {
		t.Fatalf("complete identity did not use scoped index snapshot: %+v", got)
	}
	// The fixture rejects every bridge request, so this success also proves
	// indexed metadata did not cause an RFC header or mailbox enumeration.
}

func TestArchiveSourceResolutionPreservesExplicitNonGmailWithoutIndex(t *testing.T) {
	withGmailCapabilityScript(t, "false")
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	items := NewClient().archiveSources([]BatchItem{{ID: "1", Account: "iCloud", SourceMailbox: "Archive"}}, true)
	if len(items) != 1 || items[0].Status != "" || items[0].SourceMailbox != "Archive" {
		t.Fatalf("items = %+v, want explicit non-Gmail source retained", items)
	}
}

func TestArchiveSourceResolutionFailsClosedForGmailWithoutIndex(t *testing.T) {
	withGmailCapabilityScript(t, "true")
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	items := NewClient().archiveSources([]BatchItem{{ID: "1", Account: "Klu", SourceMailbox: "Important"}}, true)
	if len(items) != 1 || items[0].Status != "failed" || !strings.Contains(items[0].Error, "Gmail") {
		t.Fatalf("items = %+v, want Gmail label resolution failure", items)
	}
}

func TestArchiveLocationUsesInboxForGmailImportantAndInbox(t *testing.T) {
	item := archiveItemFromLocation(BatchItem{ID: "1", Account: "Klu", SourceMailbox: "Important"}, MessageLocation{Account: "Klu", ArchiveMailbox: archiveSourceMailbox("All Mail", []string{"Important", "INBOX"}), IsGmail: true})
	if item.SourceMailbox != "INBOX" || !item.GmailInboxSource {
		t.Fatalf("archive item = %+v, want Gmail INBOX source", item)
	}
}

func TestGmailArchiveSyncTimeoutIsUnknown(t *testing.T) {
	binDir := t.TempDir()
	script := "#!/bin/sh\nprintf '%s\\n' 'execution error: AppleEvent timed out. (-1712)' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	item := verificationItem()
	item.GmailInboxSource = true
	status, err := VerifyMutation(NewClient(), BatchOptions{Action: "archive"}, item)
	if status != "unknown_after_timeout" || err == nil {
		t.Fatalf("verification = (%q, %v), want unknown_after_timeout", status, err)
	}
}

func TestRunBatchKeepsSuccessfulMoveSeparateFromUnverifiedDestination(t *testing.T) {
	binDir := t.TempDir()
	osaLog := filepath.Join(t.TempDir(), "osascript.log")
	osaScript := `#!/bin/sh
printf '%s\n' "$*" >> "$MAIL_APP_CLI_OSA_LOG"
case "$*" in
  *"const allIds = mbox.messages.id();"*) printf '%s\n' '{"id":"old","rfcMessageId":"<stable@example.com>","sender":"sender@example.com","subject":"subject","dateSent":"2026-08-29T00:00:00Z","messageSize":42,"mailbox":"INBOX","account":"Work"}' ;;
  *"const wanted ="*) printf '%s\n' 'mailbox not found: Archive' >&2; exit 1 ;;
  *"const accounts = mail.accounts();"*) printf '%s\n' '[{"id":"ABC","name":"Work","emailAddresses":[],"userName":"work","enabled":true}]' ;;
  *) printf '%s\n' 'unexpected osascript' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(osaScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sqlite3"), []byte("#!/bin/sh\nprintf '%s\\n' '[]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	t.Setenv("MAIL_APP_CLI_OSA_LOG", osaLog)
	previousBackoff := verificationBackoff
	verificationBackoff = []time.Duration{0, 0, 0}
	t.Cleanup(func() { verificationBackoff = previousBackoff })
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()

	result, err := RunBatch(NewClient(), BatchOptions{Action: "move", TargetMailbox: "Archive", Verify: true, Receipt: journal, TrustSource: true}, []BatchItem{{
		ID: "old", Account: "Work", SourceMailbox: "INBOX", Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42,
	}}, func(*Client, *BatchItem) error { return nil })
	var batchErr *BatchFailedError
	if !errors.As(err, &batchErr) || batchErr.Unverified != 1 {
		t.Fatalf("RunBatch error = %#v, want one inconclusive verification", err)
	}
	if result.Succeeded != 1 || result.Failed != 0 || result.Unverified != 1 || len(result.Items) != 1 {
		t.Fatalf("result counts = %+v, want applied move separated from verification", result)
	}
	item := result.Items[0]
	if item.Status != "succeeded" || item.VerifyStatus != "applied_destination_unverified" || item.VerifyError == "" {
		t.Fatalf("item = %+v, want succeeded mutation with unverified destination", item)
	}
	osaCalls, err := os.ReadFile(osaLog)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(osaCalls), "const messages = mbox.messages()"); got != 0 {
		t.Fatalf("mailbox-wide verification attempts = %d, want none", got)
	}
	events := readJournalEvents(t, journal.Path())
	if got := events[len(events)-1]["event"]; got != "completed" {
		t.Fatalf("terminal journal event = %v, want completed for non-timeout unverified move", got)
	}
}

func TestRunBatchRetainsMarkReadWhenArchiveTimesOut(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\ncase \"$*\" in *mail-app-cli-native-archive-account*) printf true ;; *AXIsProcessTrusted*) printf ready ;; *) printf Success ;; esac\n"), 0o755); err != nil {
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
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", MarkReadBefore: true, Receipt: journal, TrustSource: true}, []BatchItem{verificationItem()}, func(*Client, *BatchItem) error {
		return &AutomationTimeoutError{Engine: "applescript"}
	})
	if err == nil || len(result.Items) != 1 || !result.Items[0].MarkedRead || result.Items[0].Status != "unknown" {
		t.Fatalf("result=%+v err=%v, want marked-read unknown archive", result, err)
	}
	events := readJournalEvents(t, journalPath)
	if countJournalEvents(events, "mark_read_succeeded") != 1 || countJournalEvents(events, "mutation_unknown") != 1 {
		t.Fatalf("events = %#v", events)
	}
}

func verificationItem() BatchItem {
	return BatchItem{ID: "old", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "All Mail", Identity: StableIdentity{Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42}}
}

func withGmailCapabilityScript(t *testing.T, result string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nprintf '"+result+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
}

func TestArchiveVerificationReportsRegeneratedSourceCopy(t *testing.T) {
	// The archived message left local ID 100. Mail regenerated it in INBOX
	// as 205, and All Mail holds the read copy as 204.
	source := "[" + strings.Replace(fakeIdentityRow, `"Read":1`, `"Read":0`, 1) + "]"
	destination := "[" + strings.Replace(fakeIdentityRow, `"ID":205`, `"ID":204`, 1) + "]"
	client := fakeRelocationIndex(t, "INBOX", "All Mail", "imap://ABC/INBOX", source, destination, false)
	bin := strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0]
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nprintf Success\n"), 0755); err != nil {
		t.Fatal(err)
	}

	item := verificationItem()
	item.ID, item.Status, item.TargetMailbox, item.GmailInboxSource = "100", "succeeded", "All Mail", true
	verified := VerifyMutations(client, BatchOptions{Action: "archive", Verify: true}, []BatchItem{item})
	got := verified[0]
	if got.VerifyStatus != "present_in_source" || got.Observed == nil {
		t.Fatalf("item = %+v", got)
	}
	state := got.Observed
	if !state.SourcePresent || !state.DestinationPresent || state.DestinationRead == nil || !*state.DestinationRead {
		t.Fatalf("observed = %+v", state)
	}
	if len(state.SourceIDs) != 1 || state.SourceIDs[0] != "205" || len(state.DestinationIDs) != 1 || state.DestinationIDs[0] != "204" {
		t.Fatalf("observed IDs = %+v, want the regenerated local IDs", state)
	}
}
