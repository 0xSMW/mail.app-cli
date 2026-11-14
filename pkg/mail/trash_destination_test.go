package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func nativeTrashFixture(t *testing.T) (*nativeReadFixture, *Client) {
	t.Helper()
	f := newNativeReadFixture()
	root, trash := fixtureRef("mbxp", "trash-root"), fixtureRef("mbxp", "trash")
	f.properties["app"]["trmb"] = root
	f.properties["mbxp:trash"] = map[string]appleevents.Value{"pnam": appleevents.String("Deleted Items"), "mact": fixtureRef("mact", "a")}
	f.boxes["mbxp:trash-root"] = []appleevents.Value{trash}
	f.boxes["mact:a"] = append(f.boxes["mact:a"], trash)
	return f, fixtureClient(t, f)
}

func TestResolveTrashMailboxUsesSpecialAccountOwnedPhysicalPath(t *testing.T) {
	f, c := nativeTrashFixture(t)
	f.properties["mbxp:trash"]["pnam"] = appleevents.String("Corbeille")
	f.properties["mbxp:trash"]["mbxc"] = fixtureRef("mbxp", "provider")
	f.properties["mbxp:provider"] = map[string]appleevents.Value{"pnam": appleevents.String("[Gmail]"), "mact": fixtureRef("mact", "a")}
	// A similarly named user folder cannot replace Mail's configured destination.
	f.properties["mbxp:custom"] = map[string]appleevents.Value{"pnam": appleevents.String("Trash"), "mact": fixtureRef("mact", "a")}
	f.boxes["mact:a"] = append(f.boxes["mact:a"], fixtureRef("mbxp", "custom"))
	destination, err := c.ResolveTrashMailbox("Personal")
	if err != nil || destination != "[Gmail]/Corbeille" {
		t.Fatalf("destination=%q err=%v", destination, err)
	}
	for _, event := range f.events {
		if event.ID != "getd" {
			t.Fatalf("resolution wrote to Mail: %+v", event)
		}
	}
}

func TestResolveTrashMailboxFailsClosed(t *testing.T) {
	for _, name := range []string{"missing", "duplicate", "other-account", "bad-owner", "cycle", "transport"} {
		t.Run(name, func(t *testing.T) {
			f, c := nativeTrashFixture(t)
			switch name {
			case "missing":
				f.boxes["mbxp:trash-root"] = nil
			case "duplicate":
				f.boxes["mbxp:trash-root"] = append(f.boxes["mbxp:trash-root"], fixtureRef("mbxp", "second"))
				f.properties["mbxp:second"] = map[string]appleevents.Value{"pnam": appleevents.String("Trash"), "mact": fixtureRef("mact", "a")}
			case "other-account":
				f.properties["mbxp:trash"]["mact"] = fixtureRef("mact", "other")
				f.properties["mact:other"] = map[string]appleevents.Value{"ID  ": appleevents.String("other")}
			case "bad-owner":
				f.properties["mbxp:trash"]["mact"] = appleevents.String("a")
			case "cycle":
				f.properties["mbxp:trash"]["mbxc"] = fixtureRef("mbxp", "trash")
			case "transport":
				f.fail = func(appleevents.Event) error { return &appleevents.Error{Code: -1712, Message: "timed out"} }
			}
			if destination, err := c.ResolveTrashMailbox("Personal"); err == nil || destination != "" {
				t.Fatalf("unsafe destination=%q err=%v", destination, err)
			}
		})
	}
}

