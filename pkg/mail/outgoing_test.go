package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSendMessageRejectsScriptFailureStdout(t *testing.T) {
	for _, output := range []string{"Error: Mail rejected sender", "false"} {
		t.Run(output, func(t *testing.T) {
			writeFakeOsaScript(t, "printf '%s\\n' '"+output+"'\n")
			if err := NewClient().SendMessage("Account", "Subject", "Body", []string{"to@example.com"}, nil, nil, nil); err == nil {
				t.Fatalf("SendMessage accepted exit-zero failure stdout %q", output)
			}
		})
	}
}

func TestOutgoingProductionScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required to execute outgoing production JavaScript tests")
	}
	cases := []struct {
		name                  string
		action                string
		config                map[string]interface{}
		sender                string
		attachments           []string
		ok                    bool
		sent, closed, deleted int
		uncertain             bool
		ccOnly                bool
	}{
		{name: "default sender", ok: true, sent: 1},
		{name: "recipient insertion mismatch", config: map[string]interface{}{"dropRecipients": true}, deleted: 1},
		{name: "null content attachments without files send", config: map[string]interface{}{"nullContentAttachments": true}, ok: true, sent: 1},
		{name: "null content attachments without files save", action: "save", config: map[string]interface{}{"nullContentAttachments": true}, ok: true, closed: 1},
		{name: "localized nested Drafts", action: "save", config: map[string]interface{}{"draftName": "Bozze", "nestedDrafts": true}, ok: true, closed: 1},
		{name: "ambiguous special Drafts", action: "save", config: map[string]interface{}{"ambiguousDrafts": true}},
		{name: "later transport switch is outside identity contract", config: map[string]interface{}{"transportSender": "other@example.com"}, ok: true, sent: 1},
		{name: "Cc only recipients", ccOnly: true, ok: true, sent: 1},
		{name: "provider independence IMAP", config: map[string]interface{}{"accounts": []interface{}{map[string]interface{}{"name": "Account", "id": "imap-id", "enabled": true, "addresses": []string{"sender@example.com"}, "type": "IMAP"}}}, ok: true, sent: 1},
		{name: "provider independence exchange", config: map[string]interface{}{"accounts": []interface{}{map[string]interface{}{"name": "Account", "id": "exchange-id", "enabled": true, "addresses": []string{"sender@example.com"}, "type": "Exchange"}}}, ok: true, sent: 1},
		{name: "quoted display comma", config: map[string]interface{}{"readback": "\"Person, Example\" <sender@example.com>"}, ok: true, sent: 1},
		{name: "case insensitive readback", config: map[string]interface{}{"readback": "SENDER@example.com"}, ok: true, sent: 1},
		{name: "final sender guard", config: map[string]interface{}{"fallbackBeforeMutation": true}, deleted: 1},
		{name: "fresh final account guard", config: map[string]interface{}{"disableBeforeMutation": true}, deleted: 1},
		{name: "display name readback", config: map[string]interface{}{"readback": "Person <sender@example.com>"}, ok: true, sent: 1},
		{name: "default fallback readback", config: map[string]interface{}{"readback": "other@example.com", "ignoreSetter": true}, deleted: 1},
		{name: "multiple sender readback", config: map[string]interface{}{"readback": "sender@example.com, other@example.com"}, deleted: 1},
		{name: "invalid sender readback", config: map[string]interface{}{"readback": "invalid"}, deleted: 1},
		{name: "throwing setter", config: map[string]interface{}{"setterThrows": true}, deleted: 1},
		{name: "throwing getter", config: map[string]interface{}{"getterThrows": true}, deleted: 1},
		{name: "account getter error", config: map[string]interface{}{"accountGetterThrows": true}},
		{name: "empty account addresses", config: map[string]interface{}{"accounts": []interface{}{map[string]interface{}{"name": "Account", "id": "id-1", "enabled": true, "addresses": []string{}}}}},
		{name: "disabled", config: map[string]interface{}{"accounts": []interface{}{map[string]interface{}{"name": "Account", "id": "id-1", "enabled": false, "addresses": []string{"sender@example.com"}}}}},
		{name: "duplicate account names", config: map[string]interface{}{"accounts": []interface{}{map[string]interface{}{"name": "Account", "id": "id-1", "enabled": true, "addresses": []string{"sender@example.com"}}, map[string]interface{}{"name": "Account", "id": "id-2", "enabled": true, "addresses": []string{"other@example.com"}}}}},
		{name: "duplicate account IDs", config: map[string]interface{}{"accounts": []interface{}{map[string]interface{}{"name": "Account", "id": "id-1", "enabled": true, "addresses": []string{"sender@example.com"}}, map[string]interface{}{"name": "Other", "id": "id-1", "enabled": true, "addresses": []string{"other@example.com"}}}}},
		{name: "shared ownership", config: map[string]interface{}{"accounts": []interface{}{map[string]interface{}{"name": "Account", "id": "id-1", "enabled": true, "addresses": []string{"sender@example.com"}}, map[string]interface{}{"name": "Other", "id": "id-2", "enabled": true, "addresses": []string{"SENDER@example.com"}}}}},
		{name: "explicit alias", sender: "alias@example.com", config: map[string]interface{}{"accounts": []interface{}{map[string]interface{}{"name": "Account", "id": "id-1", "enabled": true, "addresses": []string{"sender@example.com", "alias@example.com"}}}}, ok: true, sent: 1},
		{name: "alias case insensitive match", ok: true, sent: 1, sender: "ALIAS@example.com", config: map[string]interface{}{"accounts": []interface{}{map[string]interface{}{"name": "Account", "id": "id-1", "enabled": true, "addresses": []string{"alias@example.com"}}}}},
		{name: "send false", config: map[string]interface{}{"sendFalse": true}, sent: 1, uncertain: true},
		{name: "send throw", config: map[string]interface{}{"sendThrows": true}, sent: 1, uncertain: true},
		{name: "attachment accepted", attachments: []string{"/tmp/report.pdf"}, ok: true, sent: 1},
		{name: "attachment escaped quote comma and backslash", attachments: []string{`/tmp/quote", comma\file.pdf`}, ok: true, sent: 1},
		{name: "attachment throws", attachments: []string{"/tmp/report.pdf"}, config: map[string]interface{}{"attachmentThrows": true}, deleted: 1},
		{name: "attachment wait exhausted", attachments: []string{"/tmp/report.pdf"}, config: map[string]interface{}{"attachNeverReady": true}, deleted: 1},
		{name: "cleanup failure", config: map[string]interface{}{"readback": "other@example.com", "cleanupThrows": true}, deleted: 1},
		{name: "preview no composition", action: "preview", ok: true},
		{name: "save transient missing object", action: "save", config: map[string]interface{}{"transientLookup": true}, ok: true, closed: 1},
		{name: "save transport failure", action: "save", config: map[string]interface{}{"transportLookup": true}, closed: 1, uncertain: true},
		{name: "save delayed direct evidence", action: "save", config: map[string]interface{}{"saveDelay": 4, "preexisting": true}, ok: true, closed: 1},
		{name: "save ambiguous", action: "save", config: map[string]interface{}{"ambiguousSave": true}, closed: 1, uncertain: true},
		{name: "save not visible", action: "save", config: map[string]interface{}{"neverSave": true}, closed: 1, uncertain: true},
		{name: "save sender mismatch", action: "save", config: map[string]interface{}{"savedChange": map[string]interface{}{"sender": "other@example.com"}}, closed: 1, uncertain: true},
		{name: "save recipients mismatch", action: "save", config: map[string]interface{}{"savedChange": map[string]interface{}{"bcc": []string{"other@example.com"}}}, closed: 1, uncertain: true},
		{name: "save body whitespace mismatch", action: "save", config: map[string]interface{}{"savedChange": map[string]interface{}{"body": " Body "}}, closed: 1, uncertain: true},
		{name: "save body mismatch", action: "save", config: map[string]interface{}{"savedChange": map[string]interface{}{"body": "different"}}, closed: 1, uncertain: true},
		{name: "save attachments mismatch", action: "save", attachments: []string{"/tmp/report.pdf"}, config: map[string]interface{}{"savedChange": map[string]interface{}{"attachments": []string{}}}, closed: 1, uncertain: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action := tc.action
			if action == "" {
				action = "send"
			}
			input := OutgoingInput{Account: "Account", Sender: tc.sender, Subject: "Subject", Body: "Body", To: []string{"to@example.com"}, Cc: []string{"cc@example.com"}, Bcc: []string{"bcc@example.com"}, Attachments: tc.attachments}
			if tc.ccOnly {
				input.To = nil
				input.Bcc = nil
			}
			payload, _ := json.Marshal(map[string]interface{}{"helper": jxaPhaseHelper() + jxaOutgoingHelper(), "input": input, "action": action, "config": tc.config})
			command := exec.Command(node, "testdata/outgoing_mock.js")
			command.Stdin = bytes.NewReader(payload)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("execute production JavaScript: %v %s", err, output)
			}
			var got struct {
				ComposedBody                           string `json:"composedBody"`
				TransportSender                        string
				Response                               outgoingEnvelope `json:"response"`
				Composed, Sent, Closed, Deleted, Polls int
			}
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatal(err)
			}
			// Successful sends close the same owned composition without saving.
			expectedDeleted := tc.deleted
			if tc.ok && action == "send" {
				expectedDeleted++
			}
			if got.Response.OK != tc.ok || got.Sent != tc.sent || got.Closed != tc.closed || got.Deleted != expectedDeleted || got.Response.Uncertain != tc.uncertain {
				t.Fatalf("unexpected execution: %s", output)
			}
			if got.Sent == 1 && got.ComposedBody != input.Body+"\n" {
				t.Fatalf("submission changed body: %s", output)
			}
			if tc.ok && action == "save" && got.Response.Result.Draft.ID != "stored-900" {
				t.Fatalf("assumed outgoing/stored IDs equal: %s", output)
			}
			if tc.name == "cleanup failure" && !strings.Contains(got.Response.Cleanup, "cleanup failed") {
				t.Fatalf("lost cleanup failure: %s", output)
			}
			if tc.name == "save delayed direct evidence" && got.Polls < 4 {
				t.Fatalf("did not poll fresh Mail evidence: %s", output)
			}
			if tc.name == "later transport switch is outside identity contract" && (got.TransportSender == got.Response.Result.Sender || !got.Response.Result.Submitted) {
				t.Fatalf("mock should expose identity verification cannot prove transport routing: %s", output)
			}
			if action == "preview" && got.Composed != 0 {
				t.Fatal("preview composed a message")
			}
		})
	}
}

