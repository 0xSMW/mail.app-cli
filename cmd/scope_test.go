package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/config"
	"github.com/0xSMW/mail.app-cli/v2/pkg/cache"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

func TestResolveLocatedMessagesIndexMissFailsClosed(t *testing.T) {
	previous := resolved
	t.Cleanup(func() { resolved = previous })
	resolved = config.Resolved{
		Account: config.Value{Value: "Configured", Source: config.SourceConfig},
		Mailbox: config.Value{Value: "Archive", Source: config.SourceConfig},
	}

	refs, notices, err := resolveLocatedMessages([]string{"42"}, map[string]mail.MessageLocation{}, nil)
	if refs != nil || notices != nil {
		t.Fatalf("index miss returned refs=%+v notices=%v, want neither", refs, notices)
	}
	cerr := clierr.Classify(err)
	if cerr.Code != clierr.CodeNotFound {
		t.Fatalf("error code = %q, want %q (error: %v)", cerr.Code, clierr.CodeNotFound, err)
	}
	if !strings.Contains(cerr.Hint, "--account") || !strings.Contains(cerr.Hint, "--mailbox") {
		t.Fatalf("hint = %q, want explicit account and mailbox guidance", cerr.Hint)
	}
}

func TestResolveLocatedMessagesFallsBackOnlyWhenIndexUnavailable(t *testing.T) {
	previous := resolved
	t.Cleanup(func() { resolved = previous })
	resolved = config.Resolved{
		Account: config.Value{Value: "Configured", Source: config.SourceConfig},
		Mailbox: config.Value{Value: "Archive", Source: config.SourceConfig},
	}

	refs, notices, err := resolveLocatedMessages([]string{"42"}, nil, errors.New("index unavailable"))
	if err != nil {
		t.Fatalf("fallback error = %v", err)
	}
	if len(refs) != 1 || refs[0].Account != "Configured" || refs[0].Mailbox != "Archive" {
		t.Fatalf("fallback refs = %+v, want configured scope", refs)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "Envelope Index unavailable") {
		t.Fatalf("fallback notices = %v, want unavailable notice", notices)
	}
}

func TestRequireAccountUsesLiveInventoryInsteadOfCachedAccounts(t *testing.T) {
	previousResolved, previousClient := resolved, mailClient
	t.Cleanup(func() {
		resolved = previousResolved
		mailClient = previousClient
	})

	homeDir := t.TempDir()
	binDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(`#!/bin/sh
printf '%s' '[{"id":"personal","name":"Personal","enabled":true},{"id":"work","name":"Work","enabled":true}]'
`), 0o755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}

	c, err := cache.New()
	if err != nil {
		t.Fatalf("create cache: %v", err)
	}
	stale := []mail.Account{{ID: "personal", Name: "Personal", Enabled: true}}
	if err := c.Set("accounts", stale); err != nil {
		t.Fatalf("seed accounts cache: %v", err)
	}

	mailClient = mail.NewClient()
	if _, err := accountsCached(false, false); err != nil {
		t.Fatalf("read stale accounts cache: %v", err)
	}
	resolved = config.Resolved{}

	account, err := requireAccount()
	if account != "" {
		t.Fatalf("account = %q, want no implicit selection", account)
	}
	if err == nil || !strings.Contains(err.Error(), `"Personal", "Work"`) {
		t.Fatalf("requireAccount error = %v, want live account choice", err)
	}
}
