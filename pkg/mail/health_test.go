package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDoctorProbesUseBoundedReadOnlyJXA(t *testing.T) {
	binDir := t.TempDir()
	mailAppBundlePath = t.TempDir()
	t.Cleanup(func() { mailAppBundlePath = "/System/Applications/Mail.app" })
	t.Setenv("PATH", binDir)
	script := `#!/bin/sh
case "$*" in
  *"account access probe"*) printf '%s' '{"accountCount":2}' ;;
  *) printf '%s' '{"ok":true}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}

	client := NewClient()
	if err := client.CheckMailBridge(); err != nil {
		t.Fatalf("CheckMailBridge returned error: %v", err)
	}
	if err := client.CheckAutomationAccess(time.Second); err != nil {
		t.Fatalf("CheckAutomationAccess returned error: %v", err)
	}
	count, err := client.CheckAccountAccess(time.Second)
	if err != nil {
		t.Fatalf("CheckAccountAccess returned error: %v", err)
	}
	if count != 2 {
		t.Fatalf("account count = %d, want 2", count)
	}
	if err := client.RunLiveProbe(time.Second); err != nil {
		t.Fatalf("RunLiveProbe returned error: %v", err)
	}
}

func TestRunningMailProcessIDsUsesIndependentWorkspaceProbe(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "probe.js")
	t.Setenv("PROCESS_PROBE_SCRIPT", logPath)
	writeFakeOsaScript(t, `
for arg in "$@"; do script="$arg"; done
printf '%s' "$script" > "$PROCESS_PROBE_SCRIPT"
printf '%s' '[42,84]'
`)
	pids, err := NewClient().RunningMailProcessIDs(time.Second)
	if err != nil || len(pids) != 2 || pids[0] != 42 || pids[1] != 84 {
		t.Fatalf("process observation = %v, %v", pids, err)
	}
	script, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(script), "NSRunningApplication.runningApplicationsWithBundleIdentifier('com.apple.mail')") ||
		strings.Contains(string(script), "Application(") || strings.Contains(string(script), ".launch") {
		t.Fatalf("process probe must not send an Apple Event or launch Mail: %s", script)
	}
}

func TestRunningMailProcessIDsKeepsStoppedAndUnknownDistinct(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		wantError    bool
	}{
		{name: "stopped", output: "[]"},
		{name: "null is unknown", output: "null", wantError: true},
		{name: "malformed", output: "not-json", wantError: true},
		{name: "wrong shape", output: "{}", wantError: true},
		{name: "invalid pid", output: "[0]", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PROCESS_PROBE_OUTPUT", tc.output)
			writeFakeOsaScript(t, `printf '%s' "$PROCESS_PROBE_OUTPUT"`)
			pids, err := NewClient().RunningMailProcessIDs(time.Second)
			if (err != nil) != tc.wantError {
				t.Fatalf("process observation = %v, %v", pids, err)
			}
			if !tc.wantError && len(pids) != 0 {
				t.Fatalf("stopped process observation = %v", pids)
			}
		})
	}
}

func TestCheckAccountAccessRejectsMalformedOutput(t *testing.T) {
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nprintf '%s' not-json\n"), 0755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}

	if _, err := NewClient().CheckAccountAccess(time.Second); err == nil {
		t.Fatal("CheckAccountAccess returned nil error for malformed JSON")
	}
}
