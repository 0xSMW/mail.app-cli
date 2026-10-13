package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func captureSyncScript(t *testing.T, sync func(*Client) error) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script")
	t.Setenv("SYNC_SCRIPT", path)
	writeFakeOsaScript(t, `printf '%s' "$2" >> "$SYNC_SCRIPT"`)
	if err := sync(NewClient()); err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(script)
}

func TestSyncAccountScopesSynchronizationAndFetching(t *testing.T) {
	name := `Work "quoted" \\ account`
	script := captureSyncScript(t, func(c *Client) error { return c.SyncAccount(name) })
	for _, want := range []string{
		`if name of acc is "` + escapeAppleScriptString(name) + `" then`,
		"set targetAccount to contents of acc",
		"if targetAccount is missing value then",
		`error "Account not found: ` + escapeAppleScriptString(name) + `"`,
		"if class of targetAccount is imap account or class of targetAccount is iCloud account then\n\t\t\tsynchronize with targetAccount\n\t\tend if\n\t\tcheck for new mail for targetAccount",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bridge script missing %q: %s", want, script)
		}
	}
	if strings.Count(script, "synchronize with") != 1 || strings.Count(script, "check for new mail") != 1 {
		t.Fatalf("unexpected additional sync/fetch requests: %s", script)
	}
	if strings.Index(script, "Account not found:") > strings.Index(script, "synchronize with") {
		t.Fatalf("synchronization precedes account validation: %s", script)
	}
}

func TestSyncAllAccountsOnlyRequestsEnabledAccounts(t *testing.T) {
	script := captureSyncScript(t, (*Client).SyncAllAccounts)
	for _, want := range []string{
		"repeat with acc in accounts",
		"set targetAccount to contents of acc\n\t\t\tif enabled of targetAccount then",
		"class of targetAccount is imap account or class of targetAccount is iCloud account",
		"synchronize with targetAccount",
		"synchronize with targetAccount\n\t\tend if\n\t\tcheck for new mail for targetAccount\n\t\t\tend if",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bridge script missing %q: %s", want, script)
		}
	}
	if strings.Contains(script, "on error") {
		t.Fatalf("script suppresses failed sync requests: %s", script)
	}
	if strings.Count(script, "synchronize with") != 1 || strings.Count(script, "check for new mail") != 1 {
		t.Fatalf("unexpected additional bridge requests: %s", script)
	}
}

func TestSyncPropagatesBridgeFailures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sync    func(*Client) error
		message string
	}{
		{"unknown account", func(c *Client) error { return c.SyncAccount("Unknown") }, "Account not found: Unknown"},
		{"account synchronization failed", func(c *Client) error { return c.SyncAccount("Work") }, "synchronize failed"},
		{"account fetching failed", func(c *Client) error { return c.SyncAccount("Work") }, "check for new mail failed"},
		{"all synchronization failed", (*Client).SyncAllAccounts, "synchronize failed"},
		{"all fetching failed", (*Client).SyncAllAccounts, "check for new mail failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SYNC_FAILURE", tc.message)
			writeFakeOsaScript(t, `printf '%s' "$SYNC_FAILURE" >&2; exit 1`)
			if err := tc.sync(NewClient()); err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("sync error = %v, want %q", err, tc.message)
			}
		})
	}
}

func TestSyncPropagatesContextDeadline(t *testing.T) {
	for _, tc := range []struct {
		name string
		sync func(*Client) error
	}{
		{"account", func(c *Client) error { return c.SyncAccount("Work") }},
		{"all", (*Client).SyncAllAccounts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFakeOsaScript(t, "/bin/sleep 2")
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			defer cancel()
			if err := tc.sync(NewClient().WithContext(ctx)); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("sync error = %v, want context deadline exceeded", err)
			}
		})
	}
}
