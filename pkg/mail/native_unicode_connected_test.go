package mail

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"github.com/0xSMW/mail.app-cli/v3/internal/mailindex"
)

// Exercise the handoff used by ID-based commands: real Envelope Index queries
// produce the scope passed to the native action. Only the Apple Events transport
// is replaced; account lookup, mailbox resolution and mutation construction run.
func TestNativeUnicodeLocatedMessageMutation(t *testing.T) {
	if !mailindex.Available() {
		t.Skip("embedded SQLite unavailable")
	}
	for _, tc := range []struct{ name, indexed, live string }{
		{"NFD index NFC Mail", "Entwu\u0308rfe", "Entwürfe"},
		{"NFC index NFD Mail", "Entwürfe", "Entwu\u0308rfe"},
	} {
		for _, nested := range []bool{false, true} {
			for _, ambiguous := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/nested=%t/ambiguous=%t", tc.name, nested, ambiguous), func(t *testing.T) {
					indexPath := tc.indexed
					if nested {
						indexPath = "Projets e\u0301te\u0301/" + indexPath
					}
					data := fmt.Sprintf(`
alter table messages add column date_received integer default 0;
alter table messages add column flagged integer default 0;
insert into mailboxes(rowid,url) values (1,%s),(2,%s);
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted)
values (41,1,1,1,1787961600,42,0,0),(42,2,1,1,1787961600,42,0,0);
`, sqlQuote("imap://b/"+escapeMailboxURLPath(indexPath)), sqlQuote("imap://a/"+escapeMailboxURLPath(indexPath)))
					_, _ = snapshotSQLiteFixture(t, data)
					t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", os.Getenv("SNAPSHOT_SQLITE_DATABASE"))
					t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))

					f := newNativeReadFixture()
					owner := f.accounts[0]
					f.properties["mbxp:inbox"]["pnam"] = appleevents.String(tc.live)
					boxes := []appleevents.Value{fixtureRef("mbxp", "inbox")}
					if ambiguous {
						boxes = append(boxes, fixtureRef("mbxp", "collision"))
						f.properties["mbxp:collision"] = map[string]appleevents.Value{
							"pnam": appleevents.String(tc.indexed), "mact": owner,
						}
					}
					if nested {
						parent := fixtureRef("mbxp", "archive")
						f.properties["mbxp:archive"]["pnam"] = appleevents.String("Projets été")
						f.boxes["mact:a"] = []appleevents.Value{parent}
						f.boxes["mbxp:archive"] = boxes
						for _, box := range boxes {
							f.properties[f.key(box)]["mbxc"] = parent
						}
					} else {
						f.boxes["mact:a"] = boxes
					}
					// Put a second account first, with the same mailbox name. Neither
					// enumeration order nor a matching name may select its mailbox.
					other := fixtureRef("mact", "b")
					f.accounts = append([]appleevents.Value{other}, f.accounts...)
					f.properties["mact:b"] = make(map[string]appleevents.Value)
					for key, value := range f.properties["mact:a"] {
						f.properties["mact:b"][key] = value
					}
					f.properties["mact:b"]["ID  "] = appleevents.String("b")
					f.properties["mact:b"]["pnam"] = appleevents.String("Other account")
					f.boxes["mact:b"] = []appleevents.Value{fixtureRef("mbxp", "other")}
					f.properties["mbxp:other"] = map[string]appleevents.Value{
						"pnam": appleevents.String(tc.live), "mact": other,
					}

					// Use a fresh client so accounts are also read through the native
					// transport, rather than the SQLite helper's primed account cache.
					c := NewClient()
					c.shared.backend = BackendNative
					c.shared.nativeSender = f.sender
					location, err := c.LocateMessage("42")
					if err != nil {
						t.Fatal(err)
					}
					if location.ID != "42" || location.Account != "Personal" || location.Mailbox != tc.indexed {
						t.Fatalf("index returned the wrong identity or changed spelling: %+v", location)
					}
					err = c.MarkMessageAsRead(location.Account, location.Mailbox, location.ID, true)
					if ambiguous {
						if err == nil || !strings.Contains(err.Error(), "ambiguous") {
							t.Errorf("ambiguous located mailbox must reject the mutation: %v", err)
						}
					} else if err != nil {
						t.Fatal(err)
					}

					writes := 0
					for _, event := range f.events {
						if event.ID == "getd" || event.ID == "cnte" {
							continue
						}
						writes++
						if ambiguous {
							t.Fatalf("ambiguous located mailbox dispatched a write: %+v", event)
						}
						box := appleevents.Value{Kind: "opaque", Data: "mbxp:inbox"}
						want := appleevents.Property("isrd", appleevents.ByID("mssg", appleevents.Int(42), box))
						if event.Class != "core" || event.ID != "setd" || !reflect.DeepEqual(event.Params["----"], want) || !reflect.DeepEqual(event.Params["data"], appleevents.Bool(true)) {
							t.Fatalf("mutation targeted the wrong mailbox, message or property: %+v", event)
						}
						if !reflect.DeepEqual(f.properties[f.key(box)]["mact"], owner) {
							t.Fatal("mutation targeted a mailbox outside the indexed account")
						}
					}
					if !ambiguous && writes != 1 {
						t.Fatalf("got %d writes, want exactly one mark-read", writes)
					}
				})
			}
		}
	}
}