func TestOutgoingProductionScriptJXASyntax(t *testing.T) {
	compiler, err := exec.LookPath("osacompile")
	if err != nil {
		t.Skip("macOS osacompile is unavailable")
	}
	payload, _ := json.Marshal(OutgoingInput{Account: "Account", Subject: "Subject", Body: "Body", To: []string{"to@example.com"}})
	script := jxaPhaseHelper() + jxaOutgoingHelper() + "\nJSON.stringify(composeOutgoing(" + string(payload) + ", 'send'));"
	output, err := exec.Command(compiler, "-l", "JavaScript", "-o", filepath.Join(t.TempDir(), "outgoing.scpt"), "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("production JXA syntax: %v %s", err, output)
	}
}

func TestOutgoingStrictReceipts(t *testing.T) {
	cases := []string{"Success", `{"ok":true}`, `{"ok":true,"result":{"account":"Account","accountId":"id-1","sender":"sender@example.com","status":"submitted","submitted":false}}`, `{"ok":true,"result":{"account":"Other","accountId":"id-1","sender":"sender@example.com","status":"submitted","submitted":true}}`}
	for _, output := range cases {
		t.Run(output, func(t *testing.T) {
			t.Setenv("OUTGOING_RESPONSE", output)
			writeFakeOsaScript(t, `printf '%s\n' "$OUTGOING_RESPONSE"`)
			_, err := NewClient().SendOutgoing(OutgoingInput{Account: "Account", Subject: "Subject", Body: "Body", To: []string{"to@example.com"}})
			if err == nil {
				t.Fatalf("accepted malformed receipt %s", output)
			}
			var mutation *MutationError
			if !errors.As(err, &mutation) || !mutation.Uncertain {
				t.Fatalf("missing uncertainty: %v", err)
			}
		})
	}
}

