package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/internal/output"
)

func TestSyncFailureWritesSingleFailedEnvelopeWithReceipt(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\necho 'Not authorized to send Apple events to Mail.' >&2\nexit 1\n"), 0755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}

	code, stdout, stderr := run(t, "sync", "--json")
	if code != 3 {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty because the failed result was already reported", stderr)
	}
	var env output.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout)
	}
	if env.OK || env.Code != "unavailable" || env.ExitCode != 3 {
		t.Fatalf("envelope = %+v", env)
	}
	receipt, ok := env.Data.(map[string]any)
	if !ok {
		t.Fatalf("receipt = %#v, want object", env.Data)
	}
	if receipt["status"] != "failed" || receipt["actualScope"] != "all-accounts" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if errorText, _ := receipt["error"].(string); !strings.Contains(errorText, "sync accounts:") {
		t.Fatalf("receipt error = %q", errorText)
	}
}
