package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

// These descriptors cover both the special Drafts read and the stable ordinary
// mailbox lookup used by native attachment saving. File bytes come exclusively
// from this injected transport; neither Mail nor an index can be consulted.
type nativeDraftExportFixture struct {
	draft   *nativeDraftDescriptorFixture
	name    string
	payload []byte
	events  []ae.Event
	saves   int
}

func newNativeDraftExportFixture() *nativeDraftExportFixture {
	return &nativeDraftExportFixture{draft: newNativeDraftDescriptorFixture(), name: "résumé 世界.bin", payload: []byte{0, 1, 2, 255, 10, 42}}
}
func (f *nativeDraftExportFixture) get(v ae.Value) (ae.Value, error) {
	if v.Kind == "list" {
		items := make([]ae.Value, len(v.Items))
		for i, ref := range v.Items {
			value, e := f.get(ref)
			if e != nil {
				return ae.Value{}, e
			}
			items[i] = value
		}
		return ae.List(items...), nil
	}
	class, prop, container := descriptorClass(v), descriptorProperty(v), v.Fields["from"]
	if container.Text == "account" && prop == "pALL" {
		return ae.Record(map[string]ae.Value{"ID  ": ae.String("account-id"), "pnam": ae.String("Personal"), "emad": stringsDescriptors([]string{"alias@example.com"}), "isac": ae.Bool(true), "unme": ae.String("fixture-user"), "atyp": ae.Enum("etim")}), nil
	}
	if class == "mbxp" && v.Fields["seld"].Kind == "int" && v.Fields["seld"].Number == 1 && container.Text == "account" {
		return ruleFixtureRef("draftbox"), nil
	}
	if prop == "pnam" && descriptorClass(container) == "mbxp" && container.Fields["seld"].Kind == "ordinal" {
		parent := container.Fields["from"]
		if parent.Text == "account" {
			return ae.List(ae.String("Drafts")), nil
		}
		if descriptorClass(parent) == "mbxp" && parent.Fields["from"].Text == "account" {
			return ae.List(ae.List()), nil
		}
		return ae.List(), nil
	}
	if descriptorClass(container) == "mbxp" && container.Fields["seld"].Kind == "int" && container.Fields["seld"].Number == 1 && container.Fields["from"].Text == "account" {
		switch prop {
		case "pnam":
			return ae.String("Drafts"), nil
		case "mact":
			return ruleFixtureRef("account"), nil
		case "mbxc":
			return ae.Null(), nil
		}
	}
	if prop == "mbxc" && container.Text == "draftbox" {
		return ae.Null(), nil
	}
	if class == "attc" && descriptorClass(container) == "mssg" {
		return ae.List(ruleFixtureRef("attachment")), nil
	}
	if container.Text == "attachment" {
		switch prop {
		case "pnam":
			return ae.String(f.name), nil
		case "atsz":
			return ae.Int(int64(len(f.payload) + 123)), nil
		case "attp":
			return ae.String("application/octet-stream"), nil
		}
	}
	return f.draft.get(v)
}
func (f *nativeDraftExportFixture) send(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
	f.events = append(f.events, event)
	stats := ae.Stats{Events: 1, Attempted: true}
	if event.Class+event.ID == "coregetd" {
		v, e := f.get(event.Params["----"])
		return v, stats, e
	}
	if event.Class+event.ID == "coresave" {
		if event.Params["----"].Text != "attachment" || event.Params["kfil"].Kind != "file" {
			return ae.Value{}, stats, fmt.Errorf("save did not target the selected native attachment and FileURL")
		}
		f.saves++
		return ae.Null(), stats, os.WriteFile(event.Params["kfil"].Text, f.payload, 0600)
	}
	return ae.Value{}, stats, fmt.Errorf("unexpected draft export event %s/%s", event.Class, event.ID)
}
func nativeDraftExportClient(t *testing.T, f *nativeDraftExportFixture) (*Client, string) {
	t.Helper()
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	bin := t.TempDir()
	marker := filepath.Join(bin, "script-was-invoked")
	// A regression can only hit this harmless spy; it can never invoke real Mail.
	script := "#!/bin/sh\nprintf 'invoked\\n' > \"$MAIL_APP_CLI_TEST_SCRIPT_MARKER\"\nexit 97\n"
	if e := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("MAIL_APP_CLI_TEST_SCRIPT_MARKER", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := NewClient()
	c.shared.backend = BackendNative
	c.shared.nativeSender = f.send
	c.shared.warn = func(string) {}
	return c, marker
}
func TestNativeDescriptorDraftExportPrivateHelperUsesNativeSaveAndVerifiesBytes(t *testing.T) {
	f := newNativeDraftExportFixture()
	c, marker := nativeDraftExportClient(t, f)
	draft := &Message{ID: "15", Account: "Personal", Mailbox: "Drafts"}
	before := BackendStatistics()
	exported, e := c.exportDraftAttachments(draft, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	if len(exported) != 1 || filepath.Base(exported[0]) != f.name || f.saves != 1 {
		t.Fatalf("exported=%v nativeSaves=%d", exported, f.saves)
	}
	data, e := os.ReadFile(exported[0])
	if e != nil || !bytes.Equal(data, f.payload) {
		t.Fatalf("exported bytes differ: %v %v", data, e)
	}
	expected := filepath.Join(t.TempDir(), f.name)
	if e = os.WriteFile(expected, f.payload, 0600); e != nil {
		t.Fatal(e)
	}
	if e = c.verifyDraftAttachments(draft, []string{expected}); e != nil {
		t.Fatalf("native SHA verification failed: %v", e)
	}
	// Same filename and byte count with one changed byte must fail verification.
	f.payload[2] ^= 0x40
	if e = c.verifyDraftAttachments(draft, []string{expected}); e == nil {
		t.Fatal("same-size corrupted attachment passed native verification")
	}
	if f.saves != 3 {
		t.Fatalf("expected three native saves through the private helper, got %d", f.saves)
	}
	after := BackendStatistics()
	if after.LegacyProcesses != before.LegacyProcesses || after.LegacySQLiteProcesses != before.LegacySQLiteProcesses {
		t.Fatalf("export launched legacy processes: before=%+v after=%+v", before, after)
	}
	if _, e = os.Stat(marker); !os.IsNotExist(e) {
		t.Fatalf("script spy was invoked: %v", e)
	}
	for _, event := range f.events {
		if event.Class != "core" || (event.ID != "getd" && event.ID != "save") {
			t.Fatalf("unexpected export mutation: %s/%s", event.Class, event.ID)
		}
	}
}

func TestNativeDescriptorSpecialDraftOrdinaryLookupRejectsTombstone(t *testing.T) {
	for _, operation := range []string{"list", "save"} {
		t.Run(operation, func(t *testing.T) {
			f := newNativeDraftExportFixture()
			f.draft.deleted = true
			c, _ := nativeDraftExportClient(t, f)
			var err error
			if operation == "list" {
				_, err = c.nativeAttachments("Personal", "Drafts", "15")
			} else {
				err = c.nativeSaveAttachment("Personal", "Drafts", "15", f.name, -1, filepath.Join(t.TempDir(), "export.bin"))
			}
			if !errors.Is(err, ErrNotFound) || f.saves != 0 {
				t.Fatalf("deleted special draft accepted: err=%v saves=%d", err, f.saves)
			}
			for _, event := range f.events {
				if event.ID != "getd" {
					t.Fatalf("tombstone caused mutation: %+v", event)
				}
			}
		})
	}
}

func TestNativeDescriptorAttachmentOrdinarySameLeafRetainsPriority(t *testing.T) {
	f := newNativeDraftExportFixture()
	f.draft.deleted = true
	n := &nativeMail{ctx: context.Background(), sender: func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
		ref := event.Params["----"]
		container := ref.Fields["from"]
		if event.Class+event.ID == "coregetd" {
			// Resolve the ordinary indexed mailbox to its own stable reference,
			// not the separate account-owned special Drafts reference.
			if descriptorClass(ref) == "mbxp" && ref.Fields["seld"].Kind == "int" {
				return ruleFixtureRef("ordinary-draftbox"), ae.Stats{}, nil
			}
			if container.Text == "ordinary-draftbox" {
				switch descriptorProperty(ref) {
				case "pnam":
					return ae.String("Drafts"), ae.Stats{}, nil
				case "mact":
					return ruleFixtureRef("account"), ae.Stats{}, nil
				case "mbxc":
					return ruleFixtureRef("ordinary-parent"), ae.Stats{}, nil
				}
			}
			if descriptorProperty(ref) == "mbxc" && descriptorClass(container) == "mbxp" && container.Fields["seld"].Kind == "int" {
				return ruleFixtureRef("ordinary-parent"), ae.Stats{}, nil
			}
			if container.Text == "ordinary-parent" {
				switch descriptorProperty(ref) {
				case "pnam":
					return ae.String("Archive"), ae.Stats{}, nil
				case "mbxc":
					return ae.Null(), ae.Stats{}, nil
				}
			}
		}
		return f.send(ctx, event)
	}}
	if _, err := n.attachmentMessage("Personal", "Drafts", "15"); err != nil {
		t.Fatalf("distinct Archive/Drafts ordinary lookup lost priority: %v", err)
	}
	statusReads := 0
	for _, event := range f.events {
		if descriptorProperty(event.Params["----"]) == "isdl" {
			statusReads++
		}
	}
	if statusReads != 1 {
		t.Fatalf("ordinary lookup applied additional special-Drafts guard: status reads=%d", statusReads)
	}
}

func TestNativeDescriptorActiveInboxAttachmentsDoNotResolveDrafts(t *testing.T) {
	for _, brokenDrafts := range []string{"unavailable", "ambiguous"} {
		t.Run(brokenDrafts, func(t *testing.T) {
			f := newNativeDraftExportFixture()
			draftsReads := 0
			n := &nativeMail{ctx: context.Background(), sender: func(ctx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				ref := event.Params["----"]
				if event.Class+event.ID == "coregetd" && descriptorProperty(ref) == "drmb" {
					draftsReads++
					return ae.Value{}, ae.Stats{}, fmt.Errorf("Drafts %s", brokenDrafts)
				}
				value, stats, err := f.send(ctx, event)
				if err == nil && event.Class+event.ID == "coregetd" && descriptorProperty(ref) == "pnam" {
					if value.Kind == "string" && value.Text == "Drafts" {
						value = ae.String("Inbox")
					}
					if value.Kind == "list" && len(value.Items) == 1 && value.Items[0].Kind == "string" && value.Items[0].Text == "Drafts" {
						value = ae.List(ae.String("Inbox"))
					}
				}
				return value, stats, err
			}}
			if _, err := n.attachmentMessage("Personal", "Inbox", "15"); err != nil {
				t.Fatal(err)
			}
			if draftsReads != 0 {
				t.Fatalf("active Inbox depended on Drafts: reads=%d", draftsReads)
			}
		})
	}
}
