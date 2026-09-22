package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestValidateDraftAttachments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, `quote", comma.pdf`)
	if err := os.WriteFile(path, []byte("pdf"), 0600); err != nil {
		t.Fatal(err)
	}
	paths, err := ValidateDraftAttachments([]string{path})
	if err != nil || len(paths) != 1 || paths[0] != path {
		t.Fatalf("paths=%v err=%v", paths, err)
	}
	for _, bad := range []string{"", dir, filepath.Join(dir, "missing")} {
		if _, err := ValidateDraftAttachments([]string{bad}); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	script := draftAttachmentScript(paths)
	if !strings.Contains(script, escapeAppleScriptString(path)) || strings.Contains(script, "on error") {
		t.Fatalf("unsafe attachment script: %s", script)
	}
}

func TestDraftAttachmentEnumerationFailsClosed(t *testing.T) {
	for _, result := range []string{"null", "not json"} {
		t.Run(result, func(t *testing.T) {
			t.Setenv("DRAFT_ATTACHMENTS", result)
			writeFakeOsaScript(t, `printf '%s' "$DRAFT_ATTACHMENTS"`)
			if _, err := NewClient().draftAttachments(&Message{ID: "42", Account: "Work", Mailbox: "Drafts"}); err == nil {
				t.Fatal("accepted incomplete enumeration")
			}
		})
	}
}

func TestDraftAttachmentVerificationCountsDuplicates(t *testing.T) {
	makeFile := func(name, content string) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	first := makeFile("same.pdf", "one")
	second := makeFile("same.pdf", "two")
	for _, tc := range []struct {
		name    string
		actual  []string
		wantErr bool
	}{
		{"complete", []string{makeFile("same.pdf", "two"), makeFile("same.pdf", "one")}, false},
		{"missing", []string{first}, true},
		{"same size wrong bytes", []string{first, first}, true},
		{"wrong name", []string{first, makeFile("other.pdf", "two")}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := compareDraftAttachments(tc.actual, []string{first, second})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestExportDraftAttachmentsPreservesDuplicateNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DRAFT_EXPORT", dir)
	writeFakeOsaScript(t, `
case "$*" in
 *"JSON.stringify(msg.mailAttachments()"*) printf '%s' '[{"index":0,"name":"same.pdf","fileSize":3},{"index":1,"name":"same.pdf","fileSize":3}]' ;;
 *"const requestedIndex = 0;"*) printf 'one' > "$DRAFT_EXPORT/0/same.pdf"; printf 'Success' ;;
 *"const requestedIndex = 1;"*) printf 'two' > "$DRAFT_EXPORT/1/same.pdf"; printf 'Success' ;;
 *) exit 1 ;;
esac
`)
	paths, err := NewClient().exportDraftAttachments(&Message{ID: "42", Account: "Work", Mailbox: "Drafts"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("paths=%v", paths)
	}
	for i, want := range []string{"one", "two"} {
		data, err := os.ReadFile(paths[i])
		if err != nil || string(data) != want {
			t.Fatalf("data=%q err=%v", data, err)
		}
	}
}

func TestExportDraftAttachmentsRejectsFailedSaveAndUnsafeName(t *testing.T) {
	for _, tc := range []struct{ name, result string }{
		{"failed save", `[{"index":0,"name":"safe.pdf","fileSize":3}]`},
		{"unsafe name", `[{"index":0,"name":"../escape.pdf","fileSize":3}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DRAFT_ATTACHMENTS", tc.result)
			writeFakeOsaScript(t, `
case "$*" in
 *"JSON.stringify(msg.mailAttachments()"*) printf '%s' "$DRAFT_ATTACHMENTS" ;;
 *) printf 'save failed' >&2; exit 1 ;;
esac
`)
			if _, err := NewClient().exportDraftAttachments(&Message{ID: "42", Account: "Work", Mailbox: "Drafts"}, t.TempDir()); err == nil {
				t.Fatal("accepted unsafe or failed export")
			}
		})
	}
}

func TestDraftAttachmentRejectsFIFOWithoutOpening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidateDraftAttachments([]string{path}); err == nil {
		t.Fatal("accepted FIFO")
	}
}

func TestDraftSaveWaitHonorsCancellation(t *testing.T) {
	writeFakeOsaScript(t, "printf 'ok'\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	_, err := NewClient().WithContext(ctx).CreateDraft(DraftInput{Account: "Work", To: []string{"review@example.test"}, Subject: "Review"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("draft save wait ignored cancellation")
	}
}
