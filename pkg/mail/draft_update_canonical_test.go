package mail

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateDraftLegacyUsesCanonicalBodyAndPreservesMIMEFiles(t *testing.T) {
	for _, leading := range []bool{false, true} {
		t.Run(map[bool]string{false: "wrapper LF is not authored", true: "authored leading blank survives"}[leading], func(t *testing.T) {
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			snapshot := appleRelatedFixture(t)
			canonical := "Plain <line> & symbols.\nUnicode: café 日本語 🚀\n"
			files := []draftRegressionAttachment{{Name: "résumé.bin", Base64: base64.StdEncoding.EncodeToString([]byte{0, 1, 2, 3})}}
			if leading {
				snapshot, canonical = appleLeadingBlankFixture(t)
				files = append(files, draftRegressionAttachment{Name: "second.txt", Base64: base64.StdEncoding.EncodeToString([]byte("second\n"))})
			}
			path := filepath.Join(t.TempDir(), "added.txt")
			if err := os.WriteFile(path, []byte("added bytes"), 0600); err != nil {
				t.Fatal(err)
			}
			statePath := installDraftRegressionBridge(t, snapshot, files, "")
			updated, err := NewClient().UpdateDraft("Work", "42", DraftInput{Subject: "Updated", SubjectSet: true, Attachments: []string{path}})
			state := readDraftRegressionState(t, statePath)
			if err != nil || updated == nil || updated.ID != "700" || state.SavedBody != canonical || !state.OriginalRemoved || strings.Join(state.Deleted, ",") != "special" || state.Sent != 0 {
				t.Fatalf("updated=%+v err=%v state=%+v expectedBody=%q", updated, err, state, canonical)
			}
			want := append(files, draftRegressionAttachment{Name: "added.txt", Base64: base64.StdEncoding.EncodeToString([]byte("added bytes"))})
			if len(state.SavedAttachments) != len(want) {
				t.Fatalf("files=%+v want=%+v", state.SavedAttachments, want)
			}
			for i, file := range want {
				if state.SavedAttachments[i] != file {
					t.Fatalf("file bytes changed: got=%+v want=%+v", state.SavedAttachments, want)
				}
			}
			rendered, ok := attachmentParagraphReadback(canonical)
			if !ok || updated.Content != rendered {
				t.Fatalf("replacement representation drift: got=%q rendered=%q", updated.Content, rendered)
			}
		})
	}
}

func TestUpdateDraftLegacyCanonicalNoOp(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	snapshot := appleRelatedFixture(t)
	statePath := installDraftRegressionBridge(t, snapshot, []draftRegressionAttachment{{Name: "résumé.bin", Base64: "AAECAw=="}}, "")
	draft, err := NewClient().UpdateDraft("Work", "42", DraftInput{})
	state := readDraftRegressionState(t, statePath)
	if err != nil || draft == nil || draft.ID != "42" || state.Composed != 0 || len(state.Exported) != 0 || len(state.Deleted) != 0 {
		t.Fatalf("draft=%+v err=%v state=%+v", draft, err, state)
	}
}

func TestUpdateDraftLegacyRejectsUnprovenOrChangingOriginal(t *testing.T) {
	for _, name := range []string{"unsupported HTML", "body drift", "mismatched-export", "changed-before-replacement", "changed-original-during-replacement"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			snapshot := appleRelatedFixture(t)
			specialState := name
			switch name {
			case "unsupported HTML":
				snapshot.Source = strings.Replace(snapshot.Source, "font: 12.0px Helvetica", "font: 13.0px Helvetica", 1)
			case "body drift":
				snapshot.Message.Content += "different"
			}
			statePath := installDraftRegressionBridge(t, snapshot, []draftRegressionAttachment{{Name: "résumé.bin", Base64: "AAECAw=="}}, specialState)
			updated, err := NewClient().UpdateDraft("Work", "42", DraftInput{Subject: "Updated", SubjectSet: true})
			state := readDraftRegressionState(t, statePath)
			if err == nil || state.OriginalRemoved || len(state.Deleted) != 0 || state.Sent != 0 {
				t.Fatalf("unsafe update: updated=%+v err=%v state=%+v", updated, err, state)
			}
			if name == "changed-original-during-replacement" {
				if updated == nil || updated.ID != "700" || state.Composed != 1 {
					t.Fatalf("verified replacement lost: updated=%+v state=%+v", updated, state)
				}
			} else if updated != nil || state.Composed != 0 {
				t.Fatalf("composed before preservation proven: updated=%+v state=%+v", updated, state)
			}
		})
	}
}

func TestDraftLegacyGuardedCleanupStrictIdentityAndLogicalRemoval(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{
		{"cleanup-tombstone", true}, {"cleanup-getter-absence", true}, {"cleanup-getter-present", false}, {"cleanup-account-change", false}, {"cleanup-malformed-ids", false}, {"cleanup-duplicate-ids", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			snapshot := draftRegressionSnapshot(t, nil)
			statePath := installDraftRegressionBridge(t, snapshot, nil, tc.name)
			err := NewClient().deleteUnchangedDraft(snapshot)
			state := readDraftRegressionState(t, statePath)
			if (err == nil) != tc.ok || strings.Join(state.Deleted, ",") != "special" || state.Sent != 0 {
				t.Fatalf("err=%v state=%+v", err, state)
			}
		})
	}
}

func TestDraftLegacySnapshotRequiresActiveBooleanState(t *testing.T) {
	for _, stateName := range []string{"inactive-original", "invalid-deleted-status", "unreadable-deleted-status"} {
		t.Run(stateName, func(t *testing.T) {
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			statePath := installDraftRegressionBridge(t, draftRegressionSnapshot(t, nil), nil, stateName)
			snapshot, err := NewClient().readDraftSnapshot("Work", "42")
			state := readDraftRegressionState(t, statePath)
			if err == nil || snapshot != nil || state.Composed != 0 || len(state.Deleted) != 0 {
				t.Fatalf("snapshot=%+v err=%v state=%+v", snapshot, err, state)
			}
		})
	}
}
