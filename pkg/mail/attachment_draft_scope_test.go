package mail

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

// The ordinary account mailbox and global special Drafts root intentionally
// expose different message collections, as Exchange can do after saving.
type attachmentDraftScopeFixture struct {
	base          *nativeDraftExportFixture
	ordinaryName  string
	wrongOwner    bool
	ordinaryReads int
	specialReads  int
}

func (f *attachmentDraftScopeFixture) get(v ae.Value) (ae.Value, error) {
	if v.Kind == "list" {
		items := make([]ae.Value, len(v.Items))
		for i, item := range v.Items {
			value, err := f.get(item)
			if err != nil {
				return ae.Value{}, err
			}
			items[i] = value
		}
		return ae.List(items...), nil
	}
	prop, container := descriptorProperty(v), v.Fields["from"]
	ordinaryName := f.ordinaryName
	if ordinaryName == "" {
		ordinaryName = "Drafts"
	}
	if prop == "pnam" && descriptorClass(container) == "mbxp" && container.Fields["seld"].Kind == "ordinal" && container.Fields["from"].Text == "account" {
		return ae.List(ae.String(ordinaryName)), nil
	}

	if prop == "pnam" && descriptorClass(container) == "mbxp" && container.Fields["seld"].Kind == "int" && container.Fields["from"].Text == "account" {
		return ae.String(ordinaryName), nil
	}
	if descriptorClass(v) == "mbxp" && v.Fields["seld"].Kind == "int" && container.Text == "account" {
		return ruleFixtureRef("ordinarybox"), nil
	}
	if container.Text == "ordinarybox" {
		switch prop {
		case "pnam":
			return ae.String(ordinaryName), nil
		case "mact":
			return ruleFixtureRef("account"), nil
		case "mbxc":
			return ae.Null(), nil
		}
	}

	if prop == "ID  " && descriptorClass(container) == "mssg" {
		box := container.Fields["from"]
		if box.Text != "draftbox" {
			f.ordinaryReads++
			return ae.Value{}, &ae.Error{Code: -1728, Message: "not in ordinary named Drafts"}
		}
		f.specialReads++
	}
	if f.wrongOwner && prop == "ID  " && container.Text == "account" {
		return ae.String("selected-id"), nil
	}
	if f.wrongOwner && prop == "mact" && container.Text == "draftbox" {
		return ruleFixtureRef("other-account"), nil
	}
	if prop == "ID  " && container.Text == "other-account" {
		return ae.String("other-id"), nil
	}
	return f.base.get(v)
}
func (f *attachmentDraftScopeFixture) send(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
	if event.Class+event.ID == "coregetd" {
		f.base.events = append(f.base.events, event)
		v, err := f.get(event.Params["----"])
		return v, ae.Stats{Events: 1, Attempted: true}, err
	}
	return f.base.send(ctx, event)
}
func attachmentScopeClient(t *testing.T, f *attachmentDraftScopeFixture) *Client {
	c, _ := nativeDraftExportClient(t, f.base)
	c.shared.nativeSender = f.send
	return c
}
func TestAttachmentDraftSpecialScopeExportAndPublicSelection(t *testing.T) {
	for _, action := range []string{"export", "list", "save-name", "save-index"} {
		t.Run(action, func(t *testing.T) {
			f := &attachmentDraftScopeFixture{base: newNativeDraftExportFixture()}
			c := attachmentScopeClient(t, f)
			target := filepath.Join(t.TempDir(), "saved.bin")
			switch action {
			case "export":
				paths, err := c.exportDraftAttachments(&Message{Account: "Personal", Mailbox: "Drafts", ID: "15"}, t.TempDir())
				if err != nil || len(paths) != 1 {
					t.Fatalf("export=%v err=%v", paths, err)
				}
				target = paths[0]
			case "list":
				items, err := c.GetAttachmentsJSON("Personal", "Drafts", "15")
				if err != nil || len(items) != 1 || items[0].Name != f.base.name {
					t.Fatalf("attachments=%+v err=%v", items, err)
				}
			default:
				index := -1
				if action == "save-index" {
					index = 0
				}
				if err := c.SaveAttachmentByIndex("Personal", "Drafts", "15", f.base.name, index, target); err != nil {
					t.Fatal(err)
				}
			}
			if action != "list" {
				got, err := os.ReadFile(target)
				if err != nil || !bytes.Equal(got, f.base.payload) {
					t.Fatalf("payload=%v err=%v", got, err)
				}
			}
			if f.specialReads == 0 {
				t.Fatal("special Drafts exact ID was not verified")
			}
			if action == "export" && f.ordinaryReads != 0 {
				t.Fatal("preservation consulted ordinary named Drafts")
			}
			if action != "export" && f.ordinaryReads == 0 {
				t.Fatal("public lookup skipped requested ordinary mailbox")
			}
			for _, event := range f.base.events {
				if event.ID != "getd" && event.ID != "save" {
					t.Fatalf("unexpected mutation: %s", event.ID)
				}
			}
		})
	}
}
func TestAttachmentDraftSpecialScopeRejectsOtherAccount(t *testing.T) {
	f := &attachmentDraftScopeFixture{base: newNativeDraftExportFixture(), wrongOwner: true}
	c := attachmentScopeClient(t, f)
	target := filepath.Join(t.TempDir(), "forbidden.bin")
	err := c.SaveAttachmentByIndex("Personal", "Drafts", "15", f.base.name, 0, target)
	if err == nil {
		t.Fatal("saved another account's special draft")
	}
	if f.base.saves != 0 || f.specialReads != 0 {
		t.Fatalf("cross-account reads=%d saves=%d err=%v", f.specialReads, f.base.saves, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal(fmt.Errorf("rejected selection published target: %v", err))
	}
}

func TestAttachmentDraftStaleIndexAbsenceRechecksSpecialScope(t *testing.T) {
	c, _, _ := localReadFixture(t)
	c.shared.accounts = []Account{{ID: "ABC", Name: "Personal"}}
	indexPath := os.Getenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH")
	if output, err := exec.Command("sqlite3", indexPath, "insert into mailboxes(ROWID,url,total_count,unread_count) values (5,'imap://ABC/Drafts',0,0);").CombinedOutput(); err != nil {
		t.Fatalf("fixture index: %s %v", output, err)
	}
	f := &attachmentDraftScopeFixture{base: newNativeDraftExportFixture()}
	c.shared.nativeSender = f.send
	items, handled, err := c.getAttachmentsFromDisk("Personal", "Drafts", "15")
	if err != nil || !handled || len(items) != 0 {
		t.Fatalf("cache fixture did not report absence: %v %v %v", items, handled, err)
	}
	items, err = c.GetAttachmentsJSON("Personal", "Drafts", "15")
	if err != nil || len(items) != 1 {
		t.Fatalf("stale index hid draft attachments: %v %v", items, err)
	}
	path := filepath.Join(t.TempDir(), "saved.bin")
	if err := c.SaveAttachmentByIndex("Personal", "Drafts", "15", f.base.name, 0, path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, f.base.payload) {
		t.Fatalf("payload=%v err=%v", got, err)
	}
}

func TestAttachmentDraftSpecialScopeRejectsOtherMailbox(t *testing.T) {
	f := &attachmentDraftScopeFixture{base: newNativeDraftExportFixture(), ordinaryName: "Custom"}
	c := attachmentScopeClient(t, f)
	items, err := c.GetAttachmentsJSON("Personal", "Custom", "15")
	if err != nil || len(items) != 0 {
		t.Fatalf("other mailbox listing=%v err=%v", items, err)
	}
	if err := c.SaveAttachmentByIndex("Personal", "Custom", "15", f.base.name, 0, filepath.Join(t.TempDir(), "forbidden.bin")); !IsNotFound(err) {
		t.Fatalf("other mailbox save=%v", err)
	}
	if f.base.saves != 0 || f.specialReads != 0 {
		t.Fatalf("other mailbox selected special draft: reads=%d saves=%d", f.specialReads, f.base.saves)
	}
}
