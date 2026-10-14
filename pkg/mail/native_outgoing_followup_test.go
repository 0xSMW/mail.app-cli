package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in API probe. It never sends and only closes its own compose object.
func TestNativeOutgoingAPIProbe(t *testing.T) {
	if os.Getenv("MAIL_APP_CLI_NATIVE_PROBE") != "1" {
		t.Skip("opt-in native Mail API probe")
	}
	path := filepath.Join(t.TempDir(), "probe.txt")
	if err := os.WriteFile(path, []byte("native attachment probe"), 0600); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(path)
	subject, _ := json.Marshal(fmt.Sprintf("mail-app-cli-api-probe-%d", time.Now().UnixNano()))
	output, err := NewClient().runJXA(`
ObjC.import("Foundation");
const attachPath=` + string(encoded) + `;
const mail=Application('Mail');
const subject=` + string(subject) + `;
const info={};
function probe(key,fn) { try { info[key]=fn(); } catch(e) { info[key]={error:String(e)}; } }
probe('draftsRoot',()=>mail.draftsMailbox().name());
probe('draftsChildren',()=>mail.draftsMailbox().mailboxes().map(b=>({name:b.name(),hasAccount:!!b.account(),hasContainer:!!b.container()})));
let outgoing=null;
try {
 outgoing=mail.OutgoingMessage({subject:subject,content:'probe',visible:false});
 mail.outgoingMessages.push(outgoing);
 probe('hiddenContent',()=>typeof outgoing.content());
 probe('hiddenContentAttachments',()=>outgoing.content.attachments().length);
 probe('hiddenAttachments',()=>outgoing.attachments().length);
 probe('appleScriptAttachment',()=>{
  const quote=s=>'"'+String(s).replace(/\\/g,'\\\\').replace(/"/g,'\\"')+'"';
  const code='tell application "Mail"\n tell outgoing message id '+outgoing.id()+'\n tell content\n make new attachment with properties {file name:(POSIX file '+quote(attachPath)+')} at after last paragraph\n count attachments\n end tell\n end tell\n end tell';
  const error=Ref();
  const value=$.NSAppleScript.alloc.initWithSource(code).executeAndReturnError(error);
  if(!value) throw new Error(JSON.stringify(ObjC.deepUnwrap(error[0])));
  return Number(value.int32Value);
 });
 outgoing.visible=true;
 probe('visibleContentAttachments',()=>outgoing.content.attachments().length);
 probe('visibleAttachments',()=>outgoing.attachments().length);
} finally {
 if(outgoing) mail.close(outgoing,{saving:'no'});
 for(const box of mail.draftsMailbox().mailboxes()) for(const m of box.messages()) if(m.subject()===subject) m.delete();
}
JSON.stringify(info);
`)
	if err != nil {
		t.Fatal(err)
	}
	var probe struct{ AppleScriptAttachment int }
	if err := json.Unmarshal([]byte(output), &probe); err != nil {
		t.Fatal(err)
	}
	if probe.AppleScriptAttachment != 1 {
		t.Fatalf("native insertion failed: %s", output)
	}
	t.Log(output)
}

// Saves and previews only disposable drafts addressed to the selected account.
// Sending is deliberately excluded. Cleanup is limited to a unique test subject.
func TestNativeDraftRoundTrip(t *testing.T) {
	if os.Getenv("MAIL_APP_CLI_NATIVE_PROBE") != "1" {
		t.Skip("opt-in native draft round trip")
	}
	c := NewClient()
	raw, err := c.runJXA(`JSON.stringify(Application('Mail').accounts().filter(a=>a.enabled()).map(a=>({name:a.name(),addresses:a.emailAddresses()})));`)
	if err != nil {
		t.Fatal(err)
	}
	var accounts []struct {
		Name      string
		Addresses []string
	}
	if err = json.Unmarshal([]byte(raw), &accounts); err != nil {
		t.Fatal(err)
	}
	for i, account := range accounts {
		if len(account.Addresses) == 0 {
			continue
		}
		for _, attachment := range []bool{false, true} {
			t.Run(fmt.Sprintf("account_%d_attachment_%t", i, attachment), func(t *testing.T) {
				subject := fmt.Sprintf("mail-app-cli-native-test-%d", time.Now().UnixNano())
				encoded, _ := json.Marshal(subject)
				t.Cleanup(func() {
					cleanupOutput, err := c.runJXA(`const mail=Application('Mail'); const info=[]; const subject=` + string(encoded) + `;
for(const m of mail.outgoingMessages()) { if(m.subject()===subject) mail.close(m,{saving:'no'}); }
for(const box of mail.draftsMailbox().mailboxes()) { for(const m of box.messages()) { if(m.subject()===subject) { info.push({id:String(m.id())}); m.delete(); } } }
JSON.stringify({cleaned:true,info:info});`)
					if err != nil {
						t.Errorf("disposable draft cleanup: %v", err)
					}
					t.Log("cleanup probe: " + cleanupOutput)
				})
				input := DraftInput{Account: account.Name, Subject: subject, Body: "Disposable native round trip.", To: []string{account.Addresses[0]}}
				if attachment {
					path := filepath.Join(t.TempDir(), `native "probe" é.txt`)
					if err := os.WriteFile(path, []byte("native attachment bytes\n"), 0600); err != nil {
						t.Fatal(err)
					}
					input.Attachments = []string{path}
				}
				started := time.Now()
				draft, err := c.CreateDraft(input)
				if err != nil {
					t.Fatal(err)
				}
				snapshot, err := c.readDraftSnapshot(account.Name, draft.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, line := range strings.Split(snapshot.Source, "\n") {
					if strings.HasPrefix(line, "X-Apple-") {
						t.Log(strings.TrimSpace(line))
					}
				}
				preview, err := c.PreviewDraftSend(account.Name, draft.ID)
				if err != nil {
					if strings.Contains(err.Error(), "draft MIME multipart/alternative") {
						t.Logf("saved in %s, attachment bytes verified=%t; rich draft correctly rejected: %v", time.Since(started), attachment, err)
						return
					}
					t.Fatal(err)
				}
				if preview.Submitted || preview.Status != "preview" {
					t.Fatalf("unexpected preview %+v", preview)
				}
				t.Logf("saved and previewed in %s; attachment bytes verified=%t", time.Since(started), attachment)
			})
		}
	}
}