func TestLegacyOutgoingSubmittedCleanup(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for production outgoing tests")
	}
	tests := []struct {
		name           string
		config         map[string]any
		attachments    []string
		submitted      bool
		closes         int
		ok             bool
		beforeDispatch bool
	}{
		{name: "missing value filename uses strict count", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"filenameMissingValue": true, "intrinsicBoolean": true}, submitted: true, closes: 1, ok: true},
		{name: "previously readable filename becomes missing value retains", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"filenameMissingValueAfterSend": true}, submitted: true},
		{name: "malformed filename is not missing value", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"filenameMalformed": true}, beforeDispatch: true, closes: 1},
		{name: "missing filename getter uses strict count", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"filenameMissing": true, "intrinsicBoolean": true}, submitted: true, closes: 1, ok: true},
		{name: "missing previously readable filename retains", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"filenameMissingAfterSend": true}, submitted: true},
		{name: "truthy ObjC nil wrapper fails closed", config: map[string]any{"snapshotNil": true}, beforeDispatch: true, closes: 1},
		{name: "required count missing before dispatch", config: map[string]any{"snapshotCountMissing": true}, beforeDispatch: true, closes: 1},
		{name: "required count missing after ack", config: map[string]any{"snapshotCountMissing": true, "failAfterSend": true}, submitted: true},
		{name: "owned disappears during full fingerprint", config: map[string]any{"ownedDisappearsDuringFingerprint": true}, submitted: true, ok: true},
		{name: "owned disappears during close", config: map[string]any{"ownedDisappearsAtClose": true}, submitted: true, closes: 1, ok: true},
		{name: "unchanged owned composition", submitted: true, closes: 1, ok: true},
		{name: "unchanged attachments", attachments: []string{"/tmp/report.pdf"}, submitted: true, closes: 1, ok: true},
		{name: "subject changed", config: map[string]any{"afterSend": map[string]any{"subject": "edited"}}, submitted: true},
		{name: "body changed", config: map[string]any{"afterSend": map[string]any{"body": "edited"}}, submitted: true},
		{name: "sender changed", config: map[string]any{"afterSend": map[string]any{"sender": "other@example.com"}}, submitted: true},
		{name: "To changed", config: map[string]any{"afterSend": map[string]any{"to": []string{"other@example.com"}}}, submitted: true},
		{name: "Cc changed", config: map[string]any{"afterSend": map[string]any{"cc": []string{"other@example.com"}}}, submitted: true},
		{name: "Bcc changed", config: map[string]any{"afterSend": map[string]any{"bcc": []string{"other@example.com"}}}, submitted: true},
		{name: "unsupported baseline filenames uses strict count", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"filenameUnsupported": true}, submitted: true, closes: 1, ok: true},
		{name: "previously readable filenames become unsupported", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"filenameUnsupportedAfterSend": true}, submitted: true},
		{name: "attachment name changed", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"afterSend": map[string]any{"attachments": []string{"other.pdf"}}}, submitted: true},
		{name: "attachment count changed", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"afterSend": map[string]any{"attachments": []string{"report.pdf", "report.pdf"}}}, submitted: true},
		{name: "nested recipient missing is not owned absence", config: map[string]any{"fingerprintFailures": []string{"cc"}, "failAfterSend": true}, submitted: true},
		{name: "nested attachment missing is not owned absence", config: map[string]any{"fingerprintFailures": []string{"attachments"}, "failAfterSend": true}, submitted: true},
		{name: "body read failure retains composition", config: map[string]any{"fingerprintFailures": []string{"body"}, "failAfterSend": true}, submitted: true},
		{name: "owned object already gone", config: map[string]any{"fingerprintFailures": []string{"subject"}, "failAfterSend": true}, submitted: true, ok: true},
		{name: "intrinsic true availability descriptor", config: map[string]any{"intrinsicBoolean": true}, submitted: true, closes: 1, ok: true},
		{name: "intrinsic true attachment filenames", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"intrinsicBoolean": true}, submitted: true, closes: 1, ok: true},
		{name: "intrinsic false unavailable filename descriptor", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"intrinsicBoolean": true, "filenameUnsupported": true}, submitted: true, closes: 1, ok: true},
		{name: "intrinsic true with unavailable filename list retains", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"malformedSnapshot": map[string]string{"flag": "true"}, "malformedAfterSend": true, "filenameUnsupportedAfterSend": true}, submitted: true},
		{name: "intrinsic false with inconsistent filename list retains", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"malformedSnapshot": map[string]string{"flag": "fals"}, "malformedAfterSend": true}, submitted: true},
		{name: "wrong root descriptor retains", config: map[string]any{"malformedSnapshot": map[string]string{"root": "utxt"}, "malformedAfterSend": true}, submitted: true},
		{name: "wrong count descriptor retains", config: map[string]any{"malformedSnapshot": map[string]string{"count": "utxt"}, "malformedAfterSend": true}, submitted: true},
		{name: "wrong availability descriptor retains", config: map[string]any{"malformedSnapshot": map[string]string{"flag": "long"}, "malformedAfterSend": true}, submitted: true},
		{name: "wrong names list descriptor retains", config: map[string]any{"malformedSnapshot": map[string]string{"names": "utxt"}, "malformedAfterSend": true}, submitted: true},
		{name: "wrong filename descriptor retains", attachments: []string{"/tmp/report.pdf"}, config: map[string]any{"malformedSnapshot": map[string]string{"name": "long"}, "malformedAfterSend": true}, submitted: true},
		{name: "close failure preserves acknowledgement", config: map[string]any{"cleanupThrows": true}, submitted: true, closes: 1},
		{name: "send false retains", config: map[string]any{"sendFalse": true}},
		{name: "send throws retains", config: map[string]any{"sendThrows": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := OutgoingInput{Account: "Account", Subject: "Subject", Body: "Body", To: []string{"to@example.com"}, Cc: []string{"cc@example.com"}, Bcc: []string{"bcc@example.com"}, Attachments: tt.attachments}
			payload, _ := json.Marshal(map[string]any{"helper": jxaPhaseHelper() + jxaOutgoingHelper(), "input": input, "action": "send", "config": tt.config})
			cmd := exec.Command(node, "testdata/outgoing_mock.js")
			cmd.Stdin = bytes.NewReader(payload)
			raw, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("mock: %v %s", err, raw)
			}
			var got struct {
				Response      outgoingEnvelope
				Sent, Deleted int
				Events        []json.RawMessage
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			expectedSent := 1
			if tt.beforeDispatch {
				expectedSent = 0
			}
			if got.Response.OK != tt.ok || got.Response.Result.Submitted != tt.submitted || got.Sent != expectedSent || got.Deleted != tt.closes {
				t.Fatalf("execution=%s", raw)
			}
			if tt.submitted && !tt.ok {
				if got.Response.Phase != "submitted_cleanup" || got.Response.Result.Status != "submitted_cleanup_pending" || got.Response.Cleanup == "" || got.Response.Uncertain {
					t.Fatalf("lost authoritative ack=%s", raw)
				}
			}
			if tt.name == "required count missing before dispatch" || tt.name == "required count missing after ack" {
				if !strings.Contains(got.Response.Error, "AppleScript error -1728") {
					t.Fatalf("lost nil-wrapper error metadata: %s", raw)
				}
			}
			if tt.name == "malformed filename is not missing value" && !strings.Contains(got.Response.Error, "AppleScript error -1700") {
				t.Fatalf("malformed filename coercion was swallowed: %s", raw)
			}
			if tt.beforeDispatch && (got.Response.Uncertain || got.Response.Phase != "send_fingerprint") {
				t.Fatalf("pre-dispatch count failure not safe: %s", raw)
			}
			if tt.name == "wrong availability descriptor retains" && (!strings.Contains(got.Response.Error, `available="long"`) || !strings.Contains(got.Response.Error, "1819242087")) {
				t.Fatalf("missing descriptor diagnostics: %s", raw)
			}
			if !tt.submitted && !tt.beforeDispatch && !got.Response.Uncertain {
				t.Fatalf("lost send uncertainty=%s", raw)
			}
		})
	}
}