func TestPrepareDeleteItemRejectsAlreadyTrashAndWrongDestination(t *testing.T) {
	for _, tc := range []struct {
		source, target string
		wantErr        bool
	}{
		{source: "INBOX"}, {source: "Deleted Items", wantErr: true},
		{source: "INBOX", target: "Trash", wantErr: true}, {source: "Missing", wantErr: true},
	} {
		t.Run(tc.source+tc.target, func(t *testing.T) {
			f, c := nativeTrashFixture(t)
			item := BatchItem{Account: "Personal", SourceMailbox: tc.source, TargetMailbox: tc.target}
			err := c.prepareDeleteItem(&item, "")
			if (err != nil) != tc.wantErr {
				t.Fatalf("item=%+v err=%v", item, err)
			}
			if err == nil && item.TargetMailbox != "Deleted Items" {
				t.Fatalf("target not pinned: %+v", item)
			}
			for _, event := range f.events {
				if event.ID != "getd" {
					t.Fatalf("preflight wrote to Mail: %+v", event)
				}
			}
		})
	}
}

func TestJXATrashMailboxHelperRejectsAmbiguousAndNestedTrashSource(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	script := jxaTrashMailboxHelper() + `
const owner = {id:()=> 'a'};
const parent = {name:()=> '[Gmail]',container:()=>null,account:()=>owner,mailboxes:()=>[trash]};
const trash = {name:()=> 'Corbeille',container:()=>parent,account:()=>owner,mailboxes:()=>[]};
const inbox = {name:()=> 'INBOX',container:()=>null,account:()=>owner,mailboxes:()=>[]};
const account = {id:()=> 'a',mailboxes:()=>[parent,inbox]};
const mail = {trashMailbox:()=>({mailboxes:()=>[trash]})};
if (trashPathForAccount(mail,account) !== '[Gmail]/Corbeille') throw new Error('wrong special path');
if (deleteSourcePath(account,'Corbeille') !== '[Gmail]/Corbeille') throw new Error('wrong source path');
mail.trashMailbox=()=>({mailboxes:()=>[]});
let failed=false; try { trashPathForAccount(mail,account) } catch(e) {failed=true} if(!failed) throw new Error('accepted missing Trash');
mail.trashMailbox=()=>({mailboxes:()=>[trash,inbox]});
failed=false; try { trashPathForAccount(mail,account) } catch(e) {failed=true} if(!failed) throw new Error('accepted ambiguous Trash');
const flat = {name:()=> '[Gmail]/Corbeille',container:()=>null,account:()=>owner};
mail.trashMailbox=()=>({mailboxes:()=>[trash,flat]});
failed=false; try { trashPathForAccount(mail,account) } catch(e) {failed=true} if(!failed) throw new Error('accepted distinct physical paths with same joined spelling');
trash.container=()=>trash;
failed=false; try { trashCanonicalPath(trash,'a') } catch(e) {failed=true} if(!failed) throw new Error('accepted parent cycle');
`
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("JXA helper: %v: %s", err, out)
	}
}

// installTrashVerificationBridge augments an isolated SQLite fixture without
// ever opening the user's Mail data or talking to the Mail application.
func installTrashVerificationBridge(t *testing.T, trash, source string) {
	t.Helper()
	trashJSON, _ := json.Marshal(map[string]string{"path": trash})
	sourceJSON, _ := json.Marshal(map[string]string{"path": source})
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
 *"JSON.stringify({path: trashPathForAccount"*) printf '%%s\n' '%s' ;;
 *"JSON.stringify({path: deleteSourcePath"*) printf '%%s\n' '%s' ;;
 *) printf 'unexpected Mail bridge request' >&2; exit 93 ;;
