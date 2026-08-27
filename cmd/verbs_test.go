package cmd

import (
	"testing"

	"github.com/0xSMW/mail.app-cli/internal/config"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
)

func TestUnifiedListingScopedUsesResolvedMailbox(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		env  map[string]string
		want bool
	}{
		{name: "no configured scope", want: false},
		{name: "mailbox from environment", env: map[string]string{config.EnvMailbox: "Archive"}, want: true},
		{name: "mailbox from config", cfg: config.Config{Mailbox: "Archive"}, want: true},
		{name: "account from config", cfg: config.Config{Account: "Work"}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolved := config.Resolve(nil, tt.cfg, func(key string) string {
				return tt.env[key]
			})
			if got := unifiedListingScoped(resolved); got != tt.want {
				t.Fatalf("unifiedListingScoped(%+v) = %v, want %v", resolved, got, tt.want)
			}
		})
	}
}

func TestSpecialMailboxForAccountUsesActualMailboxName(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		account   string
		mailboxes []mail.Mailbox
		want      string
	}{
		{name: "sent items", kind: "sent", account: "Work", mailboxes: []mail.Mailbox{{Account: "Work", Name: "Sent Items"}}, want: "Sent Items"},
		{name: "drafts", kind: "drafts", account: "Work", mailboxes: []mail.Mailbox{{Account: "Work", Name: "Draft"}}, want: "Draft"},
		{name: "trash", kind: "trash", account: "Work", mailboxes: []mail.Mailbox{{Account: "Work", Name: "Deleted Messages"}}, want: "Deleted Messages"},
		{name: "junk", kind: "junk", account: "Work", mailboxes: []mail.Mailbox{{Account: "Work", Name: "Spam"}}, want: "Spam"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := specialMailboxForAccount(tt.kind, tt.account, tt.mailboxes)
			if !ok || got != tt.want {
				t.Fatalf("specialMailboxForAccount(%q, %q, %v) = (%q, %v), want (%q, true)", tt.kind, tt.account, tt.mailboxes, got, ok, tt.want)
			}
		})
	}
	if mailbox, ok := specialMailboxForAccount("inbox", "Work", nil); ok || mailbox != "" {
		t.Fatalf("specialMailboxForAccount(\"inbox\", \"Work\", nil) = (%q, %v), want (\"\", false)", mailbox, ok)
	}
}
