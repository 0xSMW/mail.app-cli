package mail

import (
	"os"
	"path/filepath"
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
