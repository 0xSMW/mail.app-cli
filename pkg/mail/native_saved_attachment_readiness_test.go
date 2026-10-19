package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func TestNativeSavedAttachmentReadinessReconcilesExchangeIDs(t *testing.T) {
	for _, mode := range []string{"stable", "reassigned", "tombstone", "transient-property", "ambiguous", "late-ambiguous", "wrong-owner", "final-owner-failure", "payload-mismatch", "input-drift", "source-drift", "fingerprint-drift", "malformed", "persistent-missing"} {
		t.Run(mode, func(t *testing.T) {
			f := newOutgoingDescriptorFixture()
			c := fixtureOutgoingClient(f)
			input := fixtureOutgoingInput()
			path := filepath.Join(t.TempDir(), "検証 résumé.bin")
			original := []byte{0, 0xff, 13, 10, 42}
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			input.Attachments = []string{path}
			// Persistent readiness errors remain failures and respect cancellation.
			ctx, cancel := context.WithTimeout(context.Background(), 450*time.Millisecond)
			defer cancel()
			c = c.WithContext(ctx)
			rolled, rawReads, saves := false, 0, 0
			var get func(ae.Value) (ae.Value, error)
			get = func(v ae.Value) (ae.Value, error) {
				if v.Kind == "list" {
					items := make([]ae.Value, len(v.Items))
					for i, item := range v.Items {
						value, err := get(item)
						if err != nil {
							return ae.Value{}, err
						}
						items[i] = value
					}
					return ae.List(items...), nil
				}
				prop, container := descriptorProperty(v), v.Fields["from"]
				if f.saved && mode == "wrong-owner" && prop == "mact" && container.Text == "outgoing-fixture-drafts" {
					return ae.Null(), nil
				}
				if f.saved && mode == "final-owner-failure" && rawReads >= 2 && prop == "mact" && container.Text == "outgoing-fixture-drafts" {
					return ae.Value{}, &ae.Error{Code: -1743, Message: "owner read denied"}
				}
				if f.saved && descriptorClass(container) == "mssg" {
					exact := container.Fields["form"].Code == "ID  "
					id := container.Fields["seld"].Number
					if prop == "ID  " && !exact {
						if mode == "ambiguous" || mode == "tombstone" || (mode == "late-ambiguous" && rawReads > 0) {
							return ae.List(ae.Int(900), ae.Int(901)), nil
						}
						if rolled {
							return ae.List(ae.Int(901)), nil
						}
						return ae.List(ae.Int(900)), nil
					}
					if exact && rolled && id == 900 {
						return ae.Value{}, &ae.Error{Code: -1728, Message: "old Exchange ID removed"}
					}
					if exact && prop == "isdl" && mode == "tombstone" && id == 900 {
						return ae.Bool(true), nil
					}
					if exact && prop == "isdl" && mode == "malformed" {
						return ae.String("false"), nil
					}
					if exact && prop == "raso" {
						rawReads++
						if mode == "persistent-missing" || (mode == "transient-property" && rawReads == 1) {
							return ae.Value{}, &ae.Error{Code: -1728, Message: "raw MIME not published"}
						}
						if mode == "reassigned" && !rolled {
							rolled = true
							return ae.Value{}, &ae.Error{Code: -1728, Message: "old Exchange ID removed"}
						}
						if mode == "source-drift" && rawReads > 1 {
							return ae.String(f.savedSource + "\n"), nil
						}
						if mode == "payload-mismatch" {
							return ae.String(attachmentProofMIME([]string{filepath.Base(path)}, [][]byte{[]byte("wrong")})), nil
						}
						if mode == "input-drift" {
							if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
								return ae.Value{}, err
							}
						}
						if mode == "fingerprint-drift" {
							f.fields["subj"] = ae.String("edited after save")
						}
					}
				}
				return f.get(v)
			}
			c.shared.nativeSender = func(eventCtx context.Context, event ae.Event) (ae.Value, ae.Stats, error) {
				if event.Class+event.ID == "coregetd" {
					f.events = append(f.events, event)
					value, err := get(event.Params["----"])
					return value, ae.Stats{Events: 1, Attempted: true}, err
				}
				if event.Class+event.ID == "coresave" {
					saves++
				}
				return f.send(eventCtx, event)
			}
			result, err := c.composeOutgoing(input, "save")
			success := mode == "stable" || mode == "reassigned" || mode == "tombstone" || mode == "transient-property"
			if success {
				wantID := "900"
				if mode == "reassigned" || mode == "tombstone" {
					wantID = "901"
				}
				if err != nil || result.Draft == nil || result.Draft.ID != wantID || result.Status != "saved" {
					t.Fatalf("result=%+v error=%v want ID %s", result, err, wantID)
				}
			} else {
				var mutation *MutationError
				if !errors.As(err, &mutation) || !mutation.Saved || !mutation.Uncertain || mutation.Phase != "attachment_verification" {
					t.Fatalf("result=%+v error=%v want saved uncertain attachment failure", result, err)
				}
			}
			closed := false
			for _, event := range f.events {
				if closed && (event.Class != "core" || event.ID != "getd") {
					t.Fatalf("readiness dispatched mutation %s/%s", event.Class, event.ID)
				}
				if event.Class+event.ID == "coreclos" {
					closed = true
				}
			}
			if saves != 1 || f.closeCalls != 1 || f.sendCalls != 0 || f.draftDeletes != 0 {
				t.Fatalf("mutation replay: saves=%d closes=%d sends=%d deletes=%d", saves, f.closeCalls, f.sendCalls, f.draftDeletes)
			}
		})
	}
}