esac
`, trashJSON, sourceJSON)
	if err := os.WriteFile(filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "osascript"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
}

func TestDeleteVerificationRequiresActualTrashIdentityAndRequestedRead(t *testing.T) {
	for _, tc := range []struct {
		name, data, target string
		markRead           bool
		status             string
	}{
		{name: "regenerated Trash ID", data: `insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,2,1,1,1787961600,42,1,0);`, markRead: true, status: "confirmed_destination"},
		{name: "absent everywhere", markRead: true, status: "applied_destination_unverified"},
		{name: "regenerated source ID", data: `insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,1,1,1,1787961600,42,1,0),(206,2,1,1,1787961600,42,1,0);`, markRead: true, status: "present_in_source"},
		{name: "unread Trash", data: `insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,2,1,1,1787961600,42,0,0);`, markRead: true, status: "destination_read_unverified"},
		{name: "unread no requested mark", data: `insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,2,1,1,1787961600,42,0,0);`, status: "confirmed_destination"},
		{name: "ambiguous identity", data: `insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,2,1,1,1787961600,42,1,0),(206,2,1,1,1787961600,42,1,0);`, markRead: true, status: "applied_destination_unverified"},
		{name: "unrelated destination", target: "Archive", markRead: true, status: "applied_destination_unverified"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := snapshotSQLiteFixture(t, `insert into mailboxes(rowid,url) values (1,'ews://ABC/INBOX'),(2,'ews://ABC/Deleted%20Items');`+tc.data)
			installTrashVerificationBridge(t, "Deleted Items", "INBOX")
			item := BatchItem{ID: "101", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Deleted Items", Identity: snapshotTestIdentity(), Status: "succeeded", MarkedRead: tc.markRead}
			if tc.target != "" {
				item.TargetMailbox = tc.target
			}
			previous := verificationBackoff
			verificationBackoff = []time.Duration{0}
			t.Cleanup(func() { verificationBackoff = previous })
			items := VerifyMutations(client, BatchOptions{Action: "delete", MarkReadBefore: tc.markRead}, []BatchItem{item})
			got := items[0]
			if got.VerifyStatus != tc.status {
				t.Fatalf("verification=%+v", got)
			}
			if tc.status == "confirmed_destination" && (got.Observed == nil || len(got.Observed.DestinationIDs) != 1 || got.Observed.DestinationIDs[0] != "205" || got.Observed.SourcePresent) {
				t.Fatalf("missing regenerated destination evidence: %+v", got)
			}
			if tc.name == "ambiguous identity" && !strings.Contains(got.VerifyError, "ambiguous") {
				t.Fatalf("ambiguity lost: %+v", got)
			}
		})
	}
}

func TestDeleteVerificationMissingTrashFailsClosed(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `insert into mailboxes values ('ews://ABC/INBOX');`)
	installTrashVerificationBridge(t, "", "INBOX")
	item := BatchItem{ID: "101", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Deleted Items", Identity: snapshotTestIdentity(), Status: "succeeded"}
	items := []BatchItem{item}
	verifyMutationRelocations(client, BatchOptions{Action: "delete"}, items, []time.Duration{0})
	if items[0].VerifyStatus != "applied_destination_unverified" || items[0].VerifyError == "" {
		t.Fatalf("missing Trash confirmed: %+v", items[0])
	}
}

func TestDeleteMutatorMovesToActualTrashAndRejectsAlreadyTrash(t *testing.T) {
	for _, alreadyTrash := range []bool{false, true} {
		t.Run(fmt.Sprintf("alreadyTrash=%v", alreadyTrash), func(t *testing.T) {
			f, c := nativeTrashFixture(t)
			item := BatchItem{Account: "Personal", SourceMailbox: "INBOX", ID: "42"}
			if alreadyTrash {
				item.SourceMailbox = "Deleted Items"
			}
			err := DeleteMutator(c, &item)
			if (err != nil) != alreadyTrash {
				t.Fatalf("item=%+v err=%v", item, err)
			}
			moves := 0
			for _, event := range f.events {
				if event.ID == "delo" {
					t.Fatal("recoverable delete issued permanent-capable delete command")
				}
				if event.ID == "move" {
					moves++
					if alreadyTrash || fixtureKey(event.Params["insh"]) != "mbxp:trash" {
						t.Fatalf("unsafe Trash move: %+v", event)
					}
				}
			}
			if !alreadyTrash && moves != 1 {
				t.Fatalf("moves=%d want 1", moves)
			}
		})
	}
}

func TestReconcileDeleteReadsActualTrashWithoutReplayingUnknownMutation(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `insert into mailboxes(rowid,url) values (1,'ews://ABC/INBOX'),(2,'ews://ABC/Deleted%20Items');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,2,1,1,1787961600,42,1,0);`)
	installTrashVerificationBridge(t, "Deleted Items", "INBOX")
	previous := verificationBackoff
	verificationBackoff = []time.Duration{0}
	t.Cleanup(func() { verificationBackoff = previous })
	item := BatchItem{ID: "101", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Deleted Items", Identity: snapshotTestIdentity(), Status: "unknown", MarkedRead: true}
	got, err := ReconcileMutations(client, BatchOptions{}, BatchResult{Action: "delete", MarkReadRequested: true, Attempted: 1, Failed: 1, Items: []BatchItem{item}})
	if err != nil || got.Unverified != 0 || got.Items[0].Status != "unknown" || got.Items[0].VerifyStatus != "confirmed_destination" {
		t.Fatalf("reconcile=%+v err=%v", got, err)
	}
	if got.Items[0].Observed == nil || len(got.Items[0].Observed.DestinationIDs) != 1 || got.Items[0].Observed.DestinationIDs[0] != "205" || got.Items[0].Observed.SourcePresent {
		t.Fatalf("bad destination evidence: %+v", got.Items[0])
	}
}

func TestDeleteMutatorKeepsArchiveNamedTrashLiteral(t *testing.T) {
	f, c := nativeTrashFixture(t)
	f.properties["mbxp:trash"]["pnam"] = appleevents.String("Archive")
	item := BatchItem{Account: "Personal", SourceMailbox: "INBOX", ID: "42"}
	if err := DeleteMutator(c, &item); err != nil {
		t.Fatal(err)
	}
	for _, event := range f.events {
		if event.ID == "move" {
			if fixtureKey(event.Params["insh"]) != "mbxp:trash" {
				t.Fatalf("Trash name reinterpreted as archive alias: %+v", event)
			}
			return
		}
	}
	t.Fatal("no Trash move")
}

func TestRunBatchDeleteMarksThenMovesAndVerifiesStableTrashIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, mutation, wantStatus string
		missingTrash, alreadyTrash bool
	}{
		{name: "read regenerated Trash ID", mutation: "update messages set rowid=205,mailbox=2 where rowid=101;", wantStatus: "confirmed_destination"},
		{name: "lost after move", mutation: "delete from messages where rowid=101;", wantStatus: "applied_destination_unverified"},
		{name: "regenerated source copy", mutation: "update messages set rowid=205 where rowid=101; insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (206,2,1,1,1787961600,42,1,0);", wantStatus: "present_in_source"},
		{name: "missing Trash preflight", missingTrash: true},
		{name: "already Trash preflight", alreadyTrash: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := snapshotSQLiteFixture(t, `insert into mailboxes(rowid,url) values (1,'ews://ABC/INBOX'),(2,'ews://ABC/Deleted%20Items');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (101,1,1,1,1787961600,42,0,0);`)
			trash, source := "Deleted Items", "INBOX"
			if tc.missingTrash {
				trash = ""
			}
			if tc.alreadyTrash {
				source = "Deleted Items"
			}
			trashJSON, _ := json.Marshal(map[string]string{"path": trash})
			sourceJSON, _ := json.Marshal(map[string]string{"path": source})
			messageJSON, _ := json.Marshal(Message{ID: "101", Account: "Work", Mailbox: source, Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42})
			writes := filepath.Join(t.TempDir(), "writes.log")
			t.Setenv("TRASH_BATCH_WRITES", writes)
			t.Setenv("TRASH_BATCH_MUTATION", tc.mutation)
			script := fmt.Sprintf(`#!/bin/sh
