package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type checkpointRecoveryFixture struct {
	client                         *Client
	root, path, statePath, logPath string
	messages                       map[string]*Message
	accountID                      string
	failBodies                     map[string]bool
	metadataDelay                  time.Duration
}

func newCheckpointRecoveryFixture(t *testing.T) *checkpointRecoveryFixture {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for isolated bridge fixture")
	}
	client, _ := snapshotSQLiteFixture(t, `alter table mailboxes add column total_count integer default 0;alter table mailboxes add column unread_count integer default 0;
insert into mailboxes(rowid,url) values (1,'imap://ABC/INBOX'),(2,'imap://ABC/Projects/Archive'),(3,'imap://DEF/INBOX');
 insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (101,1,1,1,1787961600,42,0,0);`)
	_ = client.SetBackend(BackendLegacy)
	root := t.TempDir()
	index := filepath.Join(root, "V10", "MailData", "Envelope Index")
	if err := os.MkdirAll(filepath.Dir(index), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", index)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(root, "lock"))
	f := &checkpointRecoveryFixture{client: client, root: root, path: filepath.Join(root, "checkpoint.json"), statePath: filepath.Join(root, "bridge.json"), logPath: filepath.Join(root, "bridge.log"), messages: map[string]*Message{}, accountID: "ABC", failBodies: map[string]bool{}}
	f.messages["101"] = &Message{ID: "101", Account: "Work", Mailbox: "INBOX", RFCMessageID: "<stable@fixture.test>", Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42, Content: "old body"}
	t.Setenv("CHECKPOINT_BRIDGE_STATE", f.statePath)
	t.Setenv("CHECKPOINT_BRIDGE_LOG", f.logPath)
	script := `#!` + python + `
import os,sys,json,re,base64,time

def execute(script, body):
    state=json.load(open(os.environ['CHECKPOINT_BRIDGE_STATE']))
    if "JSON.stringify({id:trashAccountID(account)})" in script:
        return {'id':state['accountID']}
    match=re.search(r"const requestedId = '([0-9]+)'",script) if body else re.search(r"Number\('([0-9]+)'\)",script)
    if match is None: raise RuntimeError('unexpected bridge operation')
    mid=match.group(1)
    with open(os.environ['CHECKPOINT_BRIDGE_LOG'],'a') as log: log.write(('body ' if body else 'metadata ')+mid+'\n')
    if not body and mid in state['messages']: time.sleep(state.get('metadataDelay',0))
    msg=state['messages'].get(mid)
    if body:
        if state.get('failBodies',{}).get(mid,False):
            return {'message':None,'failure':{'phase':'content','message':'controlled extraction failure','number':1},'phases':[]}
        return {'message':msg,'phases':[]}
    return msg
script=sys.argv[-1]
if "const input = $.NSFileHandle" in script:
    for line in sys.stdin:
        try: decoded=base64.b64decode(line).decode('utf-8'); result=execute(decoded,"const requestedId =" in decoded); print(json.dumps({'output':json.dumps(result)}),flush=True)
        except Exception as error: print(json.dumps({'error':str(error)}),flush=True)
else:
    print(json.dumps(execute(script,False)))
`
	if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), "osascript"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	f.writeState(t)
	f.writeBody(t, "101", "INBOX", "old body", true)
	return f
}
func (f *checkpointRecoveryFixture) writeState(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"accountID": f.accountID, "messages": f.messages, "failBodies": f.failBodies, "metadataDelay": f.metadataDelay.Seconds()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.statePath, data, 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *checkpointRecoveryFixture) writeBody(t *testing.T, id, mailbox, body string, rfc bool) {
	t.Helper()
	header := "From: sender@example.com\r\nSubject: subject\r\nDate: Sat, 29 Aug 2026 00:00:00 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n"
	if rfc {
		header += "Message-ID: <stable@fixture.test>\r\n"
	}
	raw := header + "\r\n" + body
	directory := filepath.Join(f.root, "V10", "ABC")
	for _, part := range strings.Split(mailbox, "/") {
		directory = filepath.Join(directory, part+".mbox")
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	path := filepath.Join(directory, "fixture", messageDataDirectory(n), "Messages", id+".emlx")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(fmt.Sprintf("%d\n%s<plist>mutable read flags</plist>", len(raw), raw)), 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *checkpointRecoveryFixture) sql(t *testing.T, query string) {
	t.Helper()
	out, err := exec.Command(os.Getenv("SNAPSHOT_SQLITE_BINARY"), os.Getenv("SNAPSHOT_SQLITE_DATABASE"), query).CombinedOutput()
	if err != nil {
		t.Fatalf("fixture SQL: %v %s", err, out)
	}
}
func (f *checkpointRecoveryFixture) read(t *testing.T, refs []MessageRef, timeout, budget time.Duration) SelectedReadResult {
	t.Helper()
	result, err := f.client.ReadSelectedMessagesWithOptions(refs, timeout, budget, SelectedReadOptions{Checkpoint: f.path, SelectionIDs: []string{"101"}})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func (f *checkpointRecoveryFixture) start(t *testing.T) {
	t.Helper()
	result := f.read(t, []MessageRef{{AccountName: "Work", MailboxName: "INBOX", MessageID: "101"}}, 5*time.Second, 15*time.Second)
	if !result.Complete {
		t.Fatalf("initial extraction=%+v", result)
	}
}
func (f *checkpointRecoveryFixture) calls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(f.logPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func (f *checkpointRecoveryFixture) relocate(t *testing.T, recycle bool) {
	t.Helper()
	f.sql(t, "delete from messages where rowid=101;insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (201,2,1,1,1787961600,42,1,0);")
	original := *f.messages["101"]
	delete(f.messages, "101")
	original.ID = "201"
	original.Mailbox = "Projects/Archive"
	original.Read = true
	original.Flagged = true
	f.messages["201"] = &original
	f.writeBody(t, "201", "Projects/Archive", "old body", true)
	if recycle {
		f.sql(t, "insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (101,1,2,2,1787961600,42,0,0);")
		other := original
		other.ID = "101"
		other.Mailbox = "INBOX"
		other.RFCMessageID = "<unrelated@fixture.test>"
		other.Subject = "near subject"
		other.Sender = "other@example.com"
		f.messages["101"] = &other
	}
	f.writeState(t)
}

func TestReadCheckpointRecoveryRegeneratedIDAndRecycledOriginal(t *testing.T) {
	for _, recycle := range []bool{false, true} {
		t.Run(fmt.Sprintf("recycled=%v", recycle), func(t *testing.T) {
			f := newCheckpointRecoveryFixture(t)
			f.start(t)
			f.relocate(t, recycle)
			// No fresh locator result for original selection: the stored immutable
			// origin is the only authority for recovering its current address.
			result := f.read(t, nil, 5*time.Second, 15*time.Second)
			if !result.Complete || len(result.Items) != 1 || result.Items[0].ID != "101" || result.Items[0].Message.ID != "201" || result.Items[0].Message.Mailbox != "Projects/Archive" || !result.Items[0].Message.Read || !result.Items[0].Message.Flagged || result.Items[0].Message.Content != "old body" {
				t.Fatalf("recovered=%+v", result)
			}
			if strings.Count(f.calls(t), "body 101\n") != 1 || strings.Contains(f.calls(t), "body 201\n") {
				t.Fatalf("unchanged complete disk body should be reused: %s", f.calls(t))
			}
			// Persisted selection remains the requested key even after another resume.
			third := f.read(t, nil, 5*time.Second, 15*time.Second)
			if !third.Complete || third.Items[0].Message.ID != "201" {
				t.Fatalf("third=%+v", third)
			}
		})
	}
}

func TestReadCheckpointRecoveryRereadsChangedContentIncludingSameSize(t *testing.T) {
	for _, size := range []int{42, 99} {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			f := newCheckpointRecoveryFixture(t)
			f.start(t)
			f.messages["101"].Content = "new body"
			f.messages["101"].MessageSize = size
			f.messages["101"].Read = true
			f.sql(t, fmt.Sprintf("update messages set size=%d,read=1 where rowid=101;", size))
			f.writeBody(t, "101", "INBOX", "new body", true)
			f.writeState(t)
			result := f.read(t, checkpointRefsForRecovery("101"), 5*time.Second, 15*time.Second)
			if !result.Complete || result.Items[0].Message.Content != "new body" || !result.Items[0].Message.Read {
				t.Fatalf("changed body=%+v", result)
			}
			if strings.Count(f.calls(t), "body 101\n") != 2 {
				t.Fatalf("body not re-extracted: %s", f.calls(t))
			}
			again := f.read(t, checkpointRefsForRecovery("101"), 5*time.Second, 15*time.Second)
			if !again.Complete || strings.Count(f.calls(t), "body 101\n") != 2 {
				t.Fatalf("revision not safely retained: %+v %s", again, f.calls(t))
			}
		})
	}
}
func checkpointRefsForRecovery(ids ...string) []MessageRef {
	var refs []MessageRef
	for _, id := range ids {
		refs = append(refs, MessageRef{AccountName: "Work", MailboxName: "INBOX", MessageID: id})
	}
	return refs
}

func TestReadCheckpointRecoveryRejectsAmbiguousCrossAccountAndUncorroboratedContent(t *testing.T) {
	for _, kind := range []string{"duplicate-rfc", "other-account-only", "account-reassigned", "no-rfc-changed-content", "candidate-cap"} {
		t.Run(kind, func(t *testing.T) {
			f := newCheckpointRecoveryFixture(t)
			if kind == "no-rfc-changed-content" {
				f.messages["101"].RFCMessageID = ""
				f.writeBody(t, "101", "INBOX", "old body", false)
				f.writeState(t)
			}
			f.start(t)
			switch kind {
			case "duplicate-rfc":
				f.relocate(t, false)
				copy := *f.messages["201"]
				copy.ID = "202"
				f.messages["202"] = &copy
				f.sql(t, "insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (202,2,1,1,1787961600,42,1,0);")
			case "other-account-only":
				f.relocate(t, false)
				f.sql(t, "update messages set mailbox=3 where rowid=201;")
				f.messages["201"].Account = "Personal"
			case "account-reassigned":
				f.accountID = "DEF"
			case "no-rfc-changed-content":
				f.writeBody(t, "101", "INBOX", "new body", false)
			case "candidate-cap":
				f.relocate(t, false)
				for id := 202; id <= 234; id++ {
					f.sql(t, fmt.Sprintf("insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (%d,2,1,1,1787961600,42,1,0);", id))
				}
			}
			f.writeState(t)
			for invocation := 0; invocation < 2; invocation++ {
				result := f.read(t, nil, 5*time.Second, 15*time.Second)
				if result.Complete || len(result.Items) != 1 || result.Items[0].Message != nil || result.Items[0].Error == "" {
					t.Fatalf("unsafe recovery=%+v", result)
				}
			}
			if strings.Count(f.calls(t), "body ") != 1 {
				t.Fatalf("unsafe replacement read: %s", f.calls(t))
			}
		})
	}
}

func TestReadCheckpointRecoveryFailedRereadRetriesOriginalIdentity(t *testing.T) {
	f := newCheckpointRecoveryFixture(t)
	f.start(t)
	f.writeBody(t, "101", "INBOX", "new body", true)
	f.messages["101"].Content = "new body"
	f.failBodies["101"] = true
	f.writeState(t)
	result := f.read(t, checkpointRefsForRecovery("101"), 5*time.Second, 15*time.Second)
	if result.Complete || result.Items[0].Error == "" {
		t.Fatalf("failure=%+v", result)
	}
	f.failBodies["101"] = false
	f.writeState(t)
	result = f.read(t, checkpointRefsForRecovery("101"), 5*time.Second, 15*time.Second)
	if !result.Complete || result.Items[0].Message.Content != "new body" {
		t.Fatalf("retry=%+v", result)
	}
	if strings.Count(f.calls(t), "body 101\n") != 3 {
		t.Fatalf("unexpected reread attempts: %s", f.calls(t))
	}
}

func TestReadCheckpointRecoveryMailSourceAlwaysRereads(t *testing.T) {
	f := newCheckpointRecoveryFixture(t)
	_ = f.client.SetBodySource(BodySourceMail)
	f.start(t)
	f.messages["101"].Content = "new body"
	f.writeState(t)
	result := f.read(t, checkpointRefsForRecovery("101"), 5*time.Second, 15*time.Second)
	if !result.Complete || result.Items[0].Message.Content != "new body" || strings.Count(f.calls(t), "body 101\n") != 2 {
		t.Fatalf("mail source=%+v calls=%s", result, f.calls(t))
	}
}

func TestReadCheckpointRecoveryBudgetsBoundCandidateConfirmation(t *testing.T) {
	f := newCheckpointRecoveryFixture(t)
	f.start(t)
	f.relocate(t, false)
	f.metadataDelay = time.Second
	f.writeState(t)
	start := time.Now()
	result := f.read(t, nil, 250*time.Millisecond, 2*time.Second)
	if result.Complete || result.Items[0].Error == "" || time.Since(start) > 2*time.Second || !strings.Contains(f.calls(t), "metadata 201\n") || strings.Contains(f.calls(t), "body 201\n") {
		t.Fatalf("unbounded recovery=%+v elapsed=%s calls=%s", result, time.Since(start), f.calls(t))
	}
	start = time.Now()
	result = f.read(t, nil, 2*time.Second, 250*time.Millisecond)
	if result.Complete || result.Items[0].Error == "" || time.Since(start) > 2*time.Second || strings.Contains(f.calls(t), "body 201\n") {
		t.Fatalf("overall budget ignored: %+v elapsed=%s", result, time.Since(start))
	}
}
