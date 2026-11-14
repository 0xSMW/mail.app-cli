package mail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBatchRepeatedFailuresStopAndRetainUnattempted(t *testing.T) {
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	items := make([]BatchItem, 7)
	for i := range items {
		items[i] = BatchItem{ID: fmt.Sprint(i + 1), Account: "Work", SourceMailbox: "INBOX"}
	}
	calls := 0
	result, err := RunBatch(NewClient(), BatchOptions{Action: "mark", Receipt: journal, ChunkSize: 2, MaxConsecutiveFailures: 3}, items, func(*Client, *BatchItem) error { calls++; return errors.New("selection refused") })
	var failed *BatchFailedError
	if !errors.As(err, &failed) || calls != 3 || result.Attempted != 3 || result.Failed != 3 || result.NotAttempted != 4 || len(result.Items) != 7 {
		t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
	}
	for _, item := range result.Items[3:] {
		if item.Status != "not_attempted" {
			t.Fatalf("pending=%+v", item)
		}
	}
	events := readJournalEvents(t, journal.Path())
	if countJournalEvents(events, "mutation_started") != 3 || countJournalEvents(events, "not_attempted") != 4 || events[len(events)-1]["event"] != "stopped" {
		t.Fatalf("events=%+v", events)
	}
	if !strings.Contains(result.Summary(BatchOptions{}), "4 not attempted") {
		t.Fatal(result.Summary(BatchOptions{}))
	}
}

func TestBatchFailureRunsAreScopedAndReset(t *testing.T) {
	for _, tc := range []struct {
		name               string
		accounts, failures []string
		limit, wantCalls   int
	}{
		{"different errors", []string{"A", "A", "A", "A"}, []string{"one", "two", "one", "two"}, 2, 4},
		{"success resets", []string{"A", "A", "A", "A"}, []string{"one", "", "one", ""}, 2, 4},
		{"accounts separate", []string{"A", "B", "A", "B", "A"}, []string{"one", "one", "one", "one", "one"}, 3, 5},
		{"disabled", []string{"A", "A", "A", "A"}, []string{"one", "one", "one", "one"}, 0, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			items := make([]BatchItem, len(tc.accounts))
			for i, a := range tc.accounts {
				items[i] = BatchItem{ID: fmt.Sprint(i), Account: a, SourceMailbox: "INBOX"}
			}
			calls := 0
			result, _ := RunBatch(NewClient(), BatchOptions{Action: "mark", Receipt: journal, MaxConsecutiveFailures: tc.limit}, items, func(*Client, *BatchItem) error {
				msg := tc.failures[calls]
				calls++
				if msg != "" {
					return errors.New(msg)
				}
				return nil
			})
			if calls != tc.wantCalls || result.NotAttempted != len(items)-calls {
				t.Fatalf("calls=%d result=%+v", calls, result)
			}
		})
	}
}

func TestStoppedBatchStillVerifiesAppliedItems(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$4" in
 *"m.ROWID in (1)"*) printf '[{"ID":1,"Read":1}]\n' ;;
 *"from mailboxes"*) printf '[{"ID":9,"URL":"imap://ABC/INBOX","TotalCount":1,"UnreadCount":0}]\n' ;;
 *) printf 'unexpected query' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "sqlite3"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	old := verificationBackoff
	verificationBackoff = []time.Duration{0}
	t.Cleanup(func() { verificationBackoff = old })
	client := NewClient()
	client.PrimeAccounts([]Account{{ID: "ABC", Name: "Work"}})
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	items := make([]BatchItem, 5)
	for i := range items {
		items[i] = BatchItem{ID: fmt.Sprint(i + 1), Account: "Work", SourceMailbox: "INBOX"}
	}
	result, err := RunBatch(client, BatchOptions{Action: "mark", Read: true, Verify: true, Receipt: journal, MaxConsecutiveFailures: 2}, items, func(_ *Client, item *BatchItem) error {
		if item.ID == "1" {
			return nil
		}
		return errors.New("selection refused")
	})
	if err == nil || result.Items[0].VerifyStatus != "matched" || result.Unverified != 0 || result.NotAttempted != 2 {
		b, _ := json.Marshal(result)
		t.Fatalf("result=%s err=%v", b, err)
	}
}

func TestBatchEventsMirrorDurableReceipt(t *testing.T) {
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	var events bytes.Buffer
	result, err := RunBatch(NewClient(), BatchOptions{Action: "mark", Receipt: journal, Events: &events}, []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX"}}, func(*Client, *BatchItem) error { return nil })
	if err != nil || result.Succeeded != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	durable, err := os.ReadFile(journal.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(events.Bytes(), durable) {
		t.Fatalf("events did not mirror synced receipt: %s", events.String())
	}
}