func TestLegacyOutgoingCleanupErrorPreservesReceipt(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	writeFakeOsaScript(t, `printf '%s' '{"ok":false,"result":{"account":"Account","accountId":"id-1","sender":"sender@example.com","status":"submitted_cleanup_pending","submitted":true},"phase":"submitted_cleanup","error":"composition changed; retained","cleanup":"cleanup pending","attempted":true,"uncertain":false}'`)
	result, err := NewClient().SendOutgoing(OutgoingInput{Account: "Account", Subject: "Subject", Body: "Body", To: []string{"to@example.com"}})
	var mutation *MutationError
	if result == nil || !result.Submitted || !errors.As(err, &mutation) || !mutation.Submitted || mutation.Uncertain || mutation.Phase != "submitted_cleanup" || mutation.Cleanup == "" {
		t.Fatalf("result=%+v err=%+v", result, err)
	}
}

func TestLegacyOutgoingInterruptedAfterAcknowledgement(t *testing.T) {
	for _, tt := range []struct {
		name, tail string
		cancel     bool
	}{
		{name: "bridge failure after ack", tail: "exit 1"},
		{name: "caller deadline during cleanup", tail: "/bin/sleep 5", cancel: true},
		{name: "invalid final JSON after ack", tail: "printf '%s' invalid"},
		{name: "empty final JSON object after ack", tail: `printf '%s' '{}'`},
		{name: "empty success receipt after ack", tail: `printf '%s' '{"ok":true}'`},
		{name: "incomplete success receipt after ack", tail: `printf '%s' '{"ok":true,"result":{"account":"Account","status":"submitted","submitted":true}}'`},
		{name: "false submitted receipt after ack", tail: `printf '%s' '{"ok":true,"result":{"account":"Account","accountId":"id-1","sender":"sender@example.com","status":"submitted","submitted":false}}'`},
		{name: "wrong account receipt after ack", tail: `printf '%s' '{"ok":true,"result":{"account":"Other","accountId":"id-1","sender":"sender@example.com","status":"submitted","submitted":true}}'`},
		{name: "contradictory cleanup flags after ack", tail: `printf '%s' '{"ok":false,"result":{"account":"Account","accountId":"id-1","sender":"sender@example.com","status":"submitted_cleanup_pending","submitted":true},"phase":"submitted_cleanup","error":"retained","cleanup":"pending","attempted":false,"uncertain":true}'`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			ready := filepath.Join(t.TempDir(), "ready")
			t.Setenv("OUTGOING_ACK_READY", ready)
			writeFakeOsaScript(t, `printf '%s\n' 'mail-app-cli-phase {"name":"outgoing_send","state":"done"}' 'mail-app-cli-phase {"name":"outgoing_submitted_cleanup","state":"start"}' >&2
printf ready > "$OUTGOING_ACK_READY"
`+tt.tail)
			client := NewClient()
			input := OutgoingInput{Account: "Account", Subject: "Subject", Body: "Body", To: []string{"to@example.com"}}
			var result *OutgoingResult
			var err error
			if tt.cancel {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				client = client.WithContext(ctx)
				type receipt struct {
					result *OutgoingResult
					err    error
				}
				finished := make(chan receipt, 1)
				go func() { result, err := client.SendOutgoing(input); finished <- receipt{result, err} }()
				waitForAutomationLog(t, ready, "ready")
				cancel()
				got := <-finished
				result, err = got.result, got.err
			} else {
				result, err = client.SendOutgoing(input)
			}
			var mutation *MutationError
			if result == nil || !result.Submitted || result.Status != "submitted_cleanup_pending" || !errors.As(err, &mutation) || !mutation.Submitted || !mutation.Attempted || mutation.Uncertain || mutation.Cleanup == "" || !strings.Contains(err.Error(), "do not resend") {
				t.Fatalf("result=%+v error=%+v", result, err)
			}
			if tt.name == "wrong account receipt after ack" && (result.Account != input.Account || result.AccountID != "" || result.Sender != input.Sender) {
				t.Fatalf("untrusted receipt identity survived: %+v", result)
			}
		})
	}
}

