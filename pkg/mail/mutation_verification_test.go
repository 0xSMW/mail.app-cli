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

func TestVerifyRelocationMatchesNewLocalIDInDestination(t *testing.T) {
	item := verificationItem()
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		// The observer deliberately knows nothing about the old ID. It found
		// the same complete identity under Mail.app's newly assigned ID.
		return verificationPresence{Destination: true}, nil
	}, noVerificationPause)
	if err != nil || status != "confirmed_destination" {
		t.Fatalf("verification = (%q, %v), want confirmed_destination", status, err)
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

func TestRFCIdentityLookupWinsOverDuplicateFallback(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "osascript.log")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > \"$MAIL_APP_CLI_TEST_LOG\"\nprintf true\n"
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	t.Setenv("MAIL_APP_CLI_TEST_LOG", logPath)
	identity := StableIdentity{RFCMessageID: "<unique@example.com>", Sender: "duplicate@example.com", Subject: "duplicate", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42}
	found, err := NewClient().hasMessageIdentityForVerification("Work", "Processed", identity)
	if err != nil || !found {
		t.Fatalf("RFC lookup = (%v, %v)", found, err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), `const wanted = "\u003cunique@example.com\u003e"`) || strings.Contains(string(logged), "duplicate@example.com") {
		t.Fatalf("lookup script did not use RFC identity exclusively: %s", logged)
	}
}

