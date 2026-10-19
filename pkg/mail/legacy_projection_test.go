package mail

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// Execute the production scripts against plain JavaScript Mail objects. This
// checks projections and getter order without contacting Mail.app.
func TestLegacyMessageProjectionGetterParity(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	base := []string{"id", "subject", "sender", "dateReceived", "dateSent", "readStatus", "flaggedStatus"}
	withSize := append(append([]string{}, base...), "messageSize")
	tests := []struct {
		name    string
		read    func(*Client) error
		calls   []string
		content bool
		rfc     bool
		size    int
	}{
		{"list", func(c *Client) error {
			_, err := c.getMessagesJSONFromJXA("Work", "INBOX", 1, 0, false, false, "")
			return err
		}, append([]string{"deletedStatus"}, base...), true, false, 0},
		{"archive list", func(c *Client) error {
			_, err := c.getArchiveMessagesWithWhoseJXA("Work", "INBOX", 1, 0, false, false, "")
			return err
		}, append([]string{"deletedStatus"}, withSize...), true, false, 7},
		{"search", func(c *Client) error {
			_, err := c.searchMessagesInSingleMailboxJXA("weekly", "Work", "INBOX", 1, "")
			return err
		}, append([]string{"subject", "sender", "content", "dateReceived"}, withSize...), false, false, 7},
		{"envelope", func(c *Client) error { _, err := c.getMessageEnvelopeJXA("Work", "INBOX", "42"); return err }, append([]string{"id"}, withSize...), false, false, 7},
		{"strict verification", func(c *Client) error {
			_, err := c.GetMessageDetailsForVerificationJSON("Work", "INBOX", "42")
			return err
		}, append([]string{"id", "id", "messageId"}, withSize[1:]...), false, true, 7},
		{"detail", func(c *Client) error {
			_, _, err := c.getMessageDetailsTraced("Work", "INBOX", "42", time.Second, false)
			return err
		}, append([]string{"id", "content", "toRecipients", "ccRecipients", "bccRecipients", "id", "messageId"}, withSize[1:]...), true, true, 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
			scriptPath := filepath.Join(t.TempDir(), "read.js")
			t.Setenv("CAPTURE_SCRIPT", scriptPath)
			writeFakeOsaScript(t, `for arg in "$@"; do script="$arg"; done
printf '%s' "$script" > "$CAPTURE_SCRIPT"
printf '[{"id":"42"}]\n'
`)
			// Optional reads need an object/envelope rather than the list fixture.
			if test.name == "envelope" || test.name == "strict verification" || test.name == "detail" {
				writeFakeOsaScript(t, `for arg in "$@"; do script="$arg"; done
printf '%s' "$script" > "$CAPTURE_SCRIPT"
printf '{"id":"42","message":{"id":"42"},"phases":[]}\n'
`)
			}
			if err := test.read(NewClient()); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(node, "-e", legacyFakeMail, scriptPath).CombinedOutput()
			if err != nil {
				t.Fatalf("evaluate production script: %v\n%s", err, out)
			}
			var result struct {
				Calls   []string                   `json:"calls"`
				Message map[string]json.RawMessage `json:"message"`
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatalf("decode script output: %v\n%s", err, out)
			}
			if !reflect.DeepEqual(result.Calls, test.calls) {
				t.Fatalf("message getters = %v, want %v", result.Calls, test.calls)
			}
			for key, want := range map[string]string{"id": `"42"`, "subject": `"Weekly note"`, "sender": `"sender@example.com"`, "mailbox": `"INBOX"`, "account": `"Work"`} {
				if string(result.Message[key]) != want {
					t.Fatalf("%s = %s, want %s", key, result.Message[key], want)
				}
			}
			if _, ok := result.Message["content"]; ok != test.content {
				t.Fatalf("content present = %v, want %v", ok, test.content)
			}
			if _, ok := result.Message["rfcMessageId"]; ok != test.rfc {
				t.Fatalf("RFC ID present = %v, want %v", ok, test.rfc)
			}
			var size int
			if err := json.Unmarshal(result.Message["messageSize"], &size); err != nil || size != test.size {
				t.Fatalf("size = %d, %v, want %d", size, err, test.size)
			}
		})
	}
}

const legacyFakeMail = `
const fs = require('fs');
const calls = [];
const values = {id:42, subject:'Weekly note', sender:'sender@example.com', dateReceived:new Date('2026-01-02T00:00:00Z'), dateSent:new Date('2026-01-01T00:00:00Z'), readStatus:false, flaggedStatus:true, messageSize:7, content:'body', deletedStatus:false, messageId:'<weekly@example.com>', toRecipients:[], ccRecipients:[], bccRecipients:[]};
const msg = {};
for (const [key,value] of Object.entries(values)) msg[key] = () => { calls.push(key); return value; };
const messages = () => [msg];
messages.byId = () => msg;
messages.id = () => [42];
messages.at = () => msg;
messages.whose = () => () => [msg];
const mbox = {name:()=>'INBOX', messages};
const acc = {name:()=>'Work', inbox:()=>mbox};
const Application = () => ({accounts:{byName:()=>acc}});
const ObjC = {import:()=>{}};
const reply = () => {};
const raw = JSON.parse(eval(fs.readFileSync(process.argv[1], 'utf8')));
const message = Array.isArray(raw) ? raw[0] : (raw.message || raw);
process.stdout.write(JSON.stringify({calls,message}));
`
