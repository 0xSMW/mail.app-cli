package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func TestManifestCommandsProtectMutationReceipt(t *testing.T) {
	a := &application{}
	root := a.newManifestCmd()
	apply, _, err := root.Find([]string{"apply"})
	if err != nil {
		t.Fatal(err)
	}
	if apply.Annotations[annotationMutation] != "true" {
		t.Fatal("apply must reject receipt-hiding output flags before mutation")
	}
	if apply.Flags().Lookup("dry-run") == nil {
		t.Fatal("missing preview flag")
	}
	verify, _, err := root.Find([]string{"verify"})
	if err != nil {
		t.Fatal(err)
	}
	if verify.Annotations[annotationMutation] == "true" {
		t.Fatal("verify is read-only")
	}
	for _, cmd := range root.Commands() {
		if err := cmd.Args(cmd, nil); err == nil {
			t.Fatalf("%s accepted missing file", cmd.Name())
		}
		if err := cmd.Args(cmd, []string{"one", "two"}); err == nil {
			t.Fatalf("%s accepted extra file", cmd.Name())
		}
	}
}

func TestManifestExplicitScopeRejectsConflictingItemsBeforeMailAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.json")
	data := `{"version":1,"items":[{"account":"Work","id":"205","sourceMailbox":"INBOX","identity":{"sender":"sender@example.com","subject":"subject","dateSent":"2026-08-29T00:00:00Z","messageSize":42},"action":"mark","read":true,"requiredPresent":["INBOX"],"requiredAbsent":[]}]}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []string{"account", "mailbox"} {
		for _, command := range []string{"apply", "verify"} {
			t.Run(command+"/"+scope, func(t *testing.T) {
				a := &application{}
				if scope == "account" {
					a.resolved.Account = config.Value{Value: "Personal", Source: config.SourceFlag}
				} else {
					a.resolved.Mailbox = config.Value{Value: "Other", Source: config.SourceFlag}
				}
				cmd, _, err := a.newManifestCmd().Find([]string{command})
				if err != nil {
					t.Fatal(err)
				}
				err = cmd.RunE(cmd, []string{path})
				if err == nil || clierr.Classify(err).Code != clierr.CodeUsage || !strings.Contains(err.Error(), "conflicts with explicit --"+scope) {
					t.Fatalf("%v", err)
				}
			})
		}
	}
	m, err := mail.ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	a := &application{}
	a.resolved.Account = config.Value{Value: "Personal", Source: config.SourceConfig}
	a.resolved.Mailbox = config.Value{Value: "Other", Source: config.SourceConfig}
	if err := a.validateManifestScope(m); err != nil {
		t.Fatalf("configured defaults restricted explicit manifest: %v", err)
	}
	a.resolved.Account = config.Value{Value: "Work", Source: config.SourceFlag}
	a.resolved.Mailbox = config.Value{Value: "INBOX", Source: config.SourceFlag}
	if err := a.validateManifestScope(m); err != nil {
		t.Fatalf("matching explicit scope rejected: %v", err)
	}
	other := m.Items[0]
	other.Account = "Personal"
	m.Items = append(m.Items, other)
	if err := a.validateManifestScope(m); err == nil || !strings.Contains(err.Error(), "item 2") {
		t.Fatalf("later cross-account selection accepted: %v", err)
	}
}

func TestManifestInvalidFileFailsBeforeMailAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"items":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	a := &application{} // Deliberately no Mail client or writer.
	root := a.newManifestCmd()
	for _, name := range []string{"apply", "verify"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		err = cmd.RunE(cmd, []string{path})
		if err == nil || clierr.Classify(err).Code != clierr.CodeUsage || !strings.Contains(err.Error(), "manifest requires at least one item") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