func TestLegacyOutgoingPreparedDraftCleanup(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for outgoing production tests")
	}
	tests := []struct {
		name          string
		config        map[string]any
		sent          int
		submitted, ok bool
		deletes       int
	}{
		{name: "owned body edited during prepare prevents send", config: map[string]any{"duringPrepare": map[string]any{"body": "edited"}}},
		{name: "owned recipient edited during prepare prevents send", config: map[string]any{"duringPrepare": map[string]any{"to": []string{"other@example.com"}}}},
		{name: "owned attachment edited during prepare prevents send", config: map[string]any{"duringPrepare": map[string]any{"attachments": []string{"different.pdf"}}}},
		{name: "null IDs list after disappearance cannot establish absence", config: map[string]any{"preparedDisappearsDuringSnapshot": true, "preparedIdsAfterRace": nil}, sent: 1, submitted: true},
		{name: "null ID after disappearance cannot establish absence", config: map[string]any{"preparedDisappearsDuringSnapshot": true, "preparedIdsAfterRace": []any{nil}}, sent: 1, submitted: true},
		{name: "object ID after disappearance cannot establish absence", config: map[string]any{"preparedDisappearsDuringSnapshot": true, "preparedIdsAfterRace": []any{map[string]any{}}}, sent: 1, submitted: true},
		{name: "nonnumeric ID after disappearance cannot establish absence", config: map[string]any{"preparedDisappearsDuringSnapshot": true, "preparedIdsAfterRace": []any{"bad"}}, sent: 1, submitted: true},
		{name: "duplicate IDs after disappearance cannot establish absence", config: map[string]any{"preparedDisappearsDuringSnapshot": true, "preparedIdsAfterRace": []any{100, 100}}, sent: 1, submitted: true},
		{name: "zero ID after disappearance cannot establish absence", config: map[string]any{"preparedDisappearsDuringSnapshot": true, "preparedIdsAfterRace": []any{0}}, sent: 1, submitted: true},
		{name: "unsafe ID after disappearance cannot establish absence", config: map[string]any{"preparedDisappearsDuringSnapshot": true, "preparedIdsAfterRace": []any{9007199254740992}}, sent: 1, submitted: true},
		{name: "prepared disappears after positive IDs before byId", config: map[string]any{"preparedDisappearsAtById": true}, sent: 1, submitted: true, ok: true},
		{name: "prepared disappears during snapshot", config: map[string]any{"preparedDisappearsDuringSnapshot": true}, sent: 1, submitted: true, ok: true},
		{name: "prepared lingering ID clears after snapshot disappearance", config: map[string]any{"preparedDisappearsDuringSnapshot": true, "preparedLingeringIdReads": 3}, sent: 1, submitted: true, ok: true},
		{name: "prepared lingering ID never clears after disappearance", config: map[string]any{"preparedDisappearsDuringSnapshot": true, "preparedLingeringIdReads": 100}, sent: 1, submitted: true},
		{name: "nested stored read missing with ID present retains", config: map[string]any{"storedNestedMissing": "source"}, sent: 1, submitted: true},
		{name: "deletes only prepared stored ID and leaves old draft", config: map[string]any{"preexisting": true}, sent: 1, submitted: true, ok: true, deletes: 1},
		{name: "stable by-ID reference survives ordinal reorder", config: map[string]any{"preexisting": true, "reorderAfterLookup": true}, sent: 1, submitted: true, ok: true, deletes: 1},
		{name: "stored already absent after send", config: map[string]any{"storedAbsent": true, "preexisting": true}, sent: 1, submitted: true, ok: true},
		{name: "changed raw source retained", config: map[string]any{"storedAfterSend": map[string]any{"source": "edited raw source"}}, sent: 1, submitted: true},
		{name: "changed body retained", config: map[string]any{"storedAfterSend": map[string]any{"body": "edited"}}, sent: 1, submitted: true},
		{name: "changed To retained", config: map[string]any{"storedAfterSend": map[string]any{"to": []string{"other@example.com"}}}, sent: 1, submitted: true},
		{name: "changed Cc retained", config: map[string]any{"storedAfterSend": map[string]any{"cc": []string{"other@example.com"}}}, sent: 1, submitted: true},
		{name: "changed Bcc retained", config: map[string]any{"storedAfterSend": map[string]any{"bcc": []string{"other@example.com"}}}, sent: 1, submitted: true},
		{name: "changed filename retained", config: map[string]any{"storedAfterSend": map[string]any{"attachments": []string{"/tmp/other.pdf"}}}, sent: 1, submitted: true},
		{name: "owner changes immediately before delete", config: map[string]any{"accountIdChangeAtRead": 6}, sent: 1, submitted: true},
		{name: "owner changes after verified absence", config: map[string]any{"accountIdChangeAtRead": 7}, sent: 1, submitted: true, deletes: 1},
		{name: "changed owner retained", config: map[string]any{"storedAfterSend": map[string]any{"accountId": "other-account"}}, sent: 1, submitted: true},
		{name: "changed mailbox retained", config: map[string]any{"storedAfterSend": map[string]any{"mailbox": "Other Drafts"}}, sent: 1, submitted: true},
		{name: "stored source read failure retains", config: map[string]any{"storedReadThrows": "source"}, sent: 1, submitted: true},
		{name: "delete failure preserves acknowledgement", config: map[string]any{"storedDeleteThrows": true}, sent: 1, submitted: true, deletes: 1},
		{name: "delete absence verification failure preserves acknowledgement", config: map[string]any{"storedDeleteNoEffect": true}, sent: 1, submitted: true, deletes: 1},
		{name: "false send keeps prepared copy", config: map[string]any{"sendFalse": true}, sent: 1},
		{name: "throwing send keeps prepared copy", config: map[string]any{"sendThrows": true}, sent: 1},
		{name: "ambiguous prepared copy prevents send", config: map[string]any{"ambiguousSave": true}},
		{name: "unverified prepared copy prevents send", config: map[string]any{"neverSave": true}},
		{name: "empty raw source prevents send", config: map[string]any{"savedChange": map[string]any{"source": ""}}},
		{name: "prepared recipient mismatch prevents send", config: map[string]any{"savedChange": map[string]any{"bcc": []string{"other@example.com"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := OutgoingInput{Account: "Account", Subject: "Subject", Body: "Body", To: []string{"to@example.com"}, Cc: []string{"cc@example.com"}, Bcc: []string{"bcc@example.com"}, Attachments: []string{"/tmp/report.pdf"}}
			payload, _ := json.Marshal(map[string]any{"helper": jxaPhaseHelper() + jxaOutgoingHelper(), "input": input, "action": "send", "config": tt.config})
			command := exec.Command(node, "testdata/outgoing_mock.js")
			command.Stdin = bytes.NewReader(payload)
			raw, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("production script: %v %s", err, raw)
			}
			var got struct {
				Response                    outgoingEnvelope
				Sent, Deleted               int
				StoredDeleted, ExactLookups []string
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Response.OK != tt.ok || got.Response.Result.Submitted != tt.submitted || got.Sent != tt.sent || len(got.StoredDeleted) != tt.deletes {
				t.Fatalf("execution=%s", raw)
			}
			for _, id := range got.StoredDeleted {
				if id != "900" {
					t.Fatalf("deleted unrelated stored draft: %s", raw)
				}
			}
			for _, id := range got.ExactLookups {
				if id != "900" && !(tt.name == "ambiguous prepared copy prevents send" && id == "901") {
					t.Fatalf("read unrelated draft by ID: %s", raw)
				}
			}
			if tt.name == "ambiguous prepared copy prevents send" && (len(got.ExactLookups) != 2 || !strings.Contains(got.Response.Error, "Prepared draft identity is ambiguous")) {
				t.Fatalf("did not inspect and reject both new matching copies: %s", raw)
			}
			if tt.submitted && !tt.ok && (got.Response.Uncertain || got.Response.Result.Status != "submitted_cleanup_pending" || got.Response.Cleanup == "") {
				t.Fatalf("lost acknowledged send: %s", raw)
			}
			if tt.sent > 0 && !tt.submitted && (got.Deleted != 0 || !got.Response.Uncertain) {
				t.Fatalf("uncertain send performed cleanup: %s", raw)
			}
			if strings.Contains(tt.name, "during prepare") && got.Response.Phase != "prepared_compose_verification" {
				t.Fatalf("missed pre-send composition guard: %s", raw)
			}
			if tt.name == "nested stored read missing with ID present retains" && (!strings.Contains(got.Response.Error, "prepared_snapshot failed") || !strings.Contains(got.Response.Error, "-1728") || strings.Contains(got.Response.Error, "stored nested unavailable")) {
				t.Fatalf("missing sanitized stage/error diagnostic: %s", raw)
			}
			if tt.sent == 0 && got.Response.Uncertain {
				t.Fatalf("pre-send preparation reported send uncertainty: %s", raw)
			}
		})
	}
}

func TestLegacyOutgoingInterruptedPreparedSave(t *testing.T) {
	for _, tt := range []struct {
		name, state string
		saved       bool
	}{
		{name: "prepared save in flight", state: "start"},
		{name: "prepared save acknowledged", state: "done", saved: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			script := `printf '%s\n' 'mail-app-cli-phase {"name":"outgoing_compose","state":"done"}' 'mail-app-cli-phase {"name":"outgoing_prepare_save","state":"` + tt.state + `"}' >&2
exit 1`
			writeFakeOsaScript(t, script)
			result, err := NewClient().SendOutgoing(OutgoingInput{Account: "Account", Subject: "Subject", Body: "Body", To: []string{"to@example.com"}})
			var mutation *MutationError
			if result != nil || !errors.As(err, &mutation) || mutation.Attempted || mutation.Submitted || mutation.Uncertain || mutation.Saved != tt.saved || mutation.Cleanup == "" || !strings.Contains(mutation.Cleanup, "inspect Mail before retrying") {
				t.Fatalf("result=%+v error=%+v", result, err)
			}
		})
	}
}

