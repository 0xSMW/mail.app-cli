package mail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRelocationReceiptFormatsAndRefusals(t *testing.T) {
	item := verificationItem()
	item.Status, item.MarkedRead = "succeeded", true
	receipt := BatchResult{Action: "archive", Items: []BatchItem{item}}
	raw, _ := json.Marshal(receipt)
	envelope, _ := json.Marshal(map[string]any{"ok": false, "data": receipt})
	started, _ := json.Marshal(map[string]any{"event": "started", "action": "archive", "markReadBefore": true})
	event, _ := json.Marshal(map[string]any{"event": "mutation_succeeded", "item": item})
	for name, data := range map[string][]byte{"raw": raw, "envelope": envelope, "interrupted-journal": append(append(started, '\n'), event...)} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "receipt")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := ReadRelocationReceipt(path)
			if err != nil || len(got.Items) != 1 || !got.MarkReadRequested {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
	for name, value := range map[string]BatchResult{"dry": {Action: "archive", DryRun: true, Items: []BatchItem{item}}, "wrong-action": {Action: "delete", Items: []BatchItem{item}}, "no-identity": {Action: "archive", Items: []BatchItem{{Status: "succeeded"}}}} {
		t.Run(name, func(t *testing.T) {
			data, _ := json.Marshal(value)
			path := filepath.Join(t.TempDir(), "receipt")
			os.WriteFile(path, data, 0600)
			if _, err := ReadRelocationReceipt(path); err == nil {
				t.Fatal("accepted unsafe receipt")
			}
		})
	}
}

func TestReadRelocationReceiptCrashAfterMutationStartedIsUnknown(t *testing.T) {
	item := verificationItem()
	item.Status = ""
	started, _ := json.Marshal(map[string]any{"event": "started", "action": "archive", "matched": 1})
	applying, _ := json.Marshal(map[string]any{"event": "mutation_started", "item": item})
	path := filepath.Join(t.TempDir(), "interrupted.jsonl")
	if err := os.WriteFile(path, append(append(started, '\n'), applying...), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRelocationReceipt(path)
	if err != nil || len(got.Items) != 1 || got.Items[0].Status != "unknown" || got.Attempted != 1 || got.Failed != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestReadRelocationReceiptRefusesMixedPendingRead(t *testing.T) {
	item := verificationItem()
	item.Status = "succeeded"
	pending := verificationItem()
	pending.ID = "second"
	pending.Status = "skipped"
	pending.MarkReadOnSkip = true
	events := []map[string]any{{"event": "started", "action": "archive"}, {"event": "mutation_succeeded", "item": item}, {"event": "mark_read_started", "item": pending}}
	var data []byte
	for _, event := range events {
		line, _ := json.Marshal(event)
		data = append(append(data, line...), '\n')
	}
	path := filepath.Join(t.TempDir(), "mixed.jsonl")
	os.WriteFile(path, data, 0600)
	if _, err := ReadRelocationReceipt(path); err == nil {
		t.Fatal("pending skipped read silently passed reconciliation")
	}
}

func TestReconcileRejectsNoEligibleReceiptBeforeReads(t *testing.T) {
	for _, items := range [][]BatchItem{nil, {{Status: "failed"}}, {{Status: "skipped"}}} {
		if _, err := ReconcileMutations(NewClient(), BatchOptions{}, BatchResult{Action: "archive", Items: items}); err == nil {
			t.Fatal("accepted receipt with no eligible observations")
		}
	}
}
