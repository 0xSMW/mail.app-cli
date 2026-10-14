package mail

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"
)

func TestDraftSnapshotUsesSpecialMailboxObjects(t *testing.T) {
	for _, name := range []string{"Drafts", "Bozze", "Entwürfe", "下書き"} {
		t.Run(name, func(t *testing.T) {
			input := OutgoingInput{Account: "Account", Sender: "sender@example.com", Subject: "Subject", Body: "Body", To: []string{"to@example.com"}}
			config := map[string]any{"snapshotRead": true, "draftName": name, "physicalDraftName": "[Gmail]/" + name, "nestedDrafts": true, "source": "raw source"}
			payload, _ := json.Marshal(map[string]any{"script": jxaDraftSnapshotScript("Account", "42"), "input": input, "config": config})
			command := exec.Command("node", "testdata/outgoing_mock.js")
			command.Stdin = bytes.NewReader(payload)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, output)
			}
			var got struct{ Response draftSnapshot }
			if err = json.Unmarshal(output, &got); err != nil {
				t.Fatal(err)
			}
			if got.Response.Message == nil || got.Response.Message.ID != "42" || got.Response.Message.Mailbox != "[Gmail]/"+name || got.Response.Source != "raw source" {
				t.Fatalf("unexpected snapshot: %s", output)
			}
		})
	}
}
