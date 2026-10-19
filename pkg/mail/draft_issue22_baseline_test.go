package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func simpleDraftSnapshot(t *testing.T) *draftSnapshot {
	t.Helper()
	return &draftSnapshot{Message: &Message{ID: "42", Account: "Work", Mailbox: "Drafts", Sender: "Person <alias@example.test>", Subject: "Draft", Content: "body\r\n", ToRecipients: []string{"to@example.test"}, CcRecipients: []string{"cc@example.test"}, BccRecipients: []string{"hidden@example.test"}}, Source: "From: Person <alias@example.test>\r\nTo: to@example.test\r\nCc: cc@example.test\r\nSubject: Draft\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n"}
}
func installDraftSendBridge(t *testing.T, snapshot *draftSnapshot, send, cleanup string) string {
	t.Helper()
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRAFT_SNAPSHOT", string(encoded))
	t.Setenv("DRAFT_SEND_RESPONSE", send)
	t.Setenv("DRAFT_CLEANUP_RESPONSE", cleanup)
	log := filepath.Join(t.TempDir(), "scripts.log")
	t.Setenv("DRAFT_SCRIPT_LOG", log)
	writeFakeOsaScript(t, `printf '%s\n' "$*" >> "$DRAFT_SCRIPT_LOG"
case "$*" in
 *"const draftSnapshotRead = true;"*) printf '%s' "$DRAFT_SNAPSHOT" ;;
 *"JSON.stringify(msg.mailAttachments()"*) printf '[]' ;;
 *"const draftCleanup = true;"*) printf '%s' "$DRAFT_CLEANUP_RESPONSE" ;;
 *'JSON.stringify(composeOutgoing('*) printf '%s' "$DRAFT_SEND_RESPONSE" ;;
 *) printf 'unexpected bridge' >&2; exit 1 ;;
esac`)
	return log
}
func sendReceipt() string {
	return `{"ok":true,"result":{"account":"Work","accountId":"work-id","sender":"alias@example.test","status":"submitted","submitted":true}}`
}
func TestSendDraftPreservesAliasAndRecipientsAndUsesFreshSnapshot(t *testing.T) {
	log := installDraftSendBridge(t, simpleDraftSnapshot(t), sendReceipt(), `{"deleted":true}`)
	result, err := NewClient().SendDraftResult("Work", "42")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Submitted || result.CleanupPending || result.Sender != "alias@example.test" {
		t.Fatalf("result=%+v", result)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	script := string(data)
	for _, required := range []string{`"sender":"alias@example.test"`, `"cc":["cc@example.test"]`, `"bcc":["hidden@example.test"]`, `msg.source() !== expected.source`, `current.content !== expected.message.content`, `msg.delete();`} {
		if !strings.Contains(script, required) {
			t.Errorf("missing %s", required)
		}
	}
	if strings.Contains(script, "msg.send();") || strings.Contains(script, "sqlite3") {
		t.Fatal("stored-object send or stale index path used")
	}
	if strings.Count(script, "const draftSnapshotRead = true;") != 2 {
		t.Fatal("original not reread before send")
	}
}
func TestSendDraftRetainsOriginalAfterFalseUncertainOrCleanupFailure(t *testing.T) {
	for _, tc := range []struct {
		name, send, cleanup                  string
		submitted, uncertain, cleanupPending bool
	}{
		{"false", `{"ok":false,"phase":"send","error":"false","attempted":true,"uncertain":true}`, `{"deleted":true}`, false, true, false},
		{"malformed", "false", `{"deleted":true}`, false, true, false},
		{"cleanup", sendReceipt(), `{"deleted":false}`, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := installDraftSendBridge(t, simpleDraftSnapshot(t), tc.send, tc.cleanup)
			result, err := NewClient().SendDraftResult("Work", "42")
			var mutation *MutationError
			if !errors.As(err, &mutation) {
				t.Fatalf("err=%v", err)
			}
			if result.Submitted != tc.submitted || result.Uncertain != tc.uncertain || result.CleanupPending != tc.cleanupPending {
				t.Fatalf("result=%+v", result)
			}
			data, _ := os.ReadFile(log)
			if !tc.submitted && strings.Contains(string(data), "const draftCleanup = true;") {
				t.Fatal("deleted original without acknowledged send")
			}
		})
	}
}
func TestSendDraftUnsupportedMIMEFailsBeforeCompose(t *testing.T) {
	snapshot := simpleDraftSnapshot(t)
	snapshot.Source = strings.Replace(snapshot.Source, "text/plain", "text/html", 1)
	log := installDraftSendBridge(t, snapshot, sendReceipt(), `{"deleted":true}`)
	result, err := NewClient().SendDraftResult("Work", "42")
	if err == nil || result.Submitted {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, _ := os.ReadFile(log)
	if strings.Contains(string(data), "JSON.stringify(composeOutgoing(") || strings.Contains(string(data), "msg.delete();") {
		t.Fatal("unsupported draft mutated")
	}
}
func TestDraftMIMERejectsLostSemantics(t *testing.T) {
	for _, tc := range []struct{ name, change string }{
		{"thread", "In-Reply-To: <thread@example.test>\r\n"}, {"reply", "Reply-To: other@example.test\r\n"}, {"receipt", "Disposition-Notification-To: other@example.test\r\n"}, {"inline", "Content-ID: <image>\r\n"}, {"duplicate", "Subject: Draft\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := simpleDraftSnapshot(t)
			snapshot.Source = tc.change + snapshot.Source
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted unsupported semantics")
			}
		})
	}
	for _, kind := range []string{"text/html", "multipart/alternative; boundary=x", "multipart/related; boundary=x", "multipart/signed; boundary=x", "application/pkcs7-mime"} {
		t.Run(kind, func(t *testing.T) {
			snapshot := simpleDraftSnapshot(t)
			snapshot.Source = strings.Replace(snapshot.Source, "text/plain; charset=utf-8", kind, 1)
			if _, _, err := reconstructibleDraft(snapshot); err == nil {
				t.Fatal("accepted unsupported MIME")
			}
		})
	}
}
func TestDraftMIMEPlainAndAttachments(t *testing.T) {
	snapshot := simpleDraftSnapshot(t)
	snapshot.Source = "From: alias@example.test\r\nTo: to@example.test\r\nCc: cc@example.test\r\nSubject: Draft\r\nContent-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nbody=0A\r\n--x\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=sample.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nAAECAw==\r\n--x--\r\n"
	body, attachments, err := reconstructibleDraft(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if body != "body\n" || len(attachments) != 1 || string(attachments[0].data) != "\x00\x01\x02\x03" {
		t.Fatalf("body=%q attachments=%+v", body, attachments)
	}
	path := filepath.Join(t.TempDir(), "sample.bin")
	if err := os.WriteFile(path, attachments[0].data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := compareDraftMIMEAttachments([]string{path}, attachments); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("bad"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := compareDraftMIMEAttachments([]string{path}, attachments); err == nil {
		t.Fatal("corrupt bytes accepted")
	}
}
func TestDraftFingerprintIncludesRecipientsAndSource(t *testing.T) {
	original := simpleDraftSnapshot(t)
	changed := simpleDraftSnapshot(t)
	changed.Message.BccRecipients = []string{"other@example.test"}
	if sameDraftSnapshot(original, changed) {
		t.Fatal("recipient change ignored")
	}
	changed = simpleDraftSnapshot(t)
	changed.Source += "changed"
	if sameDraftSnapshot(original, changed) {
		t.Fatal("source change ignored")
	}
}

func TestDraftCleanupProductionScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required for production script mock")
	}
	for _, scenario := range []string{"success", "changed_source", "changed_bcc", "still_present", "read_failure"} {
		t.Run(scenario, func(t *testing.T) {
			snapshot := simpleDraftSnapshot(t)
			encoded, _ := json.Marshal(snapshot)
			prelude := `const scenario = ` + fmt.Sprintf("%q", scenario) + `; const snapshot = ` + string(encoded) + `;
let deletes=0, reads=0;
const props=snapshot.message;
const msg={id:()=>42,deletedStatus:()=>false,subject:()=>props.subject,sender:()=>props.sender,content:()=>props.content,source:()=>scenario==='changed_source' ? 'changed' : snapshot.source,toRecipients:()=>props.toRecipients.map(v=>({address:()=>v})),ccRecipients:()=>props.ccRecipients.map(v=>({address:()=>v})),bccRecipients:()=> (scenario==='changed_bcc' ? ['changed@example.test'] : props.bccRecipients).map(v=>({address:()=>v})),delete:()=>{deletes++}};
const messages={id:()=>{reads++;if(scenario==='read_failure' && deletes)throw new Error('bridge unavailable');return deletes && scenario!=='still_present' ? [] : [42]},byId:id=>{if(id!==42)throw new Error('wrong exact ID');return msg},at:()=>{throw new Error('unsafe positional draft reference')}};
const box={name:()=> 'Drafts',account:()=>acc,messages:messages};
const boxes=()=>[box]; boxes.name=()=>['Drafts'];boxes.mailboxes={name:()=>[[]]};boxes[0]=box;
const acc={id:()=> 'work-id',mailboxes:boxes};const Application=()=>({accounts:{byName:()=>acc},draftsMailbox:()=>({mailboxes:()=>[box]})});const delay=()=>{};
let error='';try{(function(){` + jxaDraftCleanupScript(snapshot) + `})()}catch(e){error=e.message};console.log(JSON.stringify({deletes,error,reads}));`
			command := exec.Command(node, "-e", prelude)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("node=%v %s", err, output)
			}
			var receipt struct {
				Deletes int
				Error   string
				Reads   int
			}
			if err := json.Unmarshal(output, &receipt); err != nil {
				t.Fatal(err)
			}
			if scenario == "success" {
				if receipt.Deletes != 1 || receipt.Error != "" || receipt.Reads < 2 {
					t.Fatalf("receipt=%+v", receipt)
				}
			} else if scenario == "changed_source" || scenario == "changed_bcc" {
				if receipt.Deletes != 0 || receipt.Error == "" {
					t.Fatalf("receipt=%+v", receipt)
				}
			} else if receipt.Deletes != 1 || receipt.Error == "" {
				t.Fatalf("receipt=%+v", receipt)
			}
		})
	}
}
func TestReadDraftSnapshotRejectsUnavailableFields(t *testing.T) {
	for _, property := range []string{"subject", "content", "sender", "toRecipients", "ccRecipients", "bccRecipients"} {
		t.Run(property, func(t *testing.T) {
			snapshot := simpleDraftSnapshot(t)
			encoded, _ := json.Marshal(snapshot)
			var document map[string]interface{}
			if err := json.Unmarshal(encoded, &document); err != nil {
				t.Fatal(err)
			}
			document["message"].(map[string]interface{})[property] = nil
			response, _ := json.Marshal(document)
			t.Setenv("SNAPSHOT_RESPONSE", string(response))
			writeFakeOsaScript(t, `printf '%s' "$SNAPSHOT_RESPONSE"`)
			if _, err := NewClient().readDraftSnapshot("Work", "42"); err == nil {
				t.Fatal("unavailable property accepted")
			}
		})
	}
}