case "$*" in
 *"JSON.stringify({path: trashPathForAccount"*) printf '%%s\n' '%s' ;;
 *"JSON.stringify({path: deleteSourcePath"*) printf '%%s\n' '%s' ;;
 *"msg.readStatus = true;"*)
  printf 'mark\n' >> "$TRASH_BATCH_WRITES"
  "$SNAPSHOT_SQLITE_BINARY" "$SNAPSHOT_SQLITE_DATABASE" 'update messages set read=1 where rowid=101;' || exit 94
  printf 'Success\n' ;;
 *"messages[0].mailbox = destination;"*)
  [ "$("$SNAPSHOT_SQLITE_BINARY" "$SNAPSHOT_SQLITE_DATABASE" 'select read from messages where rowid=101;')" = 1 ] || exit 95
  printf 'move\n' >> "$TRASH_BATCH_WRITES"
  "$SNAPSHOT_SQLITE_BINARY" "$SNAPSHOT_SQLITE_DATABASE" "$TRASH_BATCH_MUTATION" || exit 96
  printf 'Success\n' ;;
 *"mbox.messages.byId(Number("*) printf '%%s\n' '%s' ;;
 *) printf 'unexpected Mail bridge request' >&2; exit 93 ;;
esac
`, trashJSON, sourceJSON, messageJSON)
			bridge := filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "osascript")
			if err := os.WriteFile(bridge, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			previous := verificationBackoff
			verificationBackoff = []time.Duration{0}
			t.Cleanup(func() { verificationBackoff = previous })
			journalPath := filepath.Join(t.TempDir(), "receipt.jsonl")
			journal, err := CreateBatchJournal(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { journal.Close() })
			item := BatchItem{ID: "101", Account: "Work", SourceMailbox: source}
			got, err := RunBatch(client, BatchOptions{Action: "delete", MarkReadBefore: true, Verify: true, Receipt: journal}, []BatchItem{item}, DeleteMutator)
			if len(got.Items) != 1 {
				t.Fatalf("batch=%+v err=%v", got, err)
			}
			applied := got.Items[0]
			if tc.missingTrash || tc.alreadyTrash {
				if err == nil || applied.Status != "failed" || applied.MarkedRead {
					t.Fatalf("unsafe preflight batch=%+v err=%v", got, err)
				}
				if _, statErr := os.Stat(writes); !os.IsNotExist(statErr) {
					t.Fatalf("preflight issued a mark or move: %v", statErr)
				}
				return
			}
			if applied.Status != "succeeded" || applied.VerifyStatus != tc.wantStatus || !applied.MarkedRead || !applied.Identity.valid() || applied.TargetMailbox != "Deleted Items" {
				t.Fatalf("batch=%+v err=%v", got, err)
			}
			if (err == nil) != (tc.wantStatus == "confirmed_destination") {
				t.Fatalf("verification error=%v status=%s", err, tc.wantStatus)
			}
			sequence, err := os.ReadFile(writes)
			if err != nil || string(sequence) != "mark\nmove\n" {
				t.Fatalf("write order=%q err=%v", sequence, err)
			}
			if tc.wantStatus == "confirmed_destination" && (applied.Observed == nil || applied.Observed.SourcePresent || len(applied.Observed.DestinationIDs) != 1 || applied.Observed.DestinationIDs[0] != "205" || applied.Observed.DestinationRead == nil || !*applied.Observed.DestinationRead) {
				t.Fatalf("unproven destination: %+v", applied)
			}
			data, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			previousPhase := -1
			for _, phase := range []string{"identity_capture_result", "mark_read_started", "mark_read_succeeded", "mutation_started", "mutation_succeeded", "verification_result"} {
				needle := `"event":"` + phase + `"`
				position := strings.Index(string(data), needle)
				if position <= previousPhase {
					t.Fatalf("missing or unordered durable phase %s", phase)
				}
				previousPhase = position
			}
		})
	}
}

func TestDeleteVerificationKeepsArchivePhysicalScopesLiteral(t *testing.T) {
	for _, tc := range []struct{ name, source, trash, data, want string }{
		{name: "All Mail copy cannot prove Archive Trash", source: "INBOX", trash: "Archive", data: `insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,3,1,1,1787961600,42,1,0);`, want: "applied_destination_unverified"},
		{name: "actual Archive Trash copy confirms", source: "INBOX", trash: "Archive", data: `insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,2,1,1,1787961600,42,1,0);`, want: "confirmed_destination"},
		{name: "Archive source survives beside empty All Mail", source: "Archive", trash: "Deleted Items", data: `insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,2,1,1,1787961600,42,1,0),(206,4,1,1,1787961600,42,1,0);`, want: "present_in_source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := snapshotSQLiteFixture(t, `insert into mailboxes(rowid,url) values (1,'imap://ABC/INBOX'),(2,'imap://ABC/Archive'),(3,'imap://ABC/%5BGmail%5D/All%20Mail'),(4,'imap://ABC/Deleted%20Items');`+tc.data)
			installTrashVerificationBridge(t, tc.trash, tc.source)
			previous := verificationBackoff
			verificationBackoff = []time.Duration{0}
			t.Cleanup(func() { verificationBackoff = previous })
			item := BatchItem{ID: "101", Account: "Work", SourceMailbox: tc.source, TargetMailbox: tc.trash, Identity: snapshotTestIdentity(), Status: "succeeded", MarkedRead: true}
			got := VerifyMutations(client, BatchOptions{Action: "delete", MarkReadBefore: true}, []BatchItem{item})[0]
			if got.VerifyStatus != tc.want {
				t.Fatalf("literal physical scope verification=%+v", got)
			}
		})
	}
}

func TestDeleteVerificationReadsRFCIdentityInLiteralArchiveTrash(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `insert into mailboxes(rowid,url) values (1,'imap://ABC/INBOX'),(2,'imap://ABC/Archive'),(3,'imap://ABC/%5BGmail%5D/All%20Mail');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (205,2,1,1,1787961600,42,1,0);`)
	live, _ := json.Marshal(Message{ID: "205", Account: "Work", Mailbox: "Archive", Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42, RFCMessageID: "<stable@example.com>"})
	script := fmt.Sprintf(`#!/bin/sh
