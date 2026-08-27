package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
)

func TestAttachmentExportResultWritesFailedJSONEnvelopeWithReceipt(t *testing.T) {
	saved := []savedAttachment{{
		MessageID: "42",
		Name:      "invoice.pdf",
		Path:      "/tmp/invoice.pdf",
		Status:    "failed",
		Error:     "save failed",
	}}
	rows := [][]string{{"42", "invoice.pdf", "failed", "save failed"}}
	var stdout, stderr bytes.Buffer
	w, err := output.New(output.FormatJSON, &stdout, &stderr, false, "", "export attachments", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}

	err = w.Write(attachmentExportResult(saved, "/tmp", 1, rows))
	var failure *clierr.Error
	if !errors.As(err, &failure) {
		t.Fatalf("Write error = %v, want classified failure", err)
	}
	if failure.Code != clierr.CodeMutationFailed || !failure.Reported {
		t.Fatalf("failure = %+v", failure)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no second error envelope", stderr.String())
	}

	var envelope struct {
		OK       bool              `json:"ok"`
		Code     string            `json:"code"`
		Error    string            `json:"error"`
		Data     []savedAttachment `json:"data"`
		ExitCode int               `json:"exitCode"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\n%s", err, stdout.String())
	}
	if envelope.OK || envelope.Code != string(clierr.CodeMutationFailed) || envelope.ExitCode != 6 {
		t.Fatalf("envelope failure fields = %+v", envelope)
	}
	if !strings.Contains(envelope.Error, "failed to export 1 attachment") {
		t.Fatalf("envelope error = %q", envelope.Error)
	}
	if len(envelope.Data) != 1 || envelope.Data[0] != saved[0] {
		t.Fatalf("receipt data = %+v, want %+v", envelope.Data, saved)
	}
}

func TestAttachmentExportResultPreservesPlainReceiptAndFailure(t *testing.T) {
	saved := []savedAttachment{{MessageID: "42", Name: "invoice.pdf", Path: "/tmp/invoice.pdf", Status: "failed", Error: "save failed"}}
	rows := [][]string{{"42", "invoice.pdf", "failed", "save failed"}}
	var stdout, stderr bytes.Buffer
	w, err := output.New(output.FormatPlain, &stdout, &stderr, false, "", "export attachments", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}

	err = w.Write(attachmentExportResult(saved, "/tmp", 1, rows))
	var failure *clierr.Error
	if !errors.As(err, &failure) || !failure.Reported {
		t.Fatalf("Write error = %#v, want reported classified failure", err)
	}
	if !strings.Contains(stdout.String(), "invoice.pdf") || !strings.Contains(stdout.String(), "save failed") {
		t.Fatalf("plain receipt = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "failed to export 1 attachment") {
		t.Fatalf("plain error = %q", stderr.String())
	}
}

func TestExportAttachmentsPartialFailureWritesOneFailedEnvelope(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	script := `#!/bin/sh
case "$*" in
  *"const accounts = mail.accounts()"*) printf '%s' '[{"id":"work","name":"Work","enabled":true}]' ;;
  *"dateReceived"*) printf '%s' '[{"id":"42"}]' ;;
  *"const result = []"*) printf '%s' '[{"index":0,"name":"invoice.pdf"}]' ;;
  *"requestedIndex"*) printf '%s' 'Error: unable to save attachment' ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}

	outputDir := filepath.Join(t.TempDir(), "attachments")
	code, stdout, stderr := run(t, "export", "attachments", "--json", "--account", "Work", "--output", outputDir)
	if code != 6 {
		t.Fatalf("exit = %d, want 6; stderr = %s", code, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want no second error envelope", stderr)
	}

	var envelope struct {
		OK   bool              `json:"ok"`
		Code string            `json:"code"`
		Data []savedAttachment `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v\n%s", err, stdout)
	}
	if envelope.OK || envelope.Code != string(clierr.CodeMutationFailed) {
		t.Fatalf("envelope = %+v", envelope)
	}
	if len(envelope.Data) != 1 || envelope.Data[0].Status != "failed" || envelope.Data[0].Error == "" {
		t.Fatalf("receipt data = %+v", envelope.Data)
	}
}
