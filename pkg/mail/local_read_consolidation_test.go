package mail

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/mailindex"
)

func TestLocalSingleAndBatchHydrationParity(t *testing.T) {
	c, _, events := localReadFixture(t)
	mbox, ok, err := c.resolveIndexMailbox("Work", "INBOX")
	if err != nil || !ok {
		t.Fatalf("mailbox = %#v, %v, %v", mbox, ok, err)
	}
	for _, id := range []string{"101", "103", "104", "105"} {
		t.Run(id, func(t *testing.T) {
			envelope, err := c.getMessageEnvelopeFromIndex("Work", mbox, id)
			if err != nil || envelope == nil {
				t.Fatalf("envelope = %#v, %v", envelope, err)
			}
			single, handled, err := c.getMessageDetailsFromDisk("Work", "INBOX", id)
			if err != nil {
				t.Fatal(err)
			}
			batch := []Message{*envelope}
			complete, err := c.enrichMessagesFromDisk("Work", "INBOX", batch)
			if err != nil || complete[0] != handled || !reflect.DeepEqual(single, &batch[0]) {
				t.Fatalf("single=%#v handled=%v; batch=%#v complete=%v error=%v", single, handled, batch[0], complete, err)
			}
		})
	}
	if events.Load() != 0 {
		t.Fatal("local hydration asked Mail for content")
	}
}

func TestIndexMailboxCandidatePreferenceRetainsStagedQueries(t *testing.T) {
	for _, tc := range []struct {
		name, mailbox string
		urls          []string
		want          int
		queries       int
	}{
		{"exact before nested", "INBOX", []string{"Nested/INBOX", "INBOX"}, 2, 1},
		{"Gmail before literal Archive", "Archive", []string{"Archive", "%5BGmail%5D/All%20Mail", "Nested/Archive"}, 2, 1},
		{"literal before nested Archive and All Mail", "Archive", []string{"Nested/Archive", "Unrelated/All%20Mail", "Archive"}, 3, 2},
		{"nested Archive before nested All Mail", "Archive", []string{"Unrelated/All%20Mail", "Nested/Archive"}, 2, 2},
		{"Archive broad fallback", "Archive", []string{"Unrelated/All%20Mail"}, 1, 3},
		{"All Mail does not use literal Archive", "All Mail", []string{"Archive", "Unrelated/All%20Mail"}, 2, 2},
		{"encoded nested leaf", "邮件 café", []string{"Parent/%E9%82%AE%E4%BB%B6%20caf%C3%A9"}, 1, 2},
		{"nested row ordering", "INBOX", []string{"Second/INBOX", "First/INBOX"}, 1, 2},
		{"missing Archive", "Archive", []string{"Other"}, 0, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data strings.Builder
			for i, url := range tc.urls {
				fmt.Fprintf(&data, "insert into mailboxes(rowid,url) values (%d,%s);\n", i+1, sqlQuote("imap://ABC/"+url))
			}
			client, log := snapshotSQLiteFixture(t, data.String())
			setup := "alter table mailboxes add column total_count integer; alter table mailboxes add column unread_count integer; update mailboxes set total_count=rowid+10, unread_count=rowid;"
			if output, err := exec.Command(os.Getenv("SNAPSHOT_SQLITE_BINARY"), os.Getenv("SNAPSHOT_SQLITE_DATABASE"), setup).CombinedOutput(); err != nil {
				t.Fatalf("fixture counts: %v: %s", err, output)
			}
			mbox, ok, err := client.resolveIndexMailbox("Work", tc.mailbox)
			if err != nil || ok != (tc.want != 0) {
				t.Fatalf("resolve = %#v, %v, %v", mbox, ok, err)
			}
			if ok && (mbox.ID != tc.want || mbox.URL != "imap://ABC/"+tc.urls[tc.want-1] || mbox.TotalCount != tc.want+10 || mbox.UnreadCount != tc.want) {
				t.Fatalf("candidate/projection = %#v", mbox)
			}
			queries, err := os.ReadFile(log)
			if err != nil || strings.Count(string(queries), ";") != tc.queries {
				t.Fatalf("query ladder = %s, %v; want %d statements", queries, err, tc.queries)
			}
			var snapshotRows []struct{ ID int }
			if err := client.runEnvelopeIndexQuery(verificationMailboxSelect("ABC", tc.mailbox)+";", &snapshotRows); err != nil {
				t.Fatal(err)
			}
			if tc.want == 0 && len(snapshotRows) != 0 || tc.want != 0 && (len(snapshotRows) != 1 || snapshotRows[0].ID != tc.want) {
				t.Fatalf("snapshot candidate = %#v; want %d", snapshotRows, tc.want)
			}
		})
	}
}

