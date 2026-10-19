package mail

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeRelocationIndex(t *testing.T, source, target, actualSourceURL, sourceRows, destinationRows string, recur bool) *Client {
	t.Helper()
	bin := t.TempDir()
	counter := filepath.Join(t.TempDir(), "source-count")
	// Return the two scopes from the same synthetic observation, in whichever
	// order the production verifier requested them.
	snapshotRows := func(sourceFirst, sourcePresent bool) string {
		sourceData := sourceRows
		if !sourcePresent {
			sourceData = "[]"
		}
		rows := []map[string]any{
			{"VerificationScope": 0, "MailboxID": 1, "MailboxURL": actualSourceURL, "Matches": sourceData},
			{"VerificationScope": 1, "MailboxID": 2, "MailboxURL": indexMailboxURLPattern("ABC", target), "Matches": destinationRows},
		}
		if !sourceFirst {
			rows[0], rows[1] = rows[1], rows[0]
			rows[0]["VerificationScope"], rows[1]["VerificationScope"] = 0, 1
		}
		data, err := json.Marshal(rows)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	snapshotScript := fmt.Sprintf(`
 *"with verification_mailbox_0"*)
 n=0; if [ -f '%s' ]; then read -r n < '%s'; fi
 n=$((n+1)); printf '%%s\n' "$n" > '%s'
 prefix=${4%%%%verification_mailbox_1*}
 case "$prefix" in *"%s"*)
 if [ '%t' = true ] && [ "$n" -lt 3 ]; then printf '%%s\n' '%s'; else printf '%%s\n' '%s'; fi ;;
 *)
 if [ '%t' = true ] && [ "$n" -lt 3 ]; then printf '%%s\n' '%s'; else printf '%%s\n' '%s'; fi ;;
 esac ;;
`, counter, counter, counter, sqlQuote(strings.TrimPrefix(indexMailboxURLPattern("ABC", source), "imap://ABC/")), recur, snapshotRows(true, false), snapshotRows(true, true), recur, snapshotRows(false, false), snapshotRows(false, true))
	script := fmt.Sprintf(`#!/bin/sh
case "$4" in
%s
 *"m.size = 42"*"m.mailbox = 1"*|*"m.mailbox = 1"*"m.size = 42"*)
 n=0; if [ -f '%s' ]; then read -r n < '%s'; fi
 n=$((n+1)); printf '%%s\n' "$n" > '%s'
 if [ '%t' = true ] && [ "$n" -lt 3 ]; then printf '[]\n'; else printf '%%s\n' '%s'; fi ;;
 *"m.size = 42"*) printf '%%s\n' '%s' ;;
 *"%s"*) printf '%%s\n' '[{"ID":1,"URL":"%s","TotalCount":1,"UnreadCount":0}]' ;;
 *"%s"*) printf '%%s\n' '[{"ID":2,"URL":"%s","TotalCount":1,"UnreadCount":0}]' ;;
 *) printf '[]\n' ;;
esac
`, snapshotScript, counter, counter, counter, recur, sourceRows, destinationRows, sqlQuote(strings.TrimPrefix(indexMailboxURLPattern("ABC", source), "imap://ABC/")), actualSourceURL, sqlQuote(strings.TrimPrefix(indexMailboxURLPattern("ABC", target), "imap://ABC/")), indexMailboxURLPattern("ABC", target))
	if err := os.WriteFile(filepath.Join(bin, "sqlite3"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	// No real automation can run; unexpected bridge work is a test failure.
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nprintf 'unexpected bridge' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	previous := verificationBackoff
	verificationBackoff = []time.Duration{0, 0, 0}
	t.Cleanup(func() { verificationBackoff = previous })
	client := NewClient()
	client.PrimeAccounts([]Account{{ID: "ABC", Name: "Work"}})
	return client
}

const fakeIdentityRow = `{"ID":205,"Subject":"subject","Sender":"sender@example.com","DateSent":"2026-08-29T00:00:00Z","MessageSize":42,"Read":1}`

func TestRelocationProviderIntentMembership(t *testing.T) {
	for _, tc := range []struct {
		name, source, actualURL string
		want                    string
	}{
		{"Gmail restore retains All Mail", "All Mail", "imap://ABC/%5BGmail%5D/All%20Mail", "confirmed_destination"},
		{"generic All Mail is removed", "All Mail", "imap://ABC/All%20Mail", "present_in_source"},
		{"Gmail Spam is removed", "Spam", "imap://ABC/%5BGmail%5D/Spam", "present_in_source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fakeRelocationIndex(t, tc.source, "INBOX", tc.actualURL, "["+fakeIdentityRow+"]", "["+fakeIdentityRow+"]", false)
			item := verificationItem()
			item.SourceMailbox, item.TargetMailbox, item.Status = tc.source, "INBOX", "succeeded"
			// Even a forged transition flag cannot waive removal for generic/Spam.
			item.GmailInboxSource = true
			got := VerifyMutations(client, BatchOptions{Action: "move"}, []BatchItem{item})[0]
			if got.VerifyStatus != tc.want {
				t.Fatalf("got %+v, want %s", got, tc.want)
			}
			status, err := VerifyMutation(client, BatchOptions{Action: "move"}, item)
			if status != tc.want || (err == nil) != (tc.want == "confirmed_destination") {
				t.Fatalf("single=(%s,%v), grouped=%+v", status, err, got)
			}
			if got.Observed == nil || got.Observed.ObservedAt == "" || got.Observed.Account != "Work" || len(got.Observed.SourceIDs) != 1 {
				t.Fatalf("missing observed scope/IDs: %+v", got.Observed)
			}
		})
	}
}

func TestRelocationFinalObservationRejectsLateSourceRecurrence(t *testing.T) {
	client := fakeRelocationIndex(t, "INBOX", "Processed", "imap://ABC/INBOX", "["+fakeIdentityRow+"]", "["+fakeIdentityRow+"]", true)
	item := verificationItem()
	item.TargetMailbox, item.Status = "Processed", "succeeded"
	got := VerifyMutations(client, BatchOptions{Action: "move"}, []BatchItem{item})[0]
	if got.VerifyStatus != "present_in_source" || got.Observed == nil || !got.Observed.DestinationPresent {
		t.Fatalf("late recurrence accepted: %+v", got)
	}
}

func TestRelocationRequestedDestinationRead(t *testing.T) {
	for _, tc := range []struct{ name, rows, want string }{
		{"read", "[" + fakeIdentityRow + "]", "confirmed_destination"},
		{"unread", "[" + strings.Replace(fakeIdentityRow, `"Read":1`, `"Read":0`, 1) + "]", "destination_read_unverified"},
		{"mixed", "[" + fakeIdentityRow + "," + strings.Replace(fakeIdentityRow, `"Read":1`, `"Read":0`, 1) + "]", "applied_destination_unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fakeRelocationIndex(t, "INBOX", "Processed", "imap://ABC/INBOX", "[]", tc.rows, false)
			item := verificationItem()
			item.TargetMailbox, item.Status, item.MarkedRead = "Processed", "succeeded", true
			got := VerifyMutations(client, BatchOptions{Action: "move", MarkReadBefore: true}, []BatchItem{item})[0]
			if got.VerifyStatus != tc.want {
				t.Fatalf("got %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestBatchIdentitySessionReadsOnlyMetadata(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "requests")
	launches := filepath.Join(t.TempDir(), "launches")
	script := fmt.Sprintf(`#!/bin/sh
printf 'start\n' >> '%s'
while IFS= read -r line; do
 printf '%%s\n' "$line" >> '%s'
 printf '%%s\n' '{"output":"{\"id\":\"1\",\"rfcMessageId\":\"<stable@example.test>\",\"sender\":\"sender@example.com\",\"subject\":\"subject\",\"dateSent\":\"2026-08-29T00:00:00Z\",\"messageSize\":42}"}'
done
`, launches, log)
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
	items := make([]BatchItem, 25)
	for i := range items {
		items[i] = verificationItem()
		items[i].ID = fmt.Sprint(i + 1)
	}
	if err := captureBatchIdentities(NewClient(), BatchOptions{Action: "move", ReuseBridge: true, Receipt: journal}, items); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(launches)
	if err != nil || strings.Count(string(data), "start") != 3 {
		t.Fatalf("launches=%s err=%v; want 3 for 25 serial requests", data, err)
	}
	data, err = os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Fields(string(data)) {
		decoded, err := base64.StdEncoding.DecodeString(line)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"msg.content()", "toRecipients()", "ccRecipients()", "bccRecipients()", "mbox.messages.id()"} {
			if strings.Contains(string(decoded), forbidden) {
				t.Fatalf("identity requested %s", forbidden)
			}
		}
	}
	if countJournalEvents(readJournalEvents(t, journal.Path()), "identity_capture_result") != 25 {
		t.Fatal("identity capture receipts missing")
	}
}

func TestBatchOptionsRejectBeforeMutation(t *testing.T) {
	for _, opts := range []BatchOptions{{Action: "archive", MarkReadOnSkip: true}, {Action: "move", Settle: -time.Second}, {Action: "move", Settle: time.Minute, VerifyBudget: time.Second}} {
		mutated := false
		_, err := RunBatch(NewClient(), opts, []BatchItem{verificationItem()}, func(*Client, *BatchItem) error { mutated = true; return nil })
		if err == nil || mutated {
			t.Fatalf("opts %+v mutated=%t err=%v", opts, mutated, err)
		}
	}
	if got := verificationSchedule(10 * time.Second); len(got) <= len(verificationBackoff) {
		t.Fatalf("window was not extended: %v", got)
	}
}

func TestReconcileReceiptDoesNotSyncOrMutate(t *testing.T) {
	client := fakeRelocationIndex(t, "INBOX", "All Mail", "imap://ABC/INBOX", "[]", "["+fakeIdentityRow+"]", false)
	item := verificationItem()
	item.GmailInboxSource, item.Status = true, "unknown"
	receipt := BatchResult{Action: "archive", Attempted: 1, MarkReadRequested: true, Items: []BatchItem{item}}
	got, err := ReconcileMutations(client, BatchOptions{}, receipt)
	if err != nil || got.Items[0].VerifyStatus != "confirmed_destination" || got.Items[0].Status != "unknown" {
		t.Fatalf("reconcile=%+v err=%v", got, err)
	}
}

func TestArchiveSkipMarkReadOptInAndFailureReceipts(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		optIn, failMark            bool
		wantUnverified, wantFailed int
	}{
		{name: "default skip"},
		{name: "opt in mark has unread observation", optIn: true, wantUnverified: 1},
		{name: "opt in mark fails", optIn: true, failMark: true, wantFailed: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			log := filepath.Join(t.TempDir(), "bridge")
			script := fmt.Sprintf(`#!/bin/sh
printf 'call\n' >> '%s'
case "$*" in
 *"const matches = mbox.messages.whose"*) printf '%%s\n' '{"id":"1","read":false}' ;;
 *) if [ '%t' = true ]; then printf 'mark failed' >&2; exit 1; fi; printf Success ;;
esac
`, log, tc.failMark)
			if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
			prev := verificationBackoff
			verificationBackoff = []time.Duration{0, 0}
			t.Cleanup(func() { verificationBackoff = prev })
			journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			opts := BatchOptions{Action: "archive", TrustSource: true, MarkReadBefore: true, MarkReadOnSkip: tc.optIn, Receipt: journal, Verify: true}
			result, err := RunBatch(NewClient(), opts, []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "All Mail"}}, func(*Client, *BatchItem) error { t.Fatal("archive mutation on skipped item"); return nil })
			if (err != nil) != (tc.wantFailed+tc.wantUnverified > 0) || result.Attempted != 0 || result.Skipped != 1 || result.Unverified != tc.wantUnverified || result.Failed != tc.wantFailed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			item := result.Items[0]
			if item.MarkedRead != (tc.optIn && !tc.failMark) {
				t.Fatalf("markedRead=%t result=%+v", item.MarkedRead, result)
			}
			if !tc.optIn {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("default skip called Mail: %v", err)
				}
			}
			events := readJournalEvents(t, journal.Path())
			if countJournalEvents(events, "mutation_started") != 0 {
				t.Fatal("skipped archive journal recorded a mutation")
			}
			if tc.optIn && countJournalEvents(events, "mark_read_started") != 1 {
				t.Fatal("missing durable started mark-read")
			}
			if tc.failMark && countJournalEvents(events, "mark_read_failed") != 1 {
				t.Fatal("missing durable failed mark-read")
			}
		})
	}
}