func TestLegacyOutgoingAttachmentStoredBodyProof(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for outgoing production tests")
	}
	cases := []struct {
		name                                           string
		body                                           string
		count                                          int
		markers                                        bool
		markerCount                                    *int
		prefix, suffix                                 string
		extraOwned                                     string
		unavailableNames, changeDuringSave, userMarker bool
		ok                                             bool
	}{
		{name: "one attachment exact stored layout", count: 1, markers: true, prefix: "\n\u2028", ok: true},
		{name: "two attachments exact stored layout", count: 2, markers: true, prefix: "\n\u2028", ok: true},
		{name: "actual compose terminator retained", body: "body without terminator", count: 2, markers: true, prefix: "\n\u2028", ok: true},
		{name: "unsupported owned filename getter retains count proof", count: 2, markers: true, prefix: "\n\u2028", unavailableNames: true, ok: true},
		{name: "missing owned object markers", count: 2, prefix: "\n\u2028"},
		{name: "too few owned object markers", count: 2, markers: true, markerCount: func() *int { v := 1; return &v }(), prefix: "\n\u2028"},
		{name: "too many owned object markers", count: 2, markers: true, markerCount: func() *int { v := 3; return &v }(), prefix: "\n\u2028"},
		{name: "no attachment objects cannot authorize separator", prefix: "\n\u2028"},
		{name: "caller supplied object marker is rejected", count: 2, markers: true, prefix: "\n\u2028", userMarker: true},
		{name: "extra owned text is rejected", count: 2, markers: true, prefix: "\n\u2028", extraOwned: "different"},
		{name: "extra stored text is rejected", count: 2, markers: true, prefix: "\n\u2028", suffix: "different"},
		{name: "extra stored whitespace is rejected", count: 2, markers: true, prefix: "\n\u2028", suffix: " "},
		{name: "extra stored line separator is rejected", count: 2, markers: true, prefix: "\n\u2028\u2028"},
		{name: "changed owned body during save is rejected", count: 2, markers: true, prefix: "\n\u2028", changeDuringSave: true},
		{name: "existing exact body match remains", count: 2, ok: true},
		{name: "existing leading LF match remains", count: 2, prefix: "\n", ok: true},
	}
	for _, action := range []string{"save", "send"} {
		for _, tt := range cases {
			t.Run(action+"/"+tt.name, func(t *testing.T) {
				body := tt.body
				if body == "" {
					body = "\nOriginal body\n"
				}
				if tt.userMarker {
					body += "\uFFFC"
				}
				compose := body
				if !strings.HasSuffix(compose, "\n") {
					compose += "\n"
				}
				attachments := make([]string, tt.count)
				for i := range attachments {
					attachments[i] = fmt.Sprintf("/tmp/file-%d.txt", i)
				}
				config := map[string]any{"ownedAttachmentMarkers": tt.markers, "ownedBodyExtraBeforeMarkers": tt.extraOwned, "filenameUnsupported": tt.unavailableNames, "savedChange": map[string]any{"body": tt.prefix + compose + tt.suffix}}
				if tt.markerCount != nil {
					config["ownedMarkerCount"] = *tt.markerCount
				}
				if tt.changeDuringSave {
					config["duringPrepare"] = map[string]any{"body": "edited while saving"}
				}
				input := OutgoingInput{Account: "Account", Subject: "Subject", Body: body, To: []string{"to@example.com"}, Cc: []string{"cc@example.com"}, Bcc: []string{"bcc@example.com"}, Attachments: attachments}
				payload, _ := json.Marshal(map[string]any{"helper": jxaPhaseHelper() + jxaOutgoingHelper(), "input": input, "action": action, "config": config})
				command := exec.Command(node, "testdata/outgoing_mock.js")
				command.Stdin = bytes.NewReader(payload)
				raw, err := command.CombinedOutput()
				if err != nil {
					t.Fatalf("production script: %v %s", err, raw)
				}
				var got struct {
					Response     outgoingEnvelope
					Sent, Closed int
				}
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				if got.Response.OK != tt.ok || (!tt.ok && got.Sent != 0) || (action == "send" && tt.ok && got.Sent != 1) {
					t.Fatalf("execution=%s", raw)
				}
				if action == "save" && tt.ok && (got.Closed != 1 || got.Response.Result.Draft == nil || got.Response.Result.Draft.Content != tt.prefix+compose+tt.suffix) {
					t.Fatalf("save did not retain exact stored content: %s", raw)
				}
				if tt.changeDuringSave && action == "save" && !got.Response.Saved {
					t.Fatalf("save acknowledgement lost after fingerprint failure: %s", raw)
				}
				if tt.changeDuringSave && got.Closed != 0 {
					t.Fatalf("changed owned body was closed: %s", raw)
				}
			})
		}
	}
}
