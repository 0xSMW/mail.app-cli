package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDraftAttachmentDryRun(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "review, one.pdf")
	second := filepath.Join(dir, "review two.pdf")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("test"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"drafts", "create", "-a", "Work", "--to", "review@example.test"},
		{"drafts", "update", "123", "-a", "Work"},
	} {
		code, stdout, stderr := run(t, append(args, "--dry-run", "--attach", first, "--attach", second, "--json")...)
		if code != 0 {
			t.Fatalf("exit=%d: %s %s", code, stdout, stderr)
		}
		var envelope struct {
			Data struct {
				Draft struct {
					Attachments []string `json:"attachments"`
				} `json:"draft"`
				Updates struct {
					Attachments []string `json:"attachments"`
				} `json:"updates"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
			t.Fatal(err)
		}
		paths := envelope.Data.Draft.Attachments
		if args[1] == "update" {
			paths = envelope.Data.Updates.Attachments
		}
		if len(paths) != 2 || paths[0] != first || paths[1] != second {
			t.Fatalf("attachments = %#v", paths)
		}
	}
	// Repeated command execution must not retain attachments from a prior call.
	code, stdout, stderr := run(t, "drafts", "update", "123", "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("exit=%d: %s %s", code, stdout, stderr)
	}
	var envelope struct {
		Data struct {
			Updates struct {
				Attachments []string `json:"attachments"`
			} `json:"updates"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Updates.Attachments) != 0 {
		t.Fatal("stale attachments leaked into next command")
	}
}

func TestDraftAttachmentInvalidFileFailsDuringPreview(t *testing.T) {
	for _, verb := range []string{"create", "update"} {
		args := []string{"drafts", verb}
		if verb == "update" {
			args = append(args, "123")
		}
		code, _, _ := run(t, append(args, "-a", "Work", "--dry-run", "--attach", filepath.Join(t.TempDir(), "missing.pdf"), "--json")...)
		if code == 0 {
			t.Fatalf("%s accepted missing attachment", verb)
		}
	}
}