func TestUpdateDraftPreservesOriginalSenderAlias(t *testing.T) {
	snapshot := simpleDraftSnapshot(t)
	messageJSON, _ := json.Marshal(snapshot)
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	t.Setenv("ORIGINAL_DRAFT", string(messageJSON))
	log := filepath.Join(t.TempDir(), "scripts.log")
	t.Setenv("UPDATE_SCRIPT_LOG", log)
	writeFakeOsaScript(t, `printf '%s\n' "$*" >> "$UPDATE_SCRIPT_LOG"
case "$*" in
 *'JSON.stringify(composeOutgoing('*) printf '%s' '{"ok":true,"result":{"account":"Work","accountId":"work-id","sender":"alias@example.test","status":"saved","submitted":false,"draft":{"id":"43","account":"Work","mailbox":"Drafts"}}}' ;;
 *'const draftSnapshotRead = true;'*) printf '%s' "$ORIGINAL_DRAFT" ;;
 *'const draftCleanup = true;'*) printf '%s' '{"deleted":true}' ;;
 *'const messageIds ='*) printf '%s' '{"42":"body\r\n"}' ;;
 *'JSON.stringify(msg.mailAttachments()'*) printf '[]' ;;
 *'function addMailbox(mbox,'*) printf '[{"name":"Drafts","account":"Work"}]' ;;
 *'const accounts = mail.accounts();'*) printf '[{"id":"work-id","name":"Work","enabled":true,"emailAddresses":["primary@example.test","alias@example.test"]}]' ;;
 *'let indices = Array.from'*) printf '[%s]' "$ORIGINAL_DRAFT" ;;
 *'msg.delete();'*) printf 'Success' ;;
 *) printf 'unexpected bridge' >&2;exit 1 ;;
esac`)
	result, err := NewClient().UpdateDraft("Work", "42", DraftInput{Subject: "Changed", SubjectSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != "43" {
		t.Fatalf("result=%+v", result)
	}
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), `"sender":"alias@example.test"`) {
		t.Fatal("original alias not passed to replacement save")
	}
}

