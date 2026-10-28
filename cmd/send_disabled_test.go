package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
)

func TestSendingRestrictionBlocksBeforeMail(t *testing.T) {
	for _, source := range []string{"flag", "config", "config-with-false-flag"} {
		for _, backend := range []string{"native", "legacy"} {
			for _, args := range [][]string{
				{"send"}, {"drafts", "send", "42"},
				{"send", "--reply-to", "42"}, {"send", "--reply-to", "42", "--reply-all"}, {"send", "--forward-of", "42"},
			} {
				t.Run(source+"/"+backend+"/"+strings.Join(args, " "), func(t *testing.T) {
					dir := t.TempDir()
					t.Setenv(config.EnvConfigPath, filepath.Join(dir, "config.json"))
					t.Setenv(config.EnvOutput, "")
					// Any legacy automation attempt leaves evidence; native must also be blocked
					// before backend selection, account resolution, or input validation.
					marker := filepath.Join(dir, "called")
					t.Setenv("MAIL_TEST_CALL_MARKER", marker)
					if err := os.WriteFile(filepath.Join(dir, "osascript"), []byte("#!/bin/sh\nprintf called > \"$MAIL_TEST_CALL_MARKER\"\nexit 99\n"), 0700); err != nil {
						t.Fatal(err)
					}
					t.Setenv("PATH", dir)
					invocation := append([]string{}, args...)
					if source == "flag" {
						invocation = append([]string{"--no-send"}, invocation...)
					} else {
						cfg := config.Config{}
						if err := cfg.Set(config.KeySendEnabled, "false"); err != nil {
							t.Fatal(err)
						}
						if err := config.Save(cfg); err != nil {
							t.Fatal(err)
						}
						if source == "config-with-false-flag" {
							invocation = append(invocation, "--no-send=false")
						}
					}
					invocation = append(invocation, "--backend", backend, "--json")
					var stdout, stderr bytes.Buffer
					code := Run(invocation, &stdout, &stderr)
					var envelope output.ErrorEnvelope
					if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
						t.Fatal(err, stderr.String())
					}
					if code != 1 || envelope.Code != "usage" || !strings.Contains(stderr.String(), "email sending is disabled") || stdout.Len() != 0 {
						t.Fatalf("exit %d stdout %s stderr %s", code, &stdout, &stderr)
					}
					if _, err := os.Stat(marker); !os.IsNotExist(err) {
						t.Fatalf("automation invoked: %v", err)
					}
				})
			}
		}
	}
}

func TestSendingRestrictionAllowsDraftsAndPreview(t *testing.T) {
	cases := []struct {
		args     []string
		response string
	}{
		{[]string{"drafts", "create", "--subject", "Review", "--body", "Text", "--to", "to@example.test"}, `{"ok":true,"result":{"account":"Work","accountId":"work-id","sender":"work@example.test","status":"saved","submitted":false,"draft":{"id":"42","account":"Work","mailbox":"Drafts"}}}`},
		{[]string{"send", "-t", "to@example.test", "-s", "Test", "--dry-run"}, `{"ok":true,"result":{"account":"Work","accountId":"work-id","sender":"work@example.test","status":"preview","submitted":false}}`},
		{[]string{"drafts", "list"}, `[]`},
		{[]string{"drafts", "update", "42", "--dry-run", "--subject", "Updated"}, ``},
		{[]string{"drafts", "delete", "42", "--dry-run"}, ``},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			writeOutgoingCLIResponse(t, tc.response)
			args := append(tc.args, "--no-send", "--backend", "legacy", "--json")
			if tc.args[1] != "list" {
				args = append(args, "-a", "Work")
			}
			code, out, errOut := run(t, args...)
			if code != 0 {
				t.Fatalf("exit %d stdout %s stderr %s", code, out, errOut)
			}
		})
	}
}

func TestSendingConfigCommandsAndFlagSource(t *testing.T) {
	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
	t.Setenv(config.EnvOutput, "")
	for _, tc := range []struct {
		args          []string
		value, source string
	}{
		{[]string{"config", "show"}, "true", "default"},
		{[]string{"config", "set", "send.enabled", "false"}, "", ""},
		{[]string{"config", "show", "--no-send=false"}, "false", "config"},
		{[]string{"config", "show", "--no-send"}, "false", "flag"},
		{[]string{"config", "unset", "send.enabled"}, "", ""},
		{[]string{"config", "show"}, "true", "default"},
	} {
		var stdout, stderr bytes.Buffer
		code := Run(append(tc.args, "--json"), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("%v: %s", tc.args, &stderr)
		}
		if tc.value != "" {
			var envelope struct {
				Data config.Resolved `json:"data"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if got := envelope.Data.SendEnabled; got.Value != tc.value || got.Source != tc.source {
				t.Fatalf("%v: %+v", tc.args, got)
			}
		}
	}
}

func TestExplicitSendingEnabledStillSubmits(t *testing.T) {
	for _, source := range []string{"default", "false-flag", "config-true"} {
		t.Run(source, func(t *testing.T) {
			writeOutgoingCLIResponse(t, `{"ok":true,"result":{"account":"Work","accountId":"work-id","sender":"work@example.test","status":"submitted","submitted":true}}`)
			t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
			t.Setenv(config.EnvOutput, "")
			args := []string{"send", "-a", "Work", "-t", "to@example.test", "-s", "Test", "--backend", "legacy", "--json"}
			if source == "false-flag" {
				args = append(args, "--no-send=false")
			}
			if source == "config-true" {
				cfg := config.Config{}
				if err := cfg.Set(config.KeySendEnabled, "true"); err != nil {
					t.Fatal(err)
				}
				if err := config.Save(cfg); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			if code := Run(args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"submitted": true`) {
				t.Fatalf("exit %d stdout %s stderr %s", code, &stdout, &stderr)
			}
		})
	}
}
