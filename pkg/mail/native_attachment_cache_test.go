package mail

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeCachedAttachmentSelectionRoutes(t *testing.T) {
	c, root, events := localReadFixture(t)
	writeEMLX(t, attachmentFixturePath(root), attachmentMIME(
		"Content-Type: text/plain\nContent-Disposition: attachment; filename=\"first.txt\"\n\nfirst",
		"Content-Type: text/plain\nContent-Disposition: attachment; filename=\"second.txt\"\n\nsecond",
	))
	for _, selection := range []struct {
		name      string
		index     int
		payload   string
		errorText string
	}{
		{"first.txt", -1, "first", ""},
		{"second.txt", 1, "second", ""},
		{"wrong.txt", 1, "", `expected "wrong.txt"`},
		{"first.txt", -2, "", "invalid attachment index: -2"},
	} {
		t.Run(selection.name+selection.errorText, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "attachment.txt")
			err := c.SaveAttachmentByIndex("Work", "INBOX", "101", selection.name, selection.index, path)
			if selection.errorText != "" {
				if err == nil || !strings.Contains(err.Error(), selection.errorText) {
					t.Fatalf("save error = %v, want %q", err, selection.errorText)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("failed selection published a file: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(path)
			if err != nil || string(payload) != selection.payload {
				t.Fatalf("saved payload = %q, %v", payload, err)
			}
		})
	}
	if events.Load() != 0 {
		t.Fatalf("cached saves sent %d Mail events", events.Load())
	}
}
