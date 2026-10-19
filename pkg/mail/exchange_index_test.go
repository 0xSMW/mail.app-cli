package mail

import (
	"fmt"
	"testing"
)

func TestIndexMailboxSchemesAndSnapshotParity(t *testing.T) {
	for _, scheme := range []string{"ews", "imap"} {
		for _, tc := range []struct {
			name, mailbox, rows string
			want                int
		}{
			{"Inbox root queried as INBOX", "INBOX", "(1,'%s://abc/INBOX'),(2,'%s://ABC/Inbox')", 2},
			{"INBOX root queried as Inbox", "Inbox", "(1,'%s://abc/Inbox'),(2,'%s://ABC/INBOX')", 2},
			{"exact before nested", "INBOX", "(1,'%s://ABC/Parent/INBOX'),(2,'%s://ABC/INBOX')", 2},
			{"nested", "Projects", "(1,'%s://ABC/Parent/Projects')", 1},
			{"localized", "受信トレイ", "(1,'%s://ABC/受信トレイ')", 1},
			{"Gmail alias", "Archive", "(1,'%s://ABC/Archive'),(2,'%s://ABC/%%5BGmail%%5D/All%%20Mail')", 2},
			{"literal Archive before broad fallback", "Archive", "(1,'%s://ABC/Other/All%%20Mail'),(2,'%s://ABC/Archive')", 2},
			{"nested Archive before broad fallback", "Archive", "(1,'%s://ABC/Other/All%%20Mail'),(2,'%s://ABC/Parent/Archive')", 2},
			{"nested All Mail fallback", "All Mail", "(1,'%s://ABC/Parent/All%%20Mail')", 1},
			{"space percent is literal", "Team Box", "(1,'%s://ABC/Parent/TeamX20Box'),(2,'%s://ABC/Parent/Team%%20Box')", 2},
			{"underscore is literal", "A_B", "(1,'%s://ABC/Parent/AxB'),(2,'%s://ABC/Parent/A_B')", 2},
			{"backslash is literal", `A\B`, "(1,'%s://ABC/Parent/AB'),(2,'%s://ABC/Parent/A\\B')", 2},
			{"same name other account", "INBOX", "(1,'%s://DEF/INBOX'),(2,'%s://ABCD/INBOX'),(3,'%s://ABC/INBOX')", 3},
		} {
			t.Run(scheme+"/"+tc.name, func(t *testing.T) {
				// Each row has one scheme placeholder; escaped URL percents survive Sprintf.
				var schemes []any
				for _, r := range tc.rows {
					if r == '(' {
						schemes = append(schemes, scheme)
					}
				}
				rows := fmt.Sprintf(tc.rows, schemes...)
				data := "insert into mailboxes(rowid,url) values " + rows + ";"
				data += fmt.Sprintf("insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (101,%d,1,1,1787961600,42,1,0);", tc.want)
				client, _ := snapshotSQLiteFixture(t, data)
				mailbox, ok, err := client.resolveIndexMailbox("Work", tc.mailbox)
				if err != nil || !ok || mailbox.ID != tc.want {
					t.Fatalf("resolved %+v, %v, %v; want %d", mailbox, ok, err, tc.want)
				}
				scope := snapshotScope("Work", tc.mailbox)
				got := snapshotMessages(t, client.getIdentitySnapshotForVerification([]identityVerificationScope{scope}), scope)
				if len(got) != 1 || got[0].ID != "101" {
					t.Fatalf("snapshot disagrees with resolver: %+v", got)
				}
			})
		}
	}
}

func TestIndexMailboxAccountBoundariesAndMixedSchemes(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `
insert into mailboxes(rowid,url) values
 (1,'ews://A_%/INBOX'), (2,'imap://A_%/Parent/Projects'),
 (3,'ews://Axx/INBOX'), (4,'ews://A_%more/INBOX'), (5,'ews://a_%/INBOX'),
 (6,'ews://Other/ews://A_%/INBOX'), (7,'ews://A_%/Archive');`)
	account := Account{ID: "A_%", Name: "Wild"}
	client = NewClient()
	client.PrimeAccounts([]Account{account})
	boxes, ok, err := client.getMailboxesFromIndex(account)
	if err != nil || !ok || len(boxes) != 3 {
		t.Fatalf("account mailbox inventory: %+v, %v, %v", boxes, ok, err)
	}
	for _, tc := range []struct {
		name string
		id   int
	}{{"INBOX", 1}, {"Projects", 2}, {"Archive", 7}} {
		box, ok, err := client.resolveIndexMailbox("Wild", tc.name)
		if err != nil || !ok || box.ID != tc.id {
			t.Fatalf("%s mixed schemes: %+v, %v, %v", tc.name, box, ok, err)
		}
		var snapshot []indexMailbox
		if err := client.runEnvelopeIndexQuery(verificationMailboxSelect(account.ID, tc.name), &snapshot); err != nil || len(snapshot) != 1 || snapshot[0].ID != tc.id {
			t.Fatalf("snapshot selector %s: %+v, %v", tc.name, snapshot, err)
		}
	}
}

func TestIndexMailboxCustomRootCaseRemainsExact(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `insert into mailboxes(rowid,url) values (1,'ews://ABC/projects');`)
	box, ok, err := client.resolveIndexMailbox("Work", "Projects")
	if err != nil || ok || box != nil {
		t.Fatalf("custom root case mismatch must remain absent: %+v, %v, %v", box, ok, err)
	}
	var snapshot []indexMailbox
	if err := client.runEnvelopeIndexQuery(verificationMailboxSelect("ABC", "Projects"), &snapshot); err != nil || len(snapshot) != 0 {
		t.Fatalf("snapshot custom root case mismatch: %+v, %v", snapshot, err)
	}
}

func TestIndexMailboxInboxAliasPreservesListedNameAndCounts(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `insert into mailboxes(rowid,url) values (1,'ews://ABC/Inbox');`)
	mailboxes, ok, err := client.getMailboxesFromIndex(Account{ID: "ABC", Name: "Work"})
	if err != nil || !ok || len(mailboxes) != 1 {
		t.Fatalf("list Inbox: %+v, %v, %v", mailboxes, ok, err)
	}
	if mailbox := mailboxes[0]; mailbox.Name != "Inbox" || mailbox.TotalCount != 0 || mailbox.UnreadCount != 0 {
		t.Fatalf("Inbox alias must preserve indexed display name and counts: %+v", mailbox)
	}
}
