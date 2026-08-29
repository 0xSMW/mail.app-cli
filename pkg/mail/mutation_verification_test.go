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
	}, func(time.Duration) {})
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
	}, func(time.Duration) {})
	if err != nil || status != "confirmed_destination" || calls != 3 {
		t.Fatalf("verification = (%q, %v), calls=%d", status, err, calls)
	}
}

func TestVerifyRelocationDoesNotAcceptSourceOnlyDisappearance(t *testing.T) {
	item := verificationItem()
	item.TargetMailbox = "Processed"
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		return verificationPresence{}, nil
	}, func(time.Duration) {})
	if status != "source_removed_destination_unverified" || err == nil {
		t.Fatalf("verification = (%q, %v), want unverified failure", status, err)
	}
}

func TestVerifyRelocationAcceptsExplicitGmailInboxLabelTransition(t *testing.T) {
	item := verificationItem()
	item.GmailInboxSource = true
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		return verificationPresence{}, nil
	}, func(time.Duration) {})
	if err != nil || status != "confirmed_source_removed" {
		t.Fatalf("verification = (%q, %v), want confirmed_source_removed", status, err)
	}
}

func TestVerifyRelocationDoesNotTreatGenericAllMailAsGmailTransition(t *testing.T) {
	item := verificationItem()
	status, err := verifyRelocationWithLookup(context.Background(), item, func() (verificationPresence, error) {
		return verificationPresence{}, nil
	}, func(time.Duration) {})
	if status != "source_removed_destination_unverified" || err == nil {
		t.Fatalf("verification = (%q, %v), want unverified generic source removal", status, err)
	}
}

func TestVerifyRelocationClassifiesAppleEventTimeoutAsUnknown(t *testing.T) {
	status, err := verifyRelocationWithLookup(context.Background(), verificationItem(), func() (verificationPresence, error) {
		return verificationPresence{}, &AutomationTimeoutError{Engine: "applescript"}
	}, func(time.Duration) {})
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

func withGmailCapabilityScript(t *testing.T, result string) {
	t.Helper()
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nprintf '"+result+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
}
