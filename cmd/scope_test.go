package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/config"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
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
