package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v2/internal/output"
)

func installSmartSQLite(t *testing.T, response string) {
	t.Helper()
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	script := "#!/bin/sh\nprintf '%s\\n' \"$SMART_SQL_RESPONSE\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "sqlite3"), []byte(script), 0755); err != nil {
		t.Fatalf("write fake sqlite3: %v", err)
	}
	t.Setenv("SMART_SQL_RESPONSE", response)
}

func TestSmartListReportsUnavailableTodayCapability(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	code, stdout, stderr := run(t, "smart", "list", "--json")
	if code != 3 {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	var env output.ErrorEnvelope
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("unmarshal error envelope: %v; stderr = %s", err, stderr)
	}
	if env.OK || env.Code != "unavailable" || env.ExitCode != 3 {
		t.Fatalf("error envelope = %#v, want unavailable capability", env)
	}
	if env.Hint == "" {
		t.Fatal("unavailable Today error has no Full Disk Access hint")
	}
}

func TestSmartListJSONMarksTodayOnlyAndKeepsZeroCounts(t *testing.T) {
	installSmartSQLite(t, `[{"TotalCount":0,"UnreadCount":0}]`)
	code, stdout, stderr := run(t, "smart", "list", "--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	var env output.Envelope
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v; stdout = %s", err, stdout)
	}
	if !env.OK || env.Summary != "Today (built-in view; custom Smart Mailboxes unsupported)" {
		t.Fatalf("envelope = %#v", env)
	}
	if env.Meta["scope"] != "built_in_today" || env.Meta["customSmartMailboxes"] != "unsupported" {
		t.Fatalf("meta = %#v", env.Meta)
	}
	boxes, ok := env.Data.([]any)
	if !ok || len(boxes) != 1 {
		t.Fatalf("data = %#v, want one Today row", env.Data)
	}
	today, ok := boxes[0].(map[string]any)
	if !ok || today["name"] != "Today" || today["unreadCount"] != float64(0) || today["totalCount"] != float64(0) {
		t.Fatalf("Today JSON = %#v, want explicit zero counts", boxes[0])
	}
}

func TestSmartShowCustomMailboxIsUnsupportedWithoutDiskAccessHint(t *testing.T) {
	installSmartSQLite(t, `[{"TotalCount":0,"UnreadCount":0}]`)
	code, stdout, stderr := run(t, "smart", "show", "Later", "--json")
	if code != 3 {
		t.Fatalf("exit = %d, stdout = %s, stderr = %s", code, stdout, stderr)
	}
	var env output.ErrorEnvelope
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("unmarshal error envelope: %v; stderr = %s", err, stderr)
	}
	if env.Code != "unavailable" || env.Hint == "" || strings.Contains(env.Hint, "Full Disk Access") {
		t.Fatalf("unsupported custom Smart error = %#v", env)
	}
}
