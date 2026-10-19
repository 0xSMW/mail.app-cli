package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalReadProviderSchemesUseCachedMessageAndAttachments(t *testing.T) {
	for _, scheme := range []string{"ews", "imap", "exchanges"} {
		t.Run(scheme, func(t *testing.T) {
			client, root, events := localReadFixture(t)
			database := os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH")
			setup := fmt.Sprintf("update mailboxes set url=%s || substr(url,8);", sqlQuote(scheme+"://"))
			// These owners are deliberately mislabeled into Work's INBOX. Membership
			// alone must never authorize reading a different physical account's file.
			setup += `insert into mailboxes(rowid,url,total_count,unread_count) values
    (5,'ews://ABCD/INBOX',1,0),(6,'ews://abc/INBOX',1,0),(7,'ews://OTHER/ews://ABC/INBOX',1,0);
   insert into messages(ROWID,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values
    (107,1,1,1,5,1690000000,1690000000,0,0,0,12),
    (108,1,1,1,6,1690000000,1690000000,0,0,0,12),
    (109,1,1,1,7,1690000000,1690000000,0,0,0,12);
   insert into labels values (107,2),(108,2),(109,2);`
			if out, err := exec.Command("sqlite3", database, setup).CombinedOutput(); err != nil {
				t.Fatalf("configure provider fixture: %v: %s", err, out)
			}
			payload := []byte{0, 1, 2, 3, 255, 10}
			headers := "Message-ID: <ews-cached@example.com>\nIn-Reply-To: <parent@example.com>\nReferences: <root@example.com> <parent@example.com>\nSubject: Cached Exchange subject\nTo: primary@example.com\nTo: second@example.com\nCc: copy@example.com\nBcc: hidden@example.com\n"
			mime := headers + attachmentMIME("Content-Type: application/octet-stream\nContent-Disposition: attachment; filename=\"payload.bin\"\nContent-Transfer-Encoding: base64\n\n"+base64.StdEncoding.EncodeToString(payload))
			writeEMLX(t, attachmentFixturePath(root), mime)
			// The public native show route must return the complete cached message,
			// including RFC headers and recipients, without any automation fallback.
			message, err := client.GetMessageDetailsJSON("Work", "INBOX", "101")
			if err != nil || message == nil {
				t.Fatalf("public show: %+v, %v", message, err)
			}
			if message.Content != "body" || message.ContentSource != BodySourceDisk || message.Subject != "Cached Exchange subject" || message.RFCMessageID != "<ews-cached@example.com>" || message.InReplyTo != "<parent@example.com>" || message.References != "<root@example.com> <parent@example.com>" || message.Account != "Work" || message.Mailbox != "INBOX" {
				t.Fatalf("cached body/headers: %+v", message)
			}
			if strings.Join(message.ToRecipients, ",") != "primary@example.com,second@example.com" || strings.Join(message.CcRecipients, ",") != "copy@example.com" || strings.Join(message.BccRecipients, ",") != "hidden@example.com" {
				t.Fatalf("cached recipients: %+v", message)
			}
			batch := []Message{{ID: "101", Account: "Work", Mailbox: "INBOX"}}
			complete, err := client.enrichMessagesFromDisk("Work", "INBOX", batch)
			if err != nil || !complete[0] || batch[0].Content != "body" || batch[0].RFCMessageID != message.RFCMessageID || len(batch[0].ToRecipients) != 2 {
				t.Fatalf("batch cached hydration: %+v, %v, %v", batch, complete, err)
			}
			attachments, err := client.GetAttachmentsJSON("Work", "INBOX", "101")
			if err != nil || len(attachments) != 1 || attachments[0].Name != "payload.bin" || attachments[0].FileSize != len(payload) || attachments[0].Index != 0 {
				t.Fatalf("public attachment list: %+v, %v", attachments, err)
			}
			for _, index := range []int{-1, 0} {
				destination := filepath.Join(t.TempDir(), "payload.bin")
				if err := client.SaveAttachmentByIndex("Work", "INBOX", "101", "payload.bin", index, destination); err != nil {
					t.Fatalf("public attachment save index %d: %v", index, err)
				}
				saved, err := os.ReadFile(destination)
				if err != nil || !bytes.Equal(saved, payload) {
					t.Fatalf("attachment bytes: %v, %v", saved, err)
				}
			}
			if events.Load() != 0 {
				t.Fatalf("cached provider operations sent %d Mail events", events.Load())
			}
			live := localReadLiveFixture(client)
			for _, id := range []string{"102", "107", "108", "109"} {
				got, handled, err := client.getMessageDetailsFromDisk("Work", "INBOX", id)
				if err != nil || handled || got != nil {
					t.Fatalf("wrong-account body %s: %+v, %v, %v", id, got, handled, err)
				}
				got, err = client.GetMessageDetailsJSON("Work", "INBOX", id)
				if err != nil || got != nil {
					t.Fatalf("wrong-account public show %s: %+v, %v", id, got, err)
				}
				list, handled, err := client.getAttachmentsFromDisk("Work", "INBOX", id)
				if err != nil || !handled || len(list) != 0 {
					t.Fatalf("wrong-account attachment list %s: %+v, %v, %v", id, list, handled, err)
				}
				destination := filepath.Join(t.TempDir(), "forbidden.bin")
				_, handled, err = client.saveAttachmentFromDisk("Work", "INBOX", id, "payload.bin", destination)
				if !handled || !IsNotFound(err) {
					t.Fatalf("wrong-account attachment save %s: %v, %v", id, handled, err)
				}
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatalf("wrong-account save published file: %v", err)
				}
			}
			if len(live.events) == 0 {
				t.Fatal("index misses did not recheck scoped Mail absence")
			}
		})
	}
}

func TestLocalReadOwnerAccountWildcardCharactersAreLiteral(t *testing.T) {
	client, _, _ := localReadFixture(t)
	var rows []struct{ ID int }
	query := `with owners(ID,url) as (values
  (1,'ews://A_%/Inbox'),(2,'imap://A_%/Archive'),(3,'ews://Axx/Inbox'),
  (4,'ews://A_%more/Inbox'),(5,'ews://a_%/Inbox'),(6,'ews://OTHER/ews://A_%/Inbox'))
 select ID from owners mb where ` + indexAccountCondition("A_%") + ` order by ID;`
	if err := client.runEnvelopeIndexQuery(query, &rows); err != nil || len(rows) != 2 || rows[0].ID != 1 || rows[1].ID != 2 {
		t.Fatalf("literal owner account authority: %+v, %v", rows, err)
	}
}
