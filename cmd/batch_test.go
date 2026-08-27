package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

func TestRunMessageBatchVerifyReadErrorFailsMutations(t *testing.T) {
	installBatchVerificationScript(t, "error")

	for _, tt := range []struct {
		name   string
		action string
		target string
	}{
		{name: "archive", action: "archive", target: "All Mail"},
		{name: "move", action: "move", target: "Processed"},
		{name: "delete", action: "delete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runMessageBatch(mail.NewClient(), batchOptions{
				Action:        tt.action,
				TargetMailbox: tt.target,
				Verify:        true,
			}, []batchItem{{ID: "123", Account: "Work", SourceMailbox: "INBOX"}}, func(*mail.Client, *batchItem) error {
				return nil
			})
			if err == nil {
				t.Fatal("runMessageBatch() error = nil, want verification read failure")
			}
			if result.Succeeded != 0 || result.Failed != 1 {
				t.Fatalf("runMessageBatch() counts = succeeded %d, failed %d; want succeeded 0, failed 1", result.Succeeded, result.Failed)
			}
			item := result.Items[0]
			if item.Status != "failed" || item.VerifyStatus != "verification-failed" {
				t.Fatalf("runMessageBatch() item = %+v, want failed verification", item)
			}
			if !strings.Contains(item.VerifyError, "verification read failed") {
				t.Fatalf("VerifyError = %q, want read error", item.VerifyError)
			}
		})
	}
}

func TestVerifyBatchMutationAcceptsActualMessageAbsence(t *testing.T) {
	installBatchVerificationScript(t, "absent")

	for _, tt := range []struct {
		name   string
		action string
		target string
	}{
		{name: "archive", action: "archive", target: "All Mail"},
		{name: "move", action: "move", target: "Processed"},
		{name: "delete", action: "delete"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, err := mail.VerifyMutation(mail.NewClient(), batchOptions{Action: tt.action}, batchItem{
				ID: "123", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: tt.target,
			})
			if err != nil {
				t.Fatalf("verifyBatchMutation() error = %v, want nil", err)
			}
			if status != "absent-from-source" {
				t.Fatalf("verifyBatchMutation() status = %q, want absent-from-source", status)
			}
		})
	}
}

func TestWriteReceiptPreservesSuccessfulMutationReceiptWhenReportFileFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w, err := output.New(output.FormatJSON, &stdout, &stderr, false, "", "messages batch archive", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	previousWriter := writer
	writer = w
	t.Cleanup(func() { writer = previousWriter })

	result, err := runMessageBatch(mail.NewClient(), batchOptions{Action: "archive"}, []batchItem{{
		ID: "42", Account: "Work", SourceMailbox: "INBOX",
	}}, func(*mail.Client, *batchItem) error { return nil })
	if err != nil {
		t.Fatalf("runMessageBatch() error = %v", err)
	}
	reportFile := filepath.Join(t.TempDir(), "missing", "receipt.json")
	err = writeReceipt(result, batchOptions{Action: "archive"}, nil, nil, reportFile)
	var failure *clierr.Error
	if !errors.As(err, &failure) || failure.Code != clierr.CodeInternal || !failure.Reported {
		t.Fatalf("writeReceipt() error = %#v, want reported report-file failure", err)
	}
	if got := clierr.ExitCode(failure.Code); got != 7 {
		t.Fatalf("report-file failure exit code = %d, want 7", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no second error envelope", stderr.String())
	}

	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a receipt envelope: %v\n%s", err, stdout.String())
	}
	if !envelope.OK || envelope.Code != "" || envelope.ExitCode != 0 {
		t.Fatalf("envelope outcome = %+v, want successful mutation receipt", envelope)
	}
	if !strings.Contains(strings.Join(envelope.Notices, "\n"), "write batch report") || !strings.Contains(strings.Join(envelope.Notices, "\n"), reportFile) {
		t.Fatalf("notices = %q, want report-file failure for %q", envelope.Notices, reportFile)
	}

	data, ok := envelope.Data.(map[string]any)
	if !ok || data["succeeded"] != float64(1) {
		t.Fatalf("receipt data = %#v, want successful mutation", envelope.Data)
	}
}

func TestWriteReceiptKeepsMutationFailureAuthoritativeWhenReportFileFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w, err := output.New(output.FormatJSON, &stdout, &stderr, false, "", "messages batch archive", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	previousWriter := writer
	writer = w
	t.Cleanup(func() { writer = previousWriter })

	mutationErr := &mail.BatchFailedError{Action: "archive", Failed: 1, Attempted: 1}
	err = writeReceipt(batchResult{Action: "archive", Matched: 1, Attempted: 1, Failed: 1, Items: []batchItem{{ID: "42", Status: "failed"}}}, batchOptions{Action: "archive"}, nil, mutationErr, filepath.Join(t.TempDir(), "missing", "receipt.json"))
	var failure *clierr.Error
	if !errors.As(err, &failure) || failure.Code != clierr.CodeMutationFailed || !failure.Reported {
		t.Fatalf("writeReceipt() error = %#v, want reported mutation failure", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no second error envelope", stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a receipt envelope: %v\n%s", err, stdout.String())
	}
	if envelope.OK || envelope.Code != string(clierr.CodeMutationFailed) || !strings.Contains(strings.Join(envelope.Notices, "\n"), "write batch report") {
		t.Fatalf("envelope = %+v, want mutation failure with report notice", envelope)
	}
}

func installBatchVerificationScript(t *testing.T, mode string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \"$MAIL_APP_CLI_BATCH_VERIFY_MODE\" in\nerror)\n  case \"$*\" in\n    *\"mailbox not found:\"*) echo 'verification read failed' >&2; exit 1 ;;\n    *) printf null ;;\n  esac ;;\nabsent) printf null ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_BATCH_VERIFY_MODE", mode)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
}
