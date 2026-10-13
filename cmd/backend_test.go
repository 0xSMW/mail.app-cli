package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestBackendInvalidPreflight(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_CONFIG", t.TempDir()+"/config.json")
	var stdout, stderr bytes.Buffer
	if status := Run([]string{"accounts", "list", "--backend", "invalid", "--json"}, &stdout, &stderr); status != 1 {
		t.Fatalf("exit %d output %s %s", status, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String()+stderr.String(), "backend must") {
		t.Fatalf("missing usage error: %s %s", stdout.String(), stderr.String())
	}
}
