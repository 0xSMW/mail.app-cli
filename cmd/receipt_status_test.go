package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func statusLine(t *testing.T, event string, fields map[string]any) []byte {
	t.Helper()
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"] = event
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}
func statusBatch() mail.BatchResult {
	return mail.BatchResult{Action: "move", Matched: 1, Attempted: 1, Succeeded: 1, Items: []mail.BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Processed", Status: "succeeded"}}}
}
func TestReceiptStatusReadsActualBatchProducerAndFinalEnvelope(t *testing.T) {
	for _, kind := range []string{"success", "manifest-subject", "failed", "stopped", "interrupted", "verified-identity-failure"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receipt.jsonl")
			journal, err := mail.CreateBatchJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			items := []mail.BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Processed", Subject: "manifest_started"}}
			opts := mail.BatchOptions{Action: "move", TargetMailbox: "Processed", TrustSource: true, Receipt: journal}
			if kind == "stopped" {
				opts.MaxConsecutiveFailures = 1
				items = append(items, mail.BatchItem{ID: "2", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Processed"})
			}
			if kind == "verified-identity-failure" {
				installBatchVerificationScript(t, "error")
				opts.Verify = true
			}
			result, runErr := mail.RunBatch(mail.NewClient(), opts, items, func(*mail.Client, *mail.BatchItem) error {
				switch kind {
				case "failed", "stopped":
					return errors.New("controlled provider refusal")
				case "interrupted":
					return context.DeadlineExceeded
				}
				return nil
			})
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			if (kind == "success" || kind == "manifest-subject") != (runErr == nil) {
				t.Fatalf("run err=%v kind=%s", runErr, kind)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			status, err := parseReceiptStatus(data)
			if err != nil || status.Succeeded != result.Succeeded || status.Failed != result.Failed || status.NotAttempted != result.NotAttempted {
				t.Fatalf("status=%+v result=%+v err=%v\n%s", status, result, err, data)
			}
			expected := "completed"
			if kind == "stopped" {
				expected = "stopped"
			}
			if kind == "interrupted" {
				expected = "interrupted"
			}
			if status.State != expected {
				t.Fatalf("state=%s want=%s", status.State, expected)
			}
			// The same real receipt is inspectable before every completed write, and
			// terminal bytes without their newline are never promoted to completion.
			lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
			prefix := []byte{}
			for i, line := range lines {
				partial := append(append([]byte{}, prefix...), line[:len(line)/2]...)
				if i > 0 {
					live, err := parseReceiptStatus(partial)
					if err != nil || live.Complete || !live.PartialLine {
						t.Fatalf("partial at %d status=%+v err=%v", i, live, err)
					}
				}
				prefix = append(prefix, line...)
				prefix = append(prefix, '\n')
				if _, err := parseReceiptStatus(prefix); err != nil {
					t.Fatalf("producer prefix %d rejected: %v\n%s", i, err, prefix)
				}
			}
			envelope, _ := json.Marshal(map[string]any{"ok": runErr == nil, "data": result})
			final, err := parseReceiptStatus(envelope)
			if err != nil || final.Succeeded != result.Succeeded || final.Failed != result.Failed {
				t.Fatalf("final=%+v err=%v", final, err)
			}
		})
	}
}

func TestReceiptStatusAllItemPhasesAndGmailCleanupPrefixes(t *testing.T) {
	phases := []string{"identity_capture_started", "identity_capture_result", "mark_read_started", "mark_read_succeeded", "mark_read_failed", "preflight_failed", "mutation_started", "mutation_succeeded", "mutation_failed", "mutation_unknown", "verification_result", "skipped", "not_attempted", "manifest_before_read", "manifest_after_read"}
	for _, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			data := statusLine(t, "started", map[string]any{"action": "archive", "matched": 1})
			item := mail.BatchItem{ID: "1", Account: "Work", SourceMailbox: "INBOX"}
			data = append(data, statusLine(t, phase, map[string]any{"item": item})...)
			got, err := parseReceiptStatus(data)
			if err != nil || got.Phase != phase || got.State != "running_or_interrupted" {
				t.Fatalf("phase=%s status=%+v err=%v", phase, got, err)
			}
		})
	}
	prefix := statusLine(t, "label_cleanup_started", map[string]any{"action": "archive", "matched": 1, "dryRun": false})
	prefix = append(prefix, statusLine(t, "label_cleanup_result", map[string]any{"labelResults": []mail.GmailLabelResult{{Status: "failed", Account: "Work", Error: "controlled cleanup failure"}}})...)
	stopped := mail.BatchResult{Action: "archive", Matched: 1, NotAttempted: 1, StoppedReason: "cleanup failed", Items: []mail.BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX", Status: "not_attempted"}}}
	data := append(append([]byte{}, prefix...), statusLine(t, "stopped", map[string]any{"result": stopped})...)
	got, err := parseReceiptStatus(data)
	if err != nil || got.State != "stopped" || got.NotAttempted != 1 {
		t.Fatalf("cleanup stop=%+v err=%v", got, err)
	}
	data = append(prefix, statusLine(t, "started", map[string]any{"action": "archive", "matched": 1})...)
	got, err = parseReceiptStatus(data)
	if err != nil || got.Phase != "started" {
		t.Fatalf("cleanup followed by batch=%+v err=%v", got, err)
	}
}

