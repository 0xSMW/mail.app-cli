package mail

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func recipientIndexIntegrationFixture(t *testing.T, sql string) (*Client, string, *atomic.Int64) {
	t.Helper()
	client, root, events := localReadFixture(t)
	query := `create table recipients(message integer,address integer,type integer,position integer);
insert into addresses(ROWID,address,comment) values(201,'target@example.test',''),(202,'other@example.test',''),(203,'me','');
update messages set deleted=1 where ROWID<>105;` + sql
	if output, err := exec.Command("sqlite3", os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH"), query).CombinedOutput(); err != nil {
		t.Fatalf("integration fixture SQL: %v %s", err, output)
	}
	return client, root, events
}

func assertRecipientIndexIntegrationMatch(t *testing.T, message Message) {
	t.Helper()
	if message.ID != "105" || !reflect.DeepEqual(message.ToRecipients, []string{"target@example.test"}) || message.RecipientStatus.To.State != "partial" || message.RecipientStatus.To.Source != "index" || message.RecipientStatus.complete() {
		t.Fatalf("indexed match=%+v", message)
	}
	if matched, known := MatchRecipient(message, "target@example.test"); !matched || !known {
		t.Fatalf("indexed positive match=%t known=%t", matched, known)
	}
	if matched, known := MatchRecipient(message, "absent@example.test"); matched || known {
		t.Fatalf("indexed negative match=%t known=%t", matched, known)
	}
	if message.Content != "" || message.ContentSource != "" || message.BodyDiagnostics != nil {
		t.Fatal("recipient index retrieval populated body content")
	}
}

func TestRecipientIndexIntegrationExactQueriesSkipMailForKnownPositive(t *testing.T) {
	for _, route := range []string{"list", "search"} {
		t.Run(route, func(t *testing.T) {
			client, _, events := recipientIndexIntegrationFixture(t, `insert into recipients values(105,201,0,0);`)
			request := RecipientQueryRequest{Recipient: "target@example.test", Scopes: []SearchMailbox{{"Work", "INBOX"}}, Query: "indexed subject"}
			var result SearchResult
			var err error
			if route == "list" {
				result, err = client.RecipientMessages(request)
			} else {
				result, err = client.SearchRecipientMessages("Work", "INBOX", request)
			}
			if err != nil || !result.Complete || len(result.Messages) != 1 || len(result.FailedMailboxes) != 0 {
				t.Fatalf("%s result=%+v err=%v", route, result, err)
			}
			assertRecipientIndexIntegrationMatch(t, result.Messages[0])
			if events.Load() != 0 {
				t.Fatalf("known index match requested %d Mail events", events.Load())
			}
		})
	}
}

func TestRecipientIndexIntegrationTargetedEnrichmentSkipsMail(t *testing.T) {
	client, _, events := recipientIndexIntegrationFixture(t, `insert into recipients values(105,201,0,0);`)
	got := client.enrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "105"}}, "target@example.test")[0]
	assertRecipientIndexIntegrationMatch(t, got)
	if events.Load() != 0 {
		t.Fatalf("known target enrichment requested %d Mail events", events.Load())
	}
}

func TestRecipientIndexIntegrationExactScanSeparatesMatchAndFieldCoverage(t *testing.T) {
	client, _, events := recipientIndexIntegrationFixture(t, `insert into recipients values(105,201,0,0);`)
	result, err := client.ScanMessages(ScanRequest{Scopes: []SearchMailbox{{"Work", "INBOX"}}, Recipient: "target@example.test", Query: "indexed subject"})
	if err != nil || !result.Complete || len(result.Messages) != 1 {
		t.Fatalf("scan result=%+v err=%v", result, err)
	}
	assertRecipientIndexIntegrationMatch(t, result.Messages[0].Message)
	if result.RecipientCoverage == nil || *result.RecipientCoverage != (RecipientCoverage{Inspected: 1, Unavailable: 1, Complete: false}) {
		t.Fatalf("scan recipient coverage=%+v", result.RecipientCoverage)
	}
	if events.Load() != 0 {
		t.Fatalf("known scan match requested %d Mail events", events.Load())
	}
}