func TestArchiveDryRunSkipReportsMarkReadOptIn(t *testing.T) {
	result, err := RunBatch(NewClient(), BatchOptions{Action: "archive", DryRun: true, TrustSource: true, MarkReadBefore: true, MarkReadOnSkip: true}, []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "All Mail"}}, func(*Client, *BatchItem) error { t.Fatal("dry-run mutation"); return nil })
	if err != nil || len(result.Items) != 1 || !result.Items[0].MarkReadOnSkip || result.Items[0].MarkedRead {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.Summary(BatchOptions{Action: "archive", MarkReadOnSkip: true}), "would mark read") {
		t.Fatal("dry-run summary omits opt-in read change")
	}
}

func TestIdentityEnrichmentTimeoutKeepsFallbackAndIsolatesNextRead(t *testing.T) {
	bin := t.TempDir()
	launches := filepath.Join(t.TempDir(), "launches")
	script := fmt.Sprintf(`#!/bin/sh
printf 'start\n' >> '%s'
while IFS= read -r line; do printf '%%s\n' '{"error":"AppleEvent timed out (-1712)"}'; done
`, launches)
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
	items := []BatchItem{verificationItem(), {ID: "2", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Processed"}}
	if err := captureBatchIdentities(NewClient(), BatchOptions{Action: "move", ReuseBridge: true, Receipt: journal}, items); err != nil {
		t.Fatal(err)
	}
	if !items[0].Identity.valid() || items[0].Status != "" || items[0].IdentityEnrichmentError == "" {
		t.Fatalf("complete fallback discarded: %+v", items[0])
	}
	if items[1].Status != "failed" || items[1].Identity.valid() {
		t.Fatalf("unidentified item allowed: %+v", items[1])
	}
	data, err := os.ReadFile(launches)
	if err != nil || strings.Count(string(data), "start") != 2 {
		t.Fatalf("next ID did not use isolated session, launches=%s err=%v", data, err)
	}
}

func TestReconcileRejectsUnsupportedOrMismatchedReceiptsBeforeRead(t *testing.T) {
	for _, tc := range []struct {
		opts    BatchOptions
		receipt BatchResult
	}{
		{receipt: BatchResult{Action: "mark"}},
		{receipt: BatchResult{Action: "move", DryRun: true}},
		{opts: BatchOptions{Action: "archive"}, receipt: BatchResult{Action: "move"}},
		{receipt: BatchResult{Action: "move", Items: []BatchItem{{ID: "1", Status: "succeeded", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Processed"}}}},
		{receipt: BatchResult{Action: "archive", Items: []BatchItem{{ID: "1", Status: "unknown", Identity: verificationItem().Identity}}}},
	} {
		if _, err := ReconcileMutations(NewClient(), tc.opts, tc.receipt); err == nil {
			t.Fatalf("receipt accepted: %+v opts=%+v", tc.receipt, tc.opts)
		}
	}
}
