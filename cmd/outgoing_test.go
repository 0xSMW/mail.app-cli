package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/output"
)

// These tests check the CLI/bridge response contract. The mail package's Node
// model tests separately execute the complete production compose script.
func writeOutgoingCLIResponse(t *testing.T, response string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("MAIL_TEST_OUTGOING_RESPONSE", response)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(dir, "automation.lock"))
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(dir, "osascript"), []byte("#!/bin/sh\nprintf '%s' \"$MAIL_TEST_OUTGOING_RESPONSE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestSendRejectsExitZeroErrorAndUnknownResponses(t *testing.T) {
	for _, response := range []string{"Error: sender mismatch", "false", "", `{"ok":true,"result":{"status":"submitted"}}`} {
		t.Run(response, func(t *testing.T) {
			writeOutgoingCLIResponse(t, response)
			code, stdout, stderr := run(t, "send", "-a", "Exchange", "-t", "to@example.test", "-s", "Test", "--json")
			if code != 6 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			if strings.Contains(stdout, `"sent": true`) || strings.Contains(stdout, `"ok": true`) {
				t.Fatalf("false success receipt: %s", stdout)
			}
		})
	}
}

func TestSendPreservesSubmittedAndUncertainReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		wantExit       int
		wantSent       bool
		wantUncertain  bool
	}{
		{"accepted", `{"ok":true,"result":{"account":"Exchange","accountId":"exchange-id","sender":"alias@example.test","status":"submitted","submitted":true}}`, 0, true, false},
		{"rejected", `{"ok":false,"phase":"sender_verification","error":"sender mismatch","result":{"account":"Exchange","accountId":"exchange-id","sender":"alias@example.test","status":"failed","submitted":false}}`, 6, false, false},
		{"uncertain", `{"ok":false,"phase":"send","error":"connection lost","attempted":true,"uncertain":true,"result":{"account":"Exchange","accountId":"exchange-id","sender":"alias@example.test","status":"uncertain","submitted":false}}`, 6, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeOutgoingCLIResponse(t, tc.response)
			code, stdout, stderr := run(t, "send", "-a", "Exchange", "--from", "alias@example.test", "-t", "to@example.test", "-s", "Test", "--json")
			var envelope struct {
				OK   bool        `json:"ok"`
				Data sendReceipt `json:"data"`
			}
			if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
				t.Fatalf("stdout=%s stderr=%s: %v", stdout, stderr, err)
			}
			if code != tc.wantExit || envelope.OK != (tc.wantExit == 0) || envelope.Data.Sent != tc.wantSent || envelope.Data.Uncertain != tc.wantUncertain {
				t.Fatalf("exit=%d envelope=%+v stderr=%s", code, envelope, stderr)
			}
			if envelope.Data.AccountID != "exchange-id" || envelope.Data.Sender != "alias@example.test" {
				t.Fatalf("identity missing: %+v", envelope.Data)
			}
		})
	}
}

func TestSendPreviewExposesConfiguredIdentityOnly(t *testing.T) {
	writeOutgoingCLIResponse(t, `{"ok":true,"result":{"account":"Exchange","accountId":"exchange-id","sender":"alias@example.test","status":"preview","submitted":false}}`)
	code, stdout, stderr := run(t, "send", "-a", "Exchange", "--from", "alias@example.test", "-t", "to@example.test", "-s", "Test", "--dry-run", "--json")
	var envelope output.Envelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil || code != 0 || !envelope.OK {
		t.Fatalf("exit=%d stdout=%s stderr=%s error=%v", code, stdout, stderr, err)
	}
	if !strings.Contains(stdout, `"sender": "alias@example.test"`) || len(envelope.Notices) != 1 || !strings.Contains(envelope.Notices[0], "delivery route have not been tested") {
		t.Fatalf("misleading preview: %s", stdout)
	}
}

func TestSavedDraftPreviewReadsRecipientsDirectlyAndRejectsRichContent(t *testing.T) {
	for _, contentType := range []string{"text/plain", "text/html"} {
		t.Run(contentType, func(t *testing.T) {
			writeOutgoingCLIResponse(t, `{"ok":true,"result":{"account":"Exchange","accountId":"exchange-id","sender":"alias@example.test","status":"preview","submitted":false}}`)
			snapshot := map[string]any{
				"message": map[string]any{"id": "42", "account": "Exchange", "mailbox": "Drafts", "sender": "Alias <alias@example.test>",
					"subject": "Test", "content": "Body", "toRecipients": []string{"to@example.test"}, "ccRecipients": []string{}, "bccRecipients": []string{"hidden@example.test"}},
				"source": "From: Alias <alias@example.test>\r\nTo: to@example.test\r\nSubject: Test\r\nContent-Type: " + contentType + "; charset=utf-8\r\n\r\nBody",
			}
			encoded, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("MAIL_TEST_DRAFT_SNAPSHOT", string(encoded))
			bin := strings.Split(os.Getenv("PATH"), ":")[0]
			if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(`#!/bin/sh
case "$*" in
 *"const draftSnapshotRead = true;"*) printf '%s' "$MAIL_TEST_DRAFT_SNAPSHOT" ;;
 *'JSON.stringify(composeOutgoing('*) printf '%s' "$MAIL_TEST_OUTGOING_RESPONSE" ;;
 *) printf 'unexpected operation' >&2; exit 1 ;;
esac
`), 0o755); err != nil {
				t.Fatal(err)
			}
			code, stdout, stderr := run(t, "drafts", "send", "42", "-a", "Exchange", "--dry-run", "--json")
			if contentType == "text/plain" {
				if code != 0 || !strings.Contains(stdout, `"sender": "alias@example.test"`) || !strings.Contains(stdout, `"dryRun": true`) {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
				}
			} else if code == 0 {
				t.Fatalf("unsupported rich draft preview accepted: %s", stdout)
			}
		})
	}
}
