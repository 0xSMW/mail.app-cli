package mail

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Run the generated production JXA, including scoped membership and error
// serialization, in a plain-JavaScript Mail fixture at the osascript boundary.
func recipientLegacyClient(t *testing.T, scenario string) (*Client, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	dir := t.TempDir()
	mock, log := filepath.Join(dir, "mail.js"), filepath.Join(dir, "calls.jsonl")
	if err := os.WriteFile(mock, []byte(recipientLegacyMailJS), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(dir, "lock"))
	t.Setenv("RECIPIENT_SCENARIO", scenario)
	t.Setenv("RECIPIENT_CALLS", log)
	t.Setenv("RECIPIENT_SCRIPT", filepath.Join(dir, "production.js"))
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	writeFakeOsaScript(t, `for arg in "$@"; do script="$arg"; done
printf '%s' "$script" > "$RECIPIENT_SCRIPT"
exec `+quote(node)+" "+quote(mock)+` "$@"`)
	return NewClient(), log
}

func recipientLegacyCalls(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var page []string
		if err := json.Unmarshal([]byte(line), &page); err != nil {
			t.Fatalf("getter log %q: %v", line, err)
		}
		calls = append(calls, page...)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "FORBIDDEN") {
			t.Fatalf("header-only read accessed %s", call)
		}
	}
	return calls
}

func TestRecipientLegacyMetadataPreservesEnvelopeWithoutContent(t *testing.T) {
	c, log := recipientLegacyClient(t, "normal")
	m, err := c.GetMessageMetadataJSON("Work", "INBOX", "42")
	if err != nil || m == nil {
		t.Fatalf("metadata=%+v err=%v", m, err)
	}
	if m.ID != "42" || m.Subject != "Fixture subject" || m.Sender != "Sender <sender@example.com>" || m.DateReceived != "2026-01-02T03:04:05.000Z" || m.DateSent != "2026-01-01T03:04:05.000Z" || m.Read || !m.Flagged || m.MessageSize != 123 || m.Account != "Work" || m.Mailbox != "INBOX" || m.Content != "" || m.BodyDiagnostics != nil {
		t.Fatalf("metadata lost envelope or requested body: %+v", m)
	}
	calls := recipientLegacyCalls(t, log)
	for _, call := range calls {
		if call == "allHeaders" || strings.HasSuffix(call, "Recipients") {
			t.Fatalf("metadata read recipient getter %s", call)
		}
	}
	for _, getter := range []string{"subject", "sender", "dateReceived", "dateSent", "readStatus", "flaggedStatus", "messageSize"} {
		found := false
		for _, call := range calls {
			found = found || call == getter
		}
		if !found {
			t.Fatalf("metadata omitted getter %s: %v", getter, calls)
		}
	}
}

func TestRecipientLegacyMalformedHeaderUsesTypedFallback(t *testing.T) {
	c, log := recipientLegacyClient(t, "malformed cc")
	got := c.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "42"}})[0]
	if !reflect.DeepEqual(got.ToRecipients, []string{"header-to@example.com"}) || !reflect.DeepEqual(got.CcRecipients, []string{"typed-cc@example.com"}) || !reflect.DeepEqual(got.DeliveredTo, []string{"delivered@example.com"}) || !got.RecipientStatus.complete() || got.RecipientStatus.Cc.Source != "mail" {
		t.Fatalf("header/typed evidence=%+v", got)
	}
	recipientLegacyCalls(t, log)
}

func TestRecipientLegacyPreservesGetterErrorAndOtherFields(t *testing.T) {
	c, log := recipientLegacyClient(t, "getter error")
	got := c.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "42"}})[0]
	if got.RecipientStatus.Cc.State != "unavailable" || !strings.Contains(got.RecipientStatus.Cc.Error, "fixture Cc unreadable") || len(got.CcRecipients) != 0 || !reflect.DeepEqual(got.ToRecipients, []string{"typed-to@example.com"}) || !reflect.DeepEqual(got.BccRecipients, []string{"typed-bcc@example.com"}) || got.RecipientStatus.To.State != "available" || got.RecipientStatus.Bcc.State != "available" {
		t.Fatalf("partial recipient result=%+v", got)
	}
	recipientLegacyCalls(t, log)
}

