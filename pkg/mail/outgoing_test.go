package mail

import (
	"bytes"
	"encoding/json"
	"errors"
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
			if got.Response.OK != tc.ok || got.Sent != tc.sent || got.Closed != tc.closed || got.Deleted != tc.deleted || got.Response.Uncertain != tc.uncertain {
				t.Fatalf("unexpected execution: %s", output)
			}
			if got.Sent == 1 && got.ComposedBody != input.Body {
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