func TestPreviewDraftSendReadsStrictRecipientsWithoutMutation(t *testing.T) {
	log := installDraftSendBridge(t, simpleDraftSnapshot(t), `{"ok":true,"result":{"account":"Work","accountId":"work-id","sender":"alias@example.test","status":"preview","submitted":false}}`, `{"deleted":true}`)
	result, err := NewClient().PreviewDraftSend("Work", "42")
	if err != nil {
		t.Fatal(err)
	}
	if result.Submitted || result.Status != "preview" || result.Sender != "alias@example.test" {
		t.Fatalf("result=%+v", result)
	}
	data, _ := os.ReadFile(log)
	script := string(data)
	for _, required := range []string{`const draftSnapshotRead = true;`, `"sender":"alias@example.test"`, `"to":["to@example.test"]`, `"cc":["cc@example.test"]`, `"bcc":["hidden@example.test"]`, `, "preview"));`} {
		if !strings.Contains(script, required) {
			t.Errorf("missing %s", required)
		}
	}
	if strings.Count(script, "const draftSnapshotRead = true;") != 1 || strings.Contains(script, "JSON.stringify(msg.mailAttachments()") || strings.Contains(script, "const draftCleanup = true;") || strings.Contains(script, `, "send"));`) || strings.Contains(script, `, "save"));`) {
		t.Fatal("preview exported attachments or invoked mutation")
	}
}
func TestPreviewDraftSendRejectsRichDraftBeforeOutgoing(t *testing.T) {
	snapshot := simpleDraftSnapshot(t)
	snapshot.Source = strings.Replace(snapshot.Source, "text/plain", "text/html", 1)
	log := installDraftSendBridge(t, snapshot, `{"ok":true,"result":{"status":"preview"}}`, `{"deleted":true}`)
	if _, err := NewClient().PreviewDraftSend("Work", "42"); err == nil {
		t.Fatal("unsupported rich draft accepted")
	}
	data, _ := os.ReadFile(log)
	if strings.Contains(string(data), "JSON.stringify(composeOutgoing(") {
		t.Fatal("rich draft reached outgoing preview")
	}
}
func TestCreateDraftRetainsSavedDraftAfterAttachmentVerificationFailure(t *testing.T) {
	attachment := filepath.Join(t.TempDir(), "sample.bin")
	if err := os.WriteFile(attachment, []byte("attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	writeFakeOsaScript(t, `case "$*" in
 *'JSON.stringify(composeOutgoing('*) printf '%s' '{"ok":true,"result":{"account":"Work","accountId":"work-id","sender":"alias@example.test","status":"saved","submitted":false,"draft":{"id":"43","account":"Work","mailbox":"Drafts"}}}' ;;
 *'JSON.stringify(msg.mailAttachments()'*) printf '[]' ;;
 *) printf 'unexpected bridge' >&2;exit 1 ;;
esac`)
	draft, err := NewClient().CreateDraft(DraftInput{Account: "Work", Sender: "alias@example.test", To: []string{"to@example.test"}, Attachments: []string{attachment}})
	var mutation *MutationError
	if !errors.As(err, &mutation) || !mutation.Saved || mutation.Phase != "attachment_verification" {
		t.Fatalf("err=%v", err)
	}
	if draft == nil || draft.ID != "43" {
		t.Fatalf("saved draft receipt lost: %+v", draft)
	}
}