func TestRFCIdentityLookupFallsBackToEnvelopeIndexAfterArchiveResolutionError(t *testing.T) {
	binDir := t.TempDir()
	osaLog := filepath.Join(t.TempDir(), "osascript.log")
	sqlLog := filepath.Join(t.TempDir(), "sqlite.log")
	osaScript := `#!/bin/sh
printf '%s\n' "$*" >> "$MAIL_APP_CLI_OSA_LOG"
case "$*" in
  *"const wanted ="*) printf '%s\n' 'mailbox not found: Archive' >&2; exit 1 ;;
  *"const accounts = mail.accounts();"*) printf '%s\n' '[{"id":"ABC","name":"Work","emailAddresses":[],"userName":"work","enabled":true}]' ;;
  *) printf '%s\n' 'unexpected osascript' >&2; exit 1 ;;
esac
`
	sqliteScript := `#!/bin/sh
printf '%s\n' "$4" >> "$MAIL_APP_CLI_SQL_LOG"
case "$4" in
  *"m.size = 42"*) printf '%s\n' '[{"ID":999}]' ;;
  *"url = 'imap://ABC/Archive' or"*) printf '%s\n' '[{"ID":7,"URL":"imap://ABC/Archive","TotalCount":1,"UnreadCount":0}]' ;;
  *) printf '%s\n' '[]' ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(osaScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "sqlite3"), []byte(sqliteScript), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	t.Setenv("MAIL_APP_CLI_OSA_LOG", osaLog)
	t.Setenv("MAIL_APP_CLI_SQL_LOG", sqlLog)

	identity := StableIdentity{RFCMessageID: "<stable@example.com>", Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42}
	found, err := NewClient().hasMessageIdentityForVerification("Work", "Archive", identity)
	if err != nil || !found {
		t.Fatalf("identity lookup = (%v, %v), want regenerated destination row", found, err)
	}
	osaCalls, err := os.ReadFile(osaLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(osaCalls), `const wanted = "\u003cstable@example.com\u003e"`) {
		t.Fatalf("RFC lookup was not attempted first: %s", osaCalls)
	}
	sqlCalls, err := os.ReadFile(sqlLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sqlCalls), "imap://ABC/Archive") || !strings.Contains(string(sqlCalls), "m.size = 42") {
		t.Fatalf("fallback did not query the literal Archive tuple: %s", sqlCalls)
	}
	if strings.Contains(string(sqlCalls), "/%/All%20Mail") {
		t.Fatalf("literal Archive should resolve before broad All Mail fallback: %s", sqlCalls)
	}
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

func TestVerifyRelocationPollsDestinationLag(t *testing.T) {
	item := verificationItem()
	calls := 0
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		calls++
		if calls < 3 {
			return verificationPresence{Source: true}, nil
		}
		return verificationPresence{Destination: true}, nil
	}, noVerificationPause)
	if err != nil || status != "confirmed_destination" || calls != 3 {
		t.Fatalf("verification = (%q, %v), calls=%d", status, err, calls)
	}
}

func TestVerifyGmailArchiveWaitsForSourceToStayRemoved(t *testing.T) {
	item := verificationItem()
	item.GmailInboxSource = true
	calls := 0
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		calls++
		if calls == len(verificationBackoff) {
			return verificationPresence{Destination: true}, nil
		}
		return verificationPresence{Source: true, Destination: true}, nil
	}, noVerificationPause)
	if err != nil || status != "confirmed_destination" || calls != len(verificationBackoff) {
		t.Fatalf("verification = (%q, %v), calls=%d; want settled Gmail destination with source absent", status, err, calls)
	}
}

func TestVerifyGmailArchiveRejectsDestinationWhileSourcePersists(t *testing.T) {
	item := verificationItem()
	item.GmailInboxSource = true
	calls := 0
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		calls++
		return verificationPresence{Source: true, Destination: true}, nil
	}, noVerificationPause)
	if status != "present_in_source" || err == nil || calls != len(verificationBackoff) {
		t.Fatalf("verification = (%q, %v), calls=%d; want persistent Gmail source rejected", status, err, calls)
	}
}

func TestVerifyGmailArchiveRejectsSourceThatReappearsAfterSync(t *testing.T) {
	item := verificationItem()
	item.GmailInboxSource = true
	calls := 0
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		calls++
		return verificationPresence{Source: calls == len(verificationBackoff), Destination: true}, nil
	}, noVerificationPause)
	if status != "present_in_source" || err == nil || calls != len(verificationBackoff) {
		t.Fatalf("verification = (%q, %v), calls=%d; want regenerated Gmail source rejected", status, err, calls)
	}
}

func TestVerifyGmailArchiveRecognizesArchiveAlias(t *testing.T) {
	item := verificationItem()
	item.GmailInboxSource = true
	item.TargetMailbox = "Archive"
	calls := 0
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		calls++
		return verificationPresence{Source: true, Destination: true}, nil
	}, noVerificationPause)
	if status != "present_in_source" || err == nil || calls != len(verificationBackoff) {
		t.Fatalf("verification = (%q, %v), calls=%d; want Gmail archive alias to require source absence", status, err, calls)
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

func TestVerifyRelocationRetriesMailboxResolutionUntilRegeneratedDestinationAppears(t *testing.T) {
	item := verificationItem()
	item.ID = "old-mail-id"
	calls := 0
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		calls++
		if calls <= 3 {
			return verificationPresence{}, notFound("mailbox", "Archive")
		}
		// Presence is identity-based; the old local Mail ID is deliberately
		// absent after Mail assigned a new ID in the destination.
		return verificationPresence{Destination: true}, nil
	}, noVerificationPause)
	if err != nil || status != "confirmed_destination" || calls != 4 {
		t.Fatalf("verification = (%q, %v), calls=%d; want retry then regenerated destination", status, err, calls)
	}
}

func TestVerifyRelocationExhaustedLookupErrorIsAppliedAndUnverified(t *testing.T) {
	calls := 0
	status, err := verifyRelocationWithLookup(context.Background(), verificationItem(), func() (verificationPresence, error) {
		calls++
		return verificationPresence{}, notFound("mailbox", "Archive")
	}, noVerificationPause)
	if status != "applied_destination_unverified" || err == nil || calls != len(verificationBackoff) {
		t.Fatalf("verification = (%q, %v), calls=%d; want exhausted applied_destination_unverified", status, err, calls)
	}
}

func TestVerifyRelocationDoesNotRetryPermanentAutomationError(t *testing.T) {
	calls := 0
	status, err := verifyRelocationWithLookup(context.Background(), verificationItem(), func() (verificationPresence, error) {
		calls++
		return verificationPresence{}, errString("jxa error: not authorized to send Apple events (-1743)")
	}, noVerificationPause)
	if status != "applied_destination_unverified" || err == nil || calls != 1 {
		t.Fatalf("verification = (%q, %v), calls=%d; want one permanent-error attempt", status, err, calls)
	}
}

func TestVerifyRelocationCancellationInterruptsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	status, err := verifyRelocationWithLookup(ctx, verificationItem(), func() (verificationPresence, error) {
		calls++
		return verificationPresence{}, notFound("mailbox", "Archive")
	}, func(ctx context.Context, delay time.Duration) error {
		cancel()
		return waitForVerificationBackoff(ctx, delay)
	})
	if status != "unknown_after_timeout" || !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("verification = (%q, %v), calls=%d; want canceled backoff", status, err, calls)
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
	if got := strings.Count(string(osaCalls), "const wanted ="); got != len(verificationBackoff) {
		t.Fatalf("RFC verification attempts = %d, want %d", got, len(verificationBackoff))
	}
	events := readJournalEvents(t, journal.Path())
	if got := events[len(events)-1]["event"]; got != "completed" {
		t.Fatalf("terminal journal event = %v, want completed for non-timeout unverified move", got)
	}
}

func TestVerifyRelocationDoesNotAcceptSourceOnlyDisappearance(t *testing.T) {
	item := verificationItem()
	item.TargetMailbox = "Processed"
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		return verificationPresence{}, nil
	}, noVerificationPause)
	if status != "applied_destination_unverified" || err == nil {
		t.Fatalf("verification = (%q, %v), want unverified failure", status, err)
	}
}

func TestVerifyRelocationRequiresGmailArchiveDestination(t *testing.T) {
	item := verificationItem()
	item.GmailInboxSource = true
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		return verificationPresence{}, nil
	}, noVerificationPause)
	if status != "applied_destination_unverified" || err == nil {
		t.Fatalf("verification = (%q, %v), want unverified without Gmail destination", status, err)
	}
}

func TestVerifyRelocationDoesNotTreatGenericAllMailAsGmailTransition(t *testing.T) {
	item := verificationItem()
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		return verificationPresence{}, nil
	}, noVerificationPause)
	if status != "applied_destination_unverified" || err == nil {
		t.Fatalf("verification = (%q, %v), want unverified generic source removal", status, err)
	}
}

func TestVerifyRelocationClassifiesAppleEventTimeoutAsUnknown(t *testing.T) {
	status, err := verifyRelocationWithLookup(context.Background(), verificationItem(), func() (verificationPresence, error) {
		return verificationPresence{}, &AutomationTimeoutError{Engine: "applescript"}
	}, noVerificationPause)
	var timeout *AutomationTimeoutError
	if status != "unknown_after_timeout" || !errors.As(err, &timeout) {
		t.Fatalf("verification = (%q, %v), want timeout unknown", status, err)
	}
}

func TestRunBatchRetainsMarkReadWhenArchiveTimesOut(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nprintf Success\n"), 0o755); err != nil {
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

func noVerificationPause(context.Context, time.Duration) error { return nil }

func withGmailCapabilityScript(t *testing.T, result string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nprintf '"+result+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
}
