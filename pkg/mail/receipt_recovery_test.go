package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeRecoveryJournal(t *testing.T, events []map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "receipt.jsonl")
	var data []byte
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		data = append(append(data, line...), '\n')
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func requireUncertainReadReceipt(t *testing.T, path string) {
	t.Helper()
	got, err := ReadRelocationReceipt(path)
	if err == nil || !strings.Contains(err.Error(), "uncertain mark-read phase") {
		t.Fatalf("pre-relocation mark uncertainty must be refused: receipt=%+v error=%v", got, err)
	}
}

func checkFinalReadReceipt(t *testing.T, receipt BatchResult, wantError string) {
	t.Helper()
	for _, format := range []string{"raw", "envelope"} {
		t.Run(format, func(t *testing.T) {
			var value any = receipt
			if format == "envelope" {
				value = map[string]any{"ok": false, "data": receipt}
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "receipt.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadRelocationReceipt(path)
			if wantError != "" {
				if err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("receipt=%+v err=%v, want %q", got, err, wantError)
				}
			} else if err != nil || len(got.Items) != 1 || got.Items[0].Status != receipt.Items[0].Status {
				t.Fatalf("receipt=%+v err=%v", got, err)
			}
		})
	}
}

func TestReadRelocationReceiptPreservesUnknownMarkPhaseInLegacyJournals(t *testing.T) {
	for _, action := range []string{"move", "archive"} {
		for _, terminal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/terminal=%t", action, terminal), func(t *testing.T) {
				item := verificationItem()
				events := []map[string]any{{"event": "started", "action": action, "matched": 1, "markReadBefore": true}, {"event": "mark_read_started", "item": item}}
				// Older journals have no explicit unknownPhase field. Their
				// phase events still distinguish a mark timeout from relocation.
				item.Status, item.Error = "unknown", "timeout"
				events = append(events, map[string]any{"event": "mark_read_failed", "item": item})
				if terminal {
					events = append(events, map[string]any{"event": "interrupted", "result": BatchResult{Action: action, MarkReadRequested: true, Matched: 1, Attempted: 1, Failed: 1, Items: []BatchItem{item}}})
				}
				requireUncertainReadReceipt(t, writeRecoveryJournal(t, events))
			})
		}
	}
}

func TestReadRelocationReceiptMarkPhaseControls(t *testing.T) {
	for _, tc := range []struct {
		name, markEvent, markStatus, mutationEvent, wantStatus, wantError string
	}{
		{name: "interrupted mark", wantError: "uncertain mark-read phase"},
		{name: "definitive mark failure", markEvent: "mark_read_failed", markStatus: "failed", wantError: "no applied or uncertain identities"},
		{name: "successful mark without relocation", markEvent: "mark_read_succeeded", wantError: "no applied or uncertain identities"},
		{name: "successful mark then uncertain relocation", markEvent: "mark_read_succeeded", mutationEvent: "mutation_started", wantStatus: "unknown"},
		{name: "successful mark then successful relocation", markEvent: "mark_read_succeeded", mutationEvent: "mutation_succeeded", wantStatus: "succeeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := verificationItem()
			events := []map[string]any{{"event": "started", "action": "move", "markReadBefore": true}, {"event": "mark_read_started", "item": item}}
			if tc.markEvent != "" {
				item.Status, item.MarkedRead = tc.markStatus, tc.markEvent == "mark_read_succeeded"
				events = append(events, map[string]any{"event": tc.markEvent, "item": item})
			}
			if tc.mutationEvent != "" {
				if tc.mutationEvent == "mutation_succeeded" {
					item.Status = "succeeded"
				}
				events = append(events, map[string]any{"event": tc.mutationEvent, "item": item})
			}
			got, err := ReadRelocationReceipt(writeRecoveryJournal(t, events))
			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("receipt=%+v err=%v, want %q", got, err, tc.wantError)
				}
			} else if err != nil || len(got.Items) != 1 || got.Items[0].Status != tc.wantStatus {
				t.Fatalf("receipt=%+v err=%v", got, err)
			}
		})
	}
	item := verificationItem()
	item.Status = "unknown"
	// Final receipts predating explicit phase information remain readable.
	checkFinalReadReceipt(t, BatchResult{Action: "move", Items: []BatchItem{item}}, "")
}