func TestRecipientIndexIntegrationUnknownTargetFallsBackToMail(t *testing.T) {
	for name, sql := range map[string]string{
		"different address": `insert into recipients values(105,202,0,0);`,
		"missing row":       "",
		"invalid address":   `insert into recipients values(105,203,0,0);`,
	} {
		t.Run(name, func(t *testing.T) {
			client, _, events := recipientIndexIntegrationFixture(t, sql)
			got := client.enrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "105"}}, "target@example.test")[0]
			if matched, known := MatchRecipient(got, "target@example.test"); matched || known {
				t.Fatalf("incomplete index supplied negative proof: match=%t known=%t", matched, known)
			}
			if events.Load() == 0 {
				t.Fatal("unknown target did not attempt Mail fallback")
			}
		})
	}
}

func TestRecipientIndexIntegrationPublicEnrichmentStillCompletesPartialEvidence(t *testing.T) {
	client, _, events := recipientIndexIntegrationFixture(t, `insert into recipients values(105,201,0,0);`)
	got := client.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "105"}})[0]
	assertRecipientIndexIntegrationMatch(t, got)
	if events.Load() == 0 {
		t.Fatal("public recipient enrichment skipped Mail for incomplete fields")
	}
}

func TestRecipientIndexIntegrationCompleteEmptyDiskFieldRejectsContradiction(t *testing.T) {
	client, root, events := recipientIndexIntegrationFixture(t, `insert into recipients values(105,201,0,0);`)
	path := filepath.Join(root, "ABC", "[Gmail].mbox", "All Mail.mbox", "UUID", "Data", "Messages", "105.emlx")
	writeEMLX(t, path, "From: sender@example.test\nSubject: cached complete empty recipients\n\nbody\n")
	got := client.enrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "105"}}, "target@example.test")[0]
	if len(got.ToRecipients) != 0 || got.RecipientStatus.To.State != "available" || got.RecipientStatus.To.Source != "disk" || !got.RecipientStatus.complete() {
		t.Fatalf("complete cached To contradicted by index: %+v", got)
	}
	if matched, known := MatchRecipient(got, "target@example.test"); matched || !known {
		t.Fatalf("complete empty To match=%t known=%t", matched, known)
	}
	if events.Load() != 0 {
		t.Fatalf("complete cached recipients requested %d Mail events", events.Load())
	}
}

func TestRecipientIndexIntegrationCompleteMailHeadersReplacePartialIndex(t *testing.T) {
	for _, route := range []string{"public", "targeted nonmatching index"} {
		t.Run(route, func(t *testing.T) {
			client, _, _ := recipientIndexIntegrationFixture(t, `insert into recipients values(105,201,0,0);`)
			fixture := localReadLiveFixture(client)
			addRecipientFixtureMessage(fixture, 105, "mbxp:inbox")
			fixture.properties["mssg:105"]["alhe"] = ae.String("To: fresh@example.test\r\n\r\n")
			var got Message
			if route == "public" {
				got = client.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "105"}})[0]
			} else {
				got = client.enrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "105"}}, "fresh@example.test")[0]
			}
			if !reflect.DeepEqual(got.ToRecipients, []string{"fresh@example.test"}) || got.RecipientStatus.To.State != "available" || got.RecipientStatus.To.Source != "mail" || !got.RecipientStatus.complete() {
				t.Fatalf("complete Mail headers did not replace partial index: %+v", got)
			}
			if matched, known := MatchRecipient(got, "target@example.test"); matched || !known {
				t.Fatalf("stale indexed target match=%t known=%t", matched, known)
			}
			if matched, known := MatchRecipient(got, "fresh@example.test"); !matched || !known {
				t.Fatalf("fresh Mail target match=%t known=%t", matched, known)
			}
			membership, headers := -1, -1
			for i, event := range fixture.events {
				value := event.Params["----"]
				if value.Fields["seld"].Code == "ID  " && value.Fields["from"].Fields["form"].Code == "test" && value.Fields["from"].Fields["want"].Code == "mssg" {
					membership = i
				}
				if value.Fields["seld"].Code == "alhe" {
					headers = i
				}
			}
			if membership < 0 || headers <= membership {
				t.Fatalf("Mail headers were not read after scoped membership: membership=%d headers=%d", membership, headers)
			}
			assertRecipientReadEvents(t, fixture)
		})
	}
}
