package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

func TestDurableArchiveStopsOnUncertainImportantRemovalAndReportsLabelReceipt(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$*" in
 *mail-app-cli-native-archive-account*) printf true ;;
 *) printf 'unexpected Mail mutation' >&2; exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	a, stdout, _ := labelBatchApp(t, "")
	a.mailClient = mail.NewClient()
	a.batchRemoveImportant = true
	changes := 0
	a.gmailLabelApply = func(_ *cobra.Command, account string, show bool) (gmailLabelApply, error) {
		if show || account != "Gmail" {
			t.Fatalf("unexpected workflow account=%s show=%t", account, show)
		}
		return func(_ context.Context, r mail.GmailLabelRequest) (mail.GmailLabelResult, error) {
			if r.DryRun {
				return mail.GmailLabelResult{Status: "dry_run"}, nil
			}
			changes++
			return mail.GmailLabelResult{Account: "Gmail", Status: "unknown", MailStatus: "pending", ReceiptPath: "/receipts/provider-cleanup.jsonl"}, nil
		}, nil
	}
	mutated := false
	opts := batchOptions{Action: "archive"}
	report := filepath.Join(t.TempDir(), "archive.json")
	result, mutationErr := a.runDurableMessageBatch(a.mailClient, opts, importantArchiveItems(), func(*mail.Client, *batchItem) error {
		mutated = true
		return nil
	}, report)
	if mutationErr == nil || mutated || changes != 1 || result.NotAttempted != 2 || len(result.LabelResults) != 2 || result.LabelResults[0].ReceiptPath != "/receipts/provider-cleanup.jsonl" {
		t.Fatalf("mutated=%t changes=%d result=%+v error=%v", mutated, changes, result, mutationErr)
	}
	if err := a.writeReceipt(result, opts, nil, mutationErr, report); err == nil {
		t.Fatal("uncertain label cleanup reported success")
	}
	var envelope struct {
		OK   bool             `json:"ok"`
		Data mail.BatchResult `json:"data"`
		Hint string           `json:"hint"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Data.LabelResults[0].ReceiptPath != "/receipts/provider-cleanup.jsonl" || !strings.Contains(envelope.Hint, "labels reconcile") {
		t.Fatalf("envelope=%+v", envelope)
	}
	journal, err := os.ReadFile(result.ReceiptPath)
	if err != nil || !strings.Contains(string(journal), "provider-cleanup.jsonl") || !strings.Contains(string(journal), "label_cleanup_result") {
		t.Fatalf("journal=%s error=%v", journal, err)
	}
}
