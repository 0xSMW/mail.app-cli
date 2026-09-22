package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestScanMissingScopeIsPartialWithoutLegacyFallback(t *testing.T) {
	bin := t.TempDir()
	scripts := map[string]string{
		"sqlite3": "#!/bin/sh\nprintf '[]\\n'\n",
		"osascript": `#!/bin/sh
case "$*" in
 *"const accounts = mail.accounts();"*) printf '[{"id":"ABC","name":"Work","enabled":true}]\n' ;;
 *) printf 'unexpected legacy fallback' >&2; exit 1 ;;
esac
`,
	}
	for name, script := range scripts {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	code, stdout, stderr := run(t, "messages", "scan", "Missing", "-a", "Work", "--json")
	var envelope struct {
		OK   bool `json:"ok"`
		Data struct {
			Complete bool `json:"complete"`
			Coverage []struct {
				Error string `json:"error"`
			} `json:"coverage"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("%v: %s %s", err, stdout, stderr)
	}
	if code != 5 || envelope.OK || envelope.Data.Complete || len(envelope.Data.Coverage) != 1 || envelope.Data.Coverage[0].Error == "" {
		t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
}

func TestTriageRejectsInvalidFlagsBeforeMailAccess(t *testing.T) {
	for _, args := range [][]string{
		{"messages", "scan", "INBOX", "-m", "Trash", "-a", "Work"},
		{"messages", "scan", "INBOX", "--limit", "-1", "-a", "Work"},
		{"messages", "read", "1", "--budget", "0s"},
		{"messages", "read", "bad-id"},
	} {
		code, _, stderr := run(t, append(args, "--json")...)
		if code != 1 {
			t.Fatalf("args=%v exit=%d stderr=%s", args, code, stderr)
		}
	}
}
