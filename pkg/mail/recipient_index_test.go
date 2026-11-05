package mail

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func recipientIndexFixture(t *testing.T, sql string) (*Client, *atomic.Int64) {
	t.Helper()
	c, _, events := localReadFixture(t)
	recipientIndexSQL(t, `create table recipients(ROWID integer primary key,message integer not null,address integer not null,type integer,position integer,unique(message,type,position));`+sql)
	return c, events
}

func recipientIndexSQL(t *testing.T, sql string) {
	t.Helper()
	if output, err := exec.Command("sqlite3", os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), sql).CombinedOutput(); err != nil {
		t.Fatalf("recipient fixture SQL failed: %v: %s", err, output)
	}
}

func recipientIndexPending(id, account, mailbox string) Message {
	return Message{ID: id, Account: account, Mailbox: mailbox, RecipientStatus: RecipientStatus{
		To: RecipientFieldStatus{State: "unavailable", Error: "complete To headers unavailable"},
		Cc: RecipientFieldStatus{State: "unavailable", Error: "complete Cc headers unavailable"},
	}}
}

func TestRecipientIndexAddsOnlyPositivePartialToAndCcEvidence(t *testing.T) {
	c, events := recipientIndexFixture(t, `
insert into addresses(ROWID,address,comment) values
(101,'First <first@example.com>',''),(102,'second@example.com',''),(103,'cc@example.com','');
insert into recipients(message,address,type,position) values
(101,102,0,1),(101,101,0,0),(101,103,1,0),(103,101,0,0);`)
	messages := []Message{recipientIndexPending("101", "Work", "INBOX"), recipientIndexPending("103", "Work", "INBOX")}
	if err := c.enrichRecipientIndexEvidence("", "", messages); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages[0].ToRecipients, []string{"first@example.com", "second@example.com"}) || !reflect.DeepEqual(messages[0].CcRecipients, []string{"cc@example.com"}) || !reflect.DeepEqual(messages[1].ToRecipients, []string{"first@example.com"}) {
		t.Fatal("indexed recipient observations differ from the selected rows")
	}
	for _, status := range []RecipientFieldStatus{messages[0].RecipientStatus.To, messages[0].RecipientStatus.Cc, messages[1].RecipientStatus.To} {
		if status.State != "partial" || status.Source != "index" || !strings.Contains(status.Error, recipientIndexIncomplete) {
			t.Fatalf("index observation incorrectly certifies completeness: %+v", status)
		}
	}
	if messages[1].RecipientStatus.Cc.State != "unavailable" || messages[1].RecipientStatus.Cc.Error != "complete Cc headers unavailable" || messages[0].RecipientStatus.Bcc.State != "" {
		t.Fatal("missing or unverified index fields were treated as observed")
	}
	if events.Load() != 0 {
		t.Fatalf("index-only enrichment sent %d Mail events", events.Load())
	}
}

func TestRecipientIndexIgnoresAbsentAndInvalidRows(t *testing.T) {
	c, events := recipientIndexFixture(t, `
insert into addresses(ROWID,address,comment) values
(101,'valid@example.com',''),(102,'invalid',''),(103,'one@example.com, two@example.com',''),(104,NULL,''),(105,123,'');
insert into recipients(message,address,type,position) values
(101,101,2,0),(101,101,7,0),(101,101,NULL,0),(101,101,'invalid',0),
(101,101,0,NULL),(101,101,0,-1),(101,101,0,'invalid'),
(101,102,0,1),(101,103,0,2),(101,104,0,3),(101,999,0,4);`)
	messages := []Message{recipientIndexPending("101", "Work", "INBOX"), recipientIndexPending("103", "Work", "INBOX")}
	want := append([]Message(nil), messages...)
	if err := c.enrichRecipientIndexEvidence("", "", messages); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages, want) {
		t.Fatal("invalid or absent rows changed recipient evidence")
	}
	if events.Load() != 0 {
		t.Fatal("invalid index rows triggered Mail reads")
	}
}

func TestRecipientIndexChecksMembershipAndPhysicalAccountOwnership(t *testing.T) {
	c, events := recipientIndexFixture(t, `
insert into addresses(ROWID,address,comment) values (101,'indexed@example.com','');
insert into recipients(message,address,type,position) values (101,101,0,0),(102,101,0,0),(105,101,0,0);
update messages set deleted=1 where ROWID=105;`)
	for _, test := range []struct {
		name, account, mailbox, id string
		want                       bool
	}{
		{"scoped label", "Work", "INBOX", "101", true},
		{"foreign owner under label", "Work", "INBOX", "102", false},
		{"wrong mailbox", "Work", "邮件 café", "101", false},
		{"deleted row", "Work", "INBOX", "105", false},
		{"own account", "Other", "INBOX", "102", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			messages := []Message{recipientIndexPending(test.id, test.account, test.mailbox)}
			if err := c.enrichRecipientIndexEvidence("", "", messages); err != nil {
				t.Fatal(err)
			}
			if (len(messages[0].ToRecipients) > 0) != test.want {
				t.Fatal("indexed recipient evidence escaped requested ownership or membership")
			}
		})
	}
	messages := []Message{recipientIndexPending("101", "Other", "邮件 café")}
	if err := c.enrichRecipientIndexEvidence("Work", "INBOX", messages); err != nil || len(messages[0].ToRecipients) != 1 {
		t.Fatalf("explicit scope overrides were not respected: %v", err)
	}
	if events.Load() != 0 {
		t.Fatal("scoped index enrichment requested Mail data")
	}
}

