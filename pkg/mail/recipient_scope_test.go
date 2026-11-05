package mail

import (
	"reflect"
	"testing"
)

func TestRecipientDiskBatchKeepsDuplicateIDsWithinEachScope(t *testing.T) {
	for _, test := range []struct {
		name, account, mailbox string
		rows                   []Message
		wantAvailable          []bool
	}{
		{"foreign account", "", "", []Message{{ID: "101", Account: "Work", Mailbox: "INBOX"}, {ID: "101", Account: "Other", Mailbox: "INBOX"}}, []bool{true, false}},
		{"foreign mailbox", "", "", []Message{{ID: "101", Account: "Work", Mailbox: "INBOX"}, {ID: "101", Account: "Work", Mailbox: "邮件 café"}}, []bool{true, false}},
		{"valid labels", "", "", []Message{{ID: "101", Account: "Work", Mailbox: "INBOX"}, {ID: "101", Account: "Work", Mailbox: "All Mail"}}, []bool{true, true}},
		{"explicit scope overrides rows", "Work", "INBOX", []Message{{ID: "101", Account: "Other", Mailbox: "INBOX"}, {ID: "101", Account: "Work", Mailbox: "邮件 café"}}, []bool{true, true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, _, events := localReadFixture(t)
			got := client.EnrichMessagesWithRecipients(test.account, test.mailbox, test.rows)
			for i, wantAvailable := range test.wantAvailable {
				if wantAvailable {
					if !got[i].RecipientStatus.complete() || got[i].RecipientStatus.To.Source != "disk" || !reflect.DeepEqual(got[i].ToRecipients, []string{"li@example.com", "cafe@example.com", "second@example.com"}) {
						t.Fatalf("valid row %d lost scoped cached evidence: %+v", i, got[i])
					}
					continue
				}
				for _, field := range recipientFields(&got[i]) {
					if len(*field.values) != 0 || field.status.State != "unavailable" || field.status.Error == "" {
						t.Fatalf("out-of-scope row %d acquired %s evidence from another row: %+v", i, field.name, got[i])
					}
				}
			}
			if test.wantAvailable[1] && events.Load() != 0 {
				t.Fatalf("fully cached scoped rows sent %d Mail events", events.Load())
			}
		})
	}
}

func TestRecipientIndexBatchKeepsDuplicateIDsWithinEachScope(t *testing.T) {
	for _, scope := range []SearchMailbox{{"Other", "INBOX"}, {"Work", "邮件 café"}} {
		t.Run(scope.Account+"/"+scope.Mailbox, func(t *testing.T) {
			client, _ := recipientIndexFixture(t, `
insert into addresses(ROWID,address,comment) values (201,'indexed@example.test','');
insert into recipients(message,address,type,position) values (105,201,0,0);`)
			got := client.enrichMessagesWithRecipients("", "", []Message{{ID: "105", Account: "Work", Mailbox: "INBOX"}, {ID: "105", Account: scope.Account, Mailbox: scope.Mailbox}}, "indexed@example.test")
			if !reflect.DeepEqual(got[0].ToRecipients, []string{"indexed@example.test"}) || got[0].RecipientStatus.To.State != "partial" || got[0].RecipientStatus.To.Source != "index" {
				t.Fatalf("valid row lost scoped index evidence: %+v", got[0])
			}
			for _, field := range recipientFields(&got[1]) {
				if len(*field.values) != 0 || field.status.State != "unavailable" {
					t.Fatalf("out-of-scope row acquired %s index evidence from another row: %+v", field.name, got[1])
				}
			}
		})
	}
}