func TestLocalMIMETransferPoliciesRemainDistinct(t *testing.T) {
	c, root, events := localReadFixture(t)
	writeEMLX(t, attachmentFixturePath(root), "Content-Type: text/plain\nContent-Transfer-Encoding: x-custom\n\nstill readable")
	message, handled, err := c.getMessageDetailsFromDisk("Work", "INBOX", "101")
	if err != nil || !handled || message == nil || message.Content != "still readable" {
		t.Fatalf("body must retain permissive decoding: %#v, %v, %v", message, handled, err)
	}
	attachments, handled, err := c.getAttachmentsFromDisk("Work", "INBOX", "101")
	if err != nil || handled || attachments != nil {
		t.Fatalf("attachment traversal must reject unsupported encoding: %#v, %v, %v", attachments, handled, err)
	}
	if events.Load() != 0 {
		t.Fatal("local MIME policies rendered content in Mail")
	}
}

func TestLocateMessagesSharedEnvelopeProjection(t *testing.T) {
	c, _, events := localReadFixture(t)
	path := os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH")
	setup := "insert into messages(ROWID,subject,sender,summary,mailbox,date_received,date_sent,read,flagged,deleted,size) values (999,999,999,1,2,1690000000,1690000000,1,0,0,42);"
	if output, err := exec.Command("sqlite3", path, setup).CombinedOutput(); err != nil {
		t.Fatalf("fixture missing envelope joins: %v: %s", err, output)
	}
	before := mailindex.Statistics()
	locations, err := c.LocateMessages([]string{"101", "999", "777"})
	after := mailindex.Statistics()
	if err != nil || len(locations) != 2 || after.Queries-before.Queries != 2 {
		t.Fatalf("locations=%#v error=%v queries=%d", locations, err, after.Queries-before.Queries)
	}
	date := time.Unix(1690000000, 0).UTC().Format(time.RFC3339)
	want := Message{ID: "101", Subject: "indexed subject", Sender: "Sender <sender@example.com>", DateSent: date, DateReceived: date, Flagged: true, MessageSize: 1234, Mailbox: "INBOX", Account: "Work"}
	if got := locations["101"]; !reflect.DeepEqual(got.Envelope, want) || got.ID != want.ID || got.BackingMailbox != "All Mail" || got.ArchiveMailbox != "INBOX" || !reflect.DeepEqual(got.Labels, []string{"INBOX"}) {
		t.Fatalf("located envelope = %#v; want %#v", got, want)
	}
	want = Message{ID: "999", DateSent: date, DateReceived: date, Read: true, MessageSize: 42, Mailbox: "INBOX", Account: "Work"}
	if got := locations["999"]; !reflect.DeepEqual(got.Envelope, want) || got.Labels == nil || len(got.Labels) != 0 {
		t.Fatalf("left-joined envelope = %#v; want %#v", got, want)
	}
	if events.Load() != 0 {
		t.Fatal("primed account lookup sent Mail events")
	}
}

func TestIndexMailboxCandidateFailureStopsResolution(t *testing.T) {
	// Missing count columns must fail the first candidate even when it has no
	// matching row; a failed query cannot authorize a later lookup or absence.
	client, log := snapshotSQLiteFixture(t, "insert into mailboxes values ('imap://ABC/Nested/Archive');")
	mailbox, ok, err := client.resolveIndexMailbox("Work", "Archive")
	if err == nil || isEnvelopeIndexUnavailable(err) || ok || mailbox != nil {
		t.Fatalf("failed candidate = %#v, %v, %v", mailbox, ok, err)
	}
	queries, readErr := os.ReadFile(log)
	if readErr != nil || strings.Count(string(queries), ";") != 1 {
		t.Fatalf("failed candidate advanced resolution: %s, %v", queries, readErr)
	}
}