func TestReceiptStatusRejectsMalformedCompleteRecords(t *testing.T) {
	started := statusLine(t, "started", map[string]any{"action": "move", "matched": 1})
	for _, data := range [][]byte{
		[]byte(`{"data":{}}`), []byte(`{"action":"move","matched":1}`),
		append(append([]byte{}, started...), []byte("{\"event\":\n")...),
		append(append([]byte{}, started...), statusLine(t, "completed", map[string]any{"result": map[string]any{}})...),
		append(append([]byte{}, started...), statusLine(t, "mutation_succeeded", nil)...),
		append(append([]byte{}, started...), statusLine(t, "invented", nil)...),
	} {
		if result, err := parseReceiptStatus(data); err == nil {
			t.Fatalf("accepted malformed input: %s result=%+v", data, result)
		}
	}
	invalid := statusBatch()
	invalid.Succeeded = 0
	data, _ := json.Marshal(invalid)
	if _, err := parseReceiptStatus(data); err == nil {
		t.Fatalf("accepted inconsistent final counts: %s", data)
	}
	completed := append(append([]byte{}, started...), statusLine(t, "completed", map[string]any{"result": statusBatch()})...)
	completed = append(completed, statusLine(t, "mutation_started", map[string]any{"item": statusBatch().Items[0]})...)
	if _, err := parseReceiptStatus(completed); err == nil {
		t.Fatal("accepted journal records after completion")
	}
}

func TestReceiptStatusCommandUsesOnlyLocalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.json")
	data, _ := json.Marshal(map[string]any{"data": statusBatch()})
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	a := newApplication()
	a.mailClient = nil
	var stdout, stderr bytes.Buffer
	a.writer, _ = output.New(output.FormatJSON, &stdout, &stderr, false, "", "messages status", mail.SchemaVersion)
	cmd := a.newMessagesStatusCmd()
	if err := cmd.RunE(cmd, []string{path}); err != nil {
		t.Fatal(err)
	}
	if !json.Valid(stdout.Bytes()) || !strings.Contains(stdout.String(), `"state": "completed"`) || stderr.Len() != 0 {
		t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(data, after) {
		t.Fatal("status rewrote receipt")
	}
}

func TestReceiptStatusManifestParentAndFinalEnvelope(t *testing.T) {
	child := statusBatch()
	child.ReceiptPath = "/private/fixture/child.jsonl"
	manifest := mail.CleanupManifest{Version: 1, Items: []mail.ManifestItem{{ID: "1", Account: "Work", Action: "move"}}}
	final := mail.ManifestResult{Version: 1, Matched: 1, Verified: 1, Items: []mail.ManifestOutcome{{ID: "1", Account: "Work", Action: "move", Status: "verified"}}, Batches: []mail.BatchResult{child}}
	data := statusLine(t, "manifest_started", map[string]any{"manifest": manifest, "dryRun": false})
	data = append(data, statusLine(t, "manifest_batch_started", map[string]any{"receiptPath": child.ReceiptPath, "indices": []int{0}})...)
	live, err := parseReceiptStatus(data)
	if err != nil || live.Complete || len(live.Receipts) != 1 {
		t.Fatalf("live=%+v err=%v", live, err)
	}
	data = append(data, statusLine(t, "manifest_batch_completed", map[string]any{"receiptPath": child.ReceiptPath, "result": child})...)
	live, err = parseReceiptStatus(data)
	if err != nil || live.Complete || live.Succeeded != 1 {
		t.Fatalf("group=%+v err=%v", live, err)
	}
	data = append(data, statusLine(t, "manifest_completed", map[string]any{"result": final})...)
	got, err := parseReceiptStatus(data)
	if err != nil || !got.Complete || got.Succeeded != 1 || got.Action != "manifest" {
		t.Fatalf("manifest=%+v err=%v", got, err)
	}
	envelope, _ := json.Marshal(map[string]any{"data": final})
	got, err = parseReceiptStatus(envelope)
	if err != nil || !got.Complete || got.Succeeded != 1 {
		t.Fatalf("envelope=%+v err=%v", got, err)
	}
}

func TestReceiptStatusCleanupPreflightFailureWithoutProviderOutcomes(t *testing.T) {
	data := statusLine(t, "label_cleanup_started", map[string]any{"action": "archive", "matched": 1})
	data = append(data, statusLine(t, "label_cleanup_result", map[string]any{"labelResults": nil})...)
	result := mail.BatchResult{Action: "archive", Matched: 1, NotAttempted: 1, StoppedReason: "provider preflight failed", Items: []mail.BatchItem{{ID: "1", Account: "Work", SourceMailbox: "INBOX", Status: "not_attempted"}}}
	data = append(data, statusLine(t, "stopped", map[string]any{"result": result})...)
	status, err := parseReceiptStatus(data)
	if err != nil || status.State != "stopped" || status.NotAttempted != 1 {
		t.Fatalf("status=%+v err=%v", status, err)
	}
}