func TestRecipientLegacyBatchContinuesAfterStaleID(t *testing.T) {
	c, log := recipientLegacyClient(t, "normal")
	got := c.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "999"}, {ID: "42"}})
	if len(got) != 2 || got[0].RecipientStatus.To.State != "unavailable" || len(got[0].ToRecipients) != 0 || !reflect.DeepEqual(got[1].ToRecipients, []string{"header-to@example.com"}) || !got[1].RecipientStatus.complete() {
		t.Fatalf("row-local failure stopped batch: %+v", got)
	}
	recipientLegacyCalls(t, log)
}

func TestRecipientLegacyForeignScopeCannotReadGloballyResolvableID(t *testing.T) {
	for _, scope := range []SearchMailbox{{"Other", "INBOX"}, {"Work", "Archive"}, {"", ""}} {
		t.Run(scope.Account+"/"+scope.Mailbox, func(t *testing.T) {
			c, log := recipientLegacyClient(t, "normal")
			got := c.EnrichMessagesWithRecipients(scope.Account, scope.Mailbox, []Message{{ID: "42"}})[0]
			for _, field := range recipientFields(&got) {
				if len(*field.values) != 0 || field.status.State != "unavailable" {
					t.Fatalf("scope leak: %+v", got)
				}
			}
			if scope.Account == "" {
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("unscoped read executed Mail automation: %v", err)
				}
				return
			}
			for _, call := range recipientLegacyCalls(t, log) {
				if call == "allHeaders" || strings.HasSuffix(call, "Recipients") {
					t.Fatalf("foreign scope accessed %s", call)
				}
			}
		})
	}
}

const recipientLegacyMailJS = `
const fs=require('fs');
const calls=[];
const scenario=process.env.RECIPIENT_SCENARIO;
const values={id:42,subject:'Fixture subject',sender:'Sender <sender@example.com>',dateReceived:new Date('2026-01-02T03:04:05Z'),dateSent:new Date('2026-01-01T03:04:05Z'),readStatus:false,flaggedStatus:true,messageSize:123,deletedStatus:false,messageId:'<fixture@example.com>'};
const msg={};
for(const [key,value] of Object.entries(values)) msg[key]=()=>{calls.push(key);return value};
msg.allHeaders=()=>{calls.push('allHeaders');if(scenario.startsWith('systemic ')){const e=new Error('systemic fixture error');e.errorNumber=Number(scenario.split(' ')[1]);throw e;}if(scenario==='getter error')return '';if(scenario==='partial typed'){const e=new Error('optional headers absent');e.errorNumber=-1728;throw e;}return 'To: header-to@example.com\r\nCc: '+(scenario==='malformed cc'?'broken <':'header-cc@example.com')+'\r\nDelivered-To: delivered@example.com\r\n'};
for(const [key,address] of [['toRecipients','typed-to@example.com'],['ccRecipients','typed-cc@example.com'],['bccRecipients','typed-bcc@example.com']]) msg[key]=()=>{calls.push(key);if(key==='ccRecipients'&&scenario.startsWith('partial ')){const e=new Error('later Cc timeout');e.errorNumber=-1712;throw e;}if(key==='ccRecipients'&&scenario==='getter error') {const e=new Error('fixture Cc unreadable');e.errorNumber=-1728;throw e;}return [{address:()=>address}]};
for(const key of ['content','source','properties','delete','move','save','send']) msg[key]=()=>{calls.push('FORBIDDEN '+key);throw new Error('forbidden '+key)};
const messages=()=>[msg];
messages.byId=()=>msg; // Mail's by-ID resolution may escape a container.
messages.whose=filter=>()=>{calls.push('membership '+filter.id._equals);return filter.id._equals===42?[msg]:[]};
const mbox={name:()=> 'INBOX',messages};
const emptyMessages=()=>[]; emptyMessages.whose=()=>()=>[];emptyMessages.byId=()=>msg;
const archive={name:()=> 'Archive',messages:emptyMessages};
const boxes=()=>[mbox,archive];boxes.byName=name=>name==='INBOX'?mbox:archive;
const acc={name:()=> 'Work',inbox:()=>mbox,mailboxes:boxes};
const Application=()=>({accounts:{byName:name=>{if(name!=='Work'){const e=new Error('account missing');e.errorNumber=-1728;throw e;}return acc;}}});
const ObjC={import:()=>{}};
try {
 const script=process.argv[process.argv.length-1];
 const result=eval(script);
 fs.appendFileSync(process.env.RECIPIENT_CALLS,JSON.stringify(calls)+'\n');
 process.stdout.write(String(result));
} catch(e) {
 fs.appendFileSync(process.env.RECIPIENT_CALLS,JSON.stringify(calls)+'\n');
 process.stderr.write(String(e));process.exitCode=1;
}
`