func TestRecipientIndexPreservesCompleteAndMergesPartialEvidence(t *testing.T) {
	c, _ := recipientIndexFixture(t, `
insert into addresses(ROWID,address,comment) values (101,'new@example.com',''),(102,'saved@example.com','');
insert into recipients(message,address,type,position) values (101,101,0,0),(101,102,0,1),(101,101,1,0);`)
	m := recipientIndexPending("101", "Work", "INBOX")
	m.ToRecipients = []string{"saved@example.com"}
	m.RecipientStatus.To = RecipientFieldStatus{State: "partial", Source: "disk", Error: "malformed repetition"}
	m.CcRecipients = []string{"complete@example.com"}
	m.RecipientStatus.Cc = RecipientFieldStatus{State: "available", Source: "disk"}
	messages := []Message{m}
	if err := c.enrichRecipientIndexEvidence("", "", messages); err != nil {
		t.Fatal(err)
	}
	got := messages[0]
	if !reflect.DeepEqual(got.ToRecipients, []string{"saved@example.com", "new@example.com"}) || got.RecipientStatus.To.State != "partial" || got.RecipientStatus.To.Source != "mixed" || got.RecipientStatus.To.Error != "malformed repetition; "+recipientIndexIncomplete {
		t.Fatal("indexed evidence failed to union with earlier partial evidence")
	}
	if !reflect.DeepEqual(got.CcRecipients, m.CcRecipients) || got.RecipientStatus.Cc != m.RecipientStatus.Cc {
		t.Fatal("index replaced stronger complete disk evidence")
	}
}

func TestRecipientIndexEnrichesFreshZeroAndNotRequestedFields(t *testing.T) {
	c, events := recipientIndexFixture(t, `
insert into addresses(ROWID,address,comment) values (101,'indexed@example.com','');
insert into recipients(message,address,type,position) values (101,101,0,0),(101,101,1,0);`)
	for _, state := range []string{"", "not-requested"} {
		t.Run(state, func(t *testing.T) {
			messages := []Message{{ID: "101", Account: "Work", Mailbox: "INBOX", RecipientStatus: RecipientStatus{
				To: RecipientFieldStatus{State: state}, Cc: RecipientFieldStatus{State: state},
			}}}
			if err := c.enrichRecipientIndexEvidence("", "", messages); err != nil {
				t.Fatal(err)
			}
			for _, field := range recipientFields(&messages[0])[:2] {
				if !reflect.DeepEqual(*field.values, []string{"indexed@example.com"}) || field.status.State != "partial" || field.status.Source != "index" || field.status.Error != recipientIndexIncomplete {
					t.Fatal("fresh fields did not receive partial indexed evidence")
				}
			}
			if err := c.enrichRecipientIndexEvidence("", "", messages); err != nil {
				t.Fatal(err)
			}
			if strings.Count(messages[0].RecipientStatus.To.Error, recipientIndexIncomplete) != 1 || len(messages[0].ToRecipients) != 1 {
				t.Fatal("repeated indexed observation duplicated addresses or diagnostic")
			}
		})
	}
	if events.Load() != 0 {
		t.Fatal("fresh field enrichment requested Mail data")
	}
}

func TestRecipientIndexFailureDoesNotApplyStagedEvidence(t *testing.T) {
	c, _ := recipientIndexFixture(t, `
insert into addresses(ROWID,address,comment) values (101,'indexed@example.com','');
insert into recipients(message,address,type,position) values (101,101,0,0);`)
	messages := []Message{recipientIndexPending("101", "Work", "INBOX"), recipientIndexPending("103", "Missing", "INBOX")}
	want := append([]Message(nil), messages...)
	if err := c.enrichRecipientIndexEvidence("", "", messages); err == nil {
		t.Fatal("unresolvable later scope unexpectedly succeeded")
	}
	if !reflect.DeepEqual(messages, want) {
		t.Fatal("a later scope failure applied earlier staged evidence")
	}
}

func TestRecipientIndexMissingSchemaLeavesEvidenceUnchanged(t *testing.T) {
	c, _, events := localReadFixture(t)
	messages := []Message{recipientIndexPending("101", "Work", "INBOX")}
	want := append([]Message(nil), messages...)
	if err := c.enrichRecipientIndexEvidence("", "", messages); err == nil {
		t.Fatal("missing optional recipient table did not return a fallback error")
	}
	if !reflect.DeepEqual(messages, want) || events.Load() != 0 {
		t.Fatal("optional schema failure changed evidence or queried Mail")
	}
}