func TestRunBatchPreliminaryMarkFailureReceiptFormats(t *testing.T) {
	for _, tc := range []struct {
		name, mode, wantStatus string
		skippedArchive         bool
	}{
		{name: "mark timeout", mode: "timeout", wantStatus: "unknown"},
		{name: "definitive mark failure", mode: "failure", wantStatus: "failed"},
		{name: "mark success", mode: "success", wantStatus: "succeeded"},
		{name: "mark completion durability failure", mode: "durability", wantStatus: "unknown"},
		{name: "skipped archive mark timeout", mode: "timeout", wantStatus: "unknown", skippedArchive: true},
		{name: "skipped archive mark completion durability failure", mode: "durability", wantStatus: "unknown", skippedArchive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			script := `#!/bin/sh
case "$*" in
 *rfcMessageId*) printf '%s\n' '{"id":"1","rfcMessageId":"<stable@example.test>","sender":"sender@example.com","subject":"subject","dateSent":"2026-08-29T00:00:00Z","messageSize":42}' ;;
 *) case "$MARK_RESULT" in
 timeout) printf 'execution error: mark timed out (-1712)' >&2; exit 1 ;;
 failure) printf 'execution error: mark refused' >&2; exit 1 ;;
 durability) printf ready > "$MARK_READY"; IFS= read -r release < "$MARK_RELEASE"; printf Success ;;
 success) printf Success ;;
 *) exit 1 ;;
 esac ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			t.Setenv("MARK_RESULT", tc.mode)
			t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
			journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			defer journal.Close()
			var durabilityDone chan error
			if tc.mode == "durability" {
				// Handshake with the fake transport: close the journal only
				// after mark dispatch and before its successful reply arrives.
				ready, release := filepath.Join(t.TempDir(), "ready"), filepath.Join(t.TempDir(), "release")
				for _, path := range []string{ready, release} {
					if err := syscall.Mkfifo(path, 0600); err != nil {
						t.Fatal(err)
					}
				}
				t.Setenv("MARK_READY", ready)
				t.Setenv("MARK_RELEASE", release)
				durabilityDone = make(chan error, 1)
				go func() {
					if _, err := os.ReadFile(ready); err != nil {
						durabilityDone <- err
						return
					}
					if err := journal.Close(); err != nil {
						durabilityDone <- err
						return
					}
					durabilityDone <- os.WriteFile(release, []byte("release\n"), 0600)
				}()
			}
			item := verificationItem()
			item.ID = "1"
			opts := BatchOptions{Action: "move", MarkReadBefore: true, Verify: tc.mode != "success", Receipt: journal}
			if tc.skippedArchive {
				opts.Action, opts.TrustSource, opts.MarkReadOnSkip = "archive", true, true
				item.SourceMailbox = "All Mail"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			mutations := 0
			result, runErr := RunBatch(NewClient().WithContext(ctx), opts, []BatchItem{item}, func(*Client, *BatchItem) error { mutations++; return nil })
			if durabilityDone != nil {
				select {
				case err := <-durabilityDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("mark transport handshake did not complete")
				}
			}
			wantMutations := 0
			if tc.mode == "success" {
				wantMutations = 1
			}
			if (runErr != nil) != (tc.mode != "success") || len(result.Items) != 1 || result.Items[0].Status != tc.wantStatus || mutations != wantMutations {
				t.Fatalf("mutations=%d result=%+v err=%v", mutations, result, runErr)
			}
			events := readJournalEvents(t, journal.Path())
			if countJournalEvents(events, "mark_read_started") != 1 || countJournalEvents(events, "mutation_started") != wantMutations || countJournalEvents(events, "mutation_succeeded") != wantMutations {
				t.Fatalf("wrong phase ordering: %+v", events)
			}
			wantError := ""
			if tc.wantStatus == "unknown" {
				if result.Items[0].UnknownPhase != "mark_read" {
					t.Fatalf("unknown mark lost phase: %+v", result.Items[0])
				}
				if result.Items[0].MarkedRead != (tc.mode == "durability") {
					t.Fatalf("mark acknowledgment lost: %+v", result.Items[0])
				}
				wantError = "uncertain mark-read phase"
				requireUncertainReadReceipt(t, journal.Path())
				// A nil client proves in-memory callers are refused before any
				// backend access, just like callers loading a saved receipt.
				if _, err := ReconcileMutations(nil, BatchOptions{}, result); err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("in-memory unknown mark was not refused: %v", err)
				}
			} else if tc.wantStatus == "failed" {
				wantError = "no applied or uncertain identities"
			}
			checkFinalReadReceipt(t, result, wantError)
		})
	}
}