func TestRecipientLegacySystemicFailureStopsRemainingRows(t *testing.T) {
	for _, number := range []string{"-1743", "-1712", "-609"} {
		t.Run(number, func(t *testing.T) {
			c, log := recipientLegacyClient(t, "systemic "+number)
			got := c.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "42"}, {ID: "43"}})
			for _, m := range got {
				for _, field := range recipientFields(&m) {
					if field.status.State != "unavailable" || len(*field.values) != 0 {
						t.Fatalf("systemic error left evidence: %+v", got)
					}
				}
			}
			calls := recipientLegacyCalls(t, log)
			for _, call := range calls {
				if strings.HasSuffix(call, "Recipients") {
					t.Fatalf("systemic failure continued field getter %s", call)
				}
			}
			membershipReads := 0
			for _, call := range calls {
				if strings.HasPrefix(call, "membership ") {
					membershipReads++
					if call != "membership 42" {
						t.Fatalf("systemic failure continued row %s", call)
					}
				}
			}
			if membershipReads != 1 {
				t.Fatalf("scoped recipient reads=%d, want one", membershipReads)
			}
		})
	}
}

func TestRecipientLegacyLaterSystemicFailurePreservesEarlierEvidence(t *testing.T) {
	for _, scenario := range []string{"partial headers", "partial typed"} {
		t.Run(scenario, func(t *testing.T) {
			c, log := recipientLegacyClient(t, scenario)
			got := c.EnrichMessagesWithRecipients("Work", "INBOX", []Message{{ID: "42"}, {ID: "43"}})
			first := got[0]
			if scenario == "partial headers" {
				if !first.RecipientStatus.complete() || !reflect.DeepEqual(first.ToRecipients, []string{"header-to@example.com"}) || !reflect.DeepEqual(first.CcRecipients, []string{"header-cc@example.com"}) || !reflect.DeepEqual(first.DeliveredTo, []string{"delivered@example.com"}) || len(first.BccRecipients) != 0 || len(first.OriginalTo) != 0 {
					t.Fatalf("later timeout discarded valid headers: %+v", first)
				}
			} else {
				if !reflect.DeepEqual(first.ToRecipients, []string{"typed-to@example.com"}) || first.RecipientStatus.To.State != "available" || first.RecipientStatus.To.Source != "mail" {
					t.Fatalf("later timeout discarded typed To: %+v", first)
				}
				for _, field := range recipientFields(&first)[1:] {
					if field.status.State != "unavailable" || field.status.Error == "" || len(*field.values) != 0 {
						t.Fatalf("unread fields claimed available: %+v", first)
					}
				}
			}
			for _, field := range recipientFields(&got[1]) {
				if field.status.State != "unavailable" || len(*field.values) != 0 {
					t.Fatalf("later row exposed recipients after timeout: %+v", got[1])
				}
			}
			calls := recipientLegacyCalls(t, log)
			membershipReads, ccReads := 0, 0
			for _, call := range calls {
				if strings.HasPrefix(call, "membership ") {
					membershipReads++
					if call != "membership 42" {
						t.Fatalf("timeout continued row: %s", call)
					}
				}
				if call == "ccRecipients" {
					ccReads++
				}
				if call == "bccRecipients" {
					t.Fatal("timeout continued Bcc getter")
				}
			}
			if membershipReads != 1 || ccReads != 1 {
				t.Fatalf("membership=%d Cc=%d calls=%v", membershipReads, ccReads, calls)
			}
		})
	}
}
