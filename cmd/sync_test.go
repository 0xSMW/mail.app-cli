package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
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
	if receipt["status"] != "failed" || receipt["actualScope"] != "enabled-accounts" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if errorText, _ := receipt["error"].(string); !strings.Contains(errorText, "sync accounts:") {
		t.Fatalf("receipt error = %q", errorText)
	}
}

func TestSyncSuccessReportsRequestAcceptanceAndSelectedScope(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args  []string
		scope string
	}{
		{[]string{"sync", "--json"}, "enabled-accounts"},
		{[]string{"sync", "-a", "Work", "--json"}, "account"},
	} {
		code, stdout, stderr := run(t, tc.args...)
		if code != 0 || stderr != "" {
			t.Fatalf("exit=%d out=%s err=%s", code, stdout, stderr)
		}
		var env output.Envelope
		if err := json.Unmarshal([]byte(stdout), &env); err != nil {
			t.Fatal(err)
		}
		receipt, ok := env.Data.(map[string]any)
		if !ok || !env.OK || receipt["status"] != "completed" || receipt["actualScope"] != tc.scope || receipt["completionBasis"] != "request-accepted" {
			t.Fatalf("receipt=%+v", env)
		}
		if strings.Contains(env.Summary, "Synced") {
			t.Fatalf("summary implies server completion: %s", env.Summary)
		}
	}
}

func TestWaitForSyncStabilityPreservesObservationErrorClassification(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code clierr.Code
	}{
		{
			name: "not found",
			err:  clierr.New(clierr.CodeNotFound, "mailbox not found: Archive"),
			code: clierr.CodeNotFound,
		},
		{
			name: "unavailable",
			err:  clierr.New(clierr.CodeUnavailable, "Mail.app access is unavailable"),
			code: clierr.CodeUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := waitForSyncStabilityWithObserver(time.Second, time.Millisecond, func() (int, error) {
				return 0, tt.err
			})
			if !errors.Is(err, tt.err) {
				t.Fatalf("error = %v, want original observation error %v", err, tt.err)
			}
			if got := clierr.Classify(err).Code; got != tt.code {
				t.Fatalf("error code = %q, want %q", got, tt.code)
			}
		})
	}
}

func TestWaitForSyncStabilityReturnsTimeoutOnlyAfterDeadline(t *testing.T) {
	const timeout = 10 * time.Millisecond
	started := time.Now()
	err := waitForSyncStabilityWithObserver(timeout, time.Millisecond, func() (int, error) {
		return int(time.Since(started).Nanoseconds()), nil
	})
	if got := clierr.Classify(err).Code; got != clierr.CodeTimeout {
		t.Fatalf("error code = %q, want %q (error: %v)", got, clierr.CodeTimeout, err)
	}
	if elapsed := time.Since(started); elapsed < timeout {
		t.Fatalf("returned after %s, before timeout %s", elapsed, timeout)
	}
}
