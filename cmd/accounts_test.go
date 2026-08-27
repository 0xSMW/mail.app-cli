package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v2/pkg/cache"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

func TestAccountsShowUsesLiveInventoryInsteadOfCachedList(t *testing.T) {
	homeDir := t.TempDir()
	binDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(`#!/bin/sh
printf '%s' '[{"id":"work","name":"Work","emailAddress":"work@example.com","enabled":true}]'
`), 0o755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}

	c, err := cache.New()
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	if err := c.Set("accounts", []mail.Account{{ID: "personal", Name: "Personal", Enabled: true}}); err != nil {
		t.Fatalf("seed accounts cache: %v", err)
	}

	code, stdout, stderr := run(t, "accounts", "list")
	if code != 0 || !strings.Contains(stdout, "Personal") || stderr != "" {
		t.Fatalf("accounts list = exit %d, stdout %q, stderr %q; want cached Personal account", code, stdout, stderr)
	}

	code, stdout, stderr = run(t, "accounts", "show", "Work")
	if code != 0 || !strings.Contains(stdout, "Work") || stderr != "" {
		t.Fatalf("accounts show Work = exit %d, stdout %q, stderr %q; want live Work account", code, stdout, stderr)
	}

	code, _, stderr = run(t, "accounts", "show", "Personal")
	if code == 0 || !strings.Contains(stderr, "account not found: Personal") {
		t.Fatalf("accounts show Personal = exit %d, stderr %q; want live not-found response", code, stderr)
	}
}