case "$*" in
 *"JSON.stringify({path: trashPathForAccount"*) printf '%%s\n' '{"path":"Archive"}' ;;
 *"JSON.stringify({path: deleteSourcePath"*) printf '%%s\n' '{"path":"INBOX"}' ;;
 *"const box = deleteSourceMailbox(account, 'Archive', true);"*) printf '%%s\n' '%s' ;;
 *) printf 'unexpected alias-aware RFC read' >&2; exit 93 ;;
esac
`, live)
	if err := os.WriteFile(filepath.Join(strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "osascript"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	previous := verificationBackoff
	verificationBackoff = []time.Duration{0}
	t.Cleanup(func() { verificationBackoff = previous })
	identity := snapshotTestIdentity()
	identity.RFCMessageID = "<stable@example.com>"
	got := VerifyMutations(client, BatchOptions{Action: "delete"}, []BatchItem{{ID: "101", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Archive", Identity: identity, Status: "succeeded"}})[0]
	if got.VerifyStatus != "confirmed_destination" {
		t.Fatalf("literal RFC verification=%+v", got)
	}
}

func TestDeletePreflightRejectsArchiveNamedTrashEvenWithAllMail(t *testing.T) {
	f, c := nativeTrashFixture(t)
	f.properties["mbxp:trash"]["pnam"] = appleevents.String("Archive")
	item := BatchItem{Account: "Personal", SourceMailbox: "Archive", ID: "42"}
	if err := DeleteMutator(c, &item); err == nil {
		t.Fatal("actual Trash source was interpreted as All Mail")
	}
	for _, event := range f.events {
		if event.ID != "getd" {
			t.Fatalf("already-Trash preflight wrote: %+v", event)
		}
	}
}
