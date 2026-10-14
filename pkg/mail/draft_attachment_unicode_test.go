package mail

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDraftAttachmentCanonicalFilename(t *testing.T) {
	expected := filepath.Join(t.TempDir(), "café.txt")
	actual := filepath.Join(t.TempDir(), "cafe\u0301.txt")
	for _, path := range []string{expected, actual} {
		if err := os.WriteFile(path, []byte("same bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := compareDraftAttachments([]string{actual}, []string{expected}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actual, []byte("wrong bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := compareDraftAttachments([]string{actual}, []string{expected}); err == nil {
		t.Fatal("accepted changed attachment bytes")
	}
}
