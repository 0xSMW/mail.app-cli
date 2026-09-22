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

func TestSessionReusesProcessAndReleasesLock(t *testing.T) {
	log := filepath.Join(t.TempDir(), "launches")
	t.Setenv("SESSION_LAUNCH_LOG", log)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `printf 'start\n' >> "$SESSION_LAUNCH_LOG"
while IFS= read -r line; do printf '{"output":"Success"}\n'; done
`)
	s, err := newJXASession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 3; i++ {
		if out, err := s.run("'Success'", 5*time.Second); err != nil || out != "Success" {
			t.Fatalf("%q, %v", out, err)
		}
	}
	s.Close()
	data, err := os.ReadFile(log)
	if err != nil || strings.Count(string(data), "start") != 1 {
		t.Fatalf("launches: %s, %v", data, err)
	}
	s2, err := newJXASession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestSessionTimeoutKillsChildAndAllowsFreshSession(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, "while IFS= read -r line; do /bin/sleep 30; done\n")
	s, err := newJXASession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.run("slow", 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || !s.closed {
		t.Fatalf("timeout = %v, closed = %v", err, s.closed)
	}
	writeFakeOsaScript(t, "while IFS= read -r line; do printf '{\"output\":\"fresh\"}\\n'; done\n")
	s2, err := newJXASession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if out, err := s2.run("fresh", 5*time.Second); err != nil || out != "fresh" {
		t.Fatalf("recovery: %q, %v", out, err)
	}
}

func TestBatchBridgeKeepsDurablePhasesAndStopsOnUnknown(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `n=0
while IFS= read -r line; do
 n=$((n+1))
 if [ "$n" -eq 4 ]; then exit 1; fi
 printf '{"output":"Success"}\n'
done
`)
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	items := []BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX"}, {ID: "2", Account: "Work", SourceMailbox: "INBOX"}, {ID: "3", Account: "Work", SourceMailbox: "INBOX"}}
	result, err := RunBatch(NewClient(), BatchOptions{Action: "move", TargetMailbox: "Processed", MarkReadBefore: true, ReuseBridge: true, Receipt: journal}, items, MoveMutator(false))
	if !errors.Is(err, errSessionInterrupted) || result.Succeeded != 1 || len(result.Items) != 2 || result.Items[1].Status != "unknown" {
		t.Fatalf("result = %+v, %v", result, err)
	}
	events := readJournalEvents(t, journal.Path())
	if countJournalEvents(events, "mark_read_succeeded") != 2 || countJournalEvents(events, "mutation_started") != 2 || countJournalEvents(events, "mutation_succeeded") != 1 || events[len(events)-1]["event"] != "interrupted" {
		t.Fatalf("events = %+v", events)
	}
}
