package mail

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func attachmentFixturePath(root string) string {
	return filepath.Join(root, "ABC", "[Gmail].mbox", "All Mail.mbox", "UUID", "Data", "Messages", "101.emlx")
}

func attachmentMIME(parts ...string) string {
	message := "Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: text/plain\n\nbody\n"
	for _, part := range parts {
		message += "--b\n" + part + "\n"
	}
	return message + "--b--\n"
}

func TestLocalAttachmentMetadataAndExactPayloadSave(t *testing.T) {
	c, root, events := localReadFixture(t)
	payload := []byte("héllo 😀\n")
	image := []byte{0, 1, 2, 3}
	message := attachmentMIME(
		"Content-Type: application/octet-stream\nContent-Disposition: attachment; filename*=utf-8''caf%C3%A9%20%F0%9F%98%80.txt\nContent-Transfer-Encoding: base64\n\n"+base64.StdEncoding.EncodeToString(payload),
		"Content-Type: image/png; name=\"tiny.png\"\nContent-Disposition: inline; filename=\"tiny.png\"\nContent-Transfer-Encoding: base64\n\n"+base64.StdEncoding.EncodeToString(image),
	)
	writeEMLX(t, attachmentFixturePath(root), message)
	attachments, handled, err := c.getAttachmentsFromDisk("Work", "INBOX", "101")
	if err != nil || !handled || len(attachments) != 2 {
		t.Fatalf("attachments=%#v %v %v", attachments, handled, err)
	}
	if attachments[0].Index != 0 || attachments[0].Name != "café 😀.txt" || attachments[0].FileSize != len(payload) || attachments[0].MimeType != "application/octet-stream" || attachments[1].Index != 1 || attachments[1].Name != "tiny.png" || attachments[1].FileSize != len(image) || attachments[1].MimeType != "image/png" {
		t.Fatalf("metadata=%#v", attachments)
	}
	outputDir := t.TempDir()
	destination := filepath.Join(outputDir, "café 😀.txt")
	saved, handled, err := c.saveAttachmentFromDisk("Work", "INBOX", "101", "café 😀.txt", destination)
	if err != nil || !handled || saved != destination {
		t.Fatalf("save=%q %v %v", saved, handled, err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("saved payload=%q %v", got, err)
	}
	// A later save must preserve an existing destination's exact bytes.
	if _, handled, err := c.saveAttachmentFromDisk("Work", "INBOX", "101", "café 😀.txt", destination); !handled || err == nil {
		t.Fatalf("overwrite accepted=%v %v", handled, err)
	}
	got, err = os.ReadFile(destination)
	if err != nil || string(got) != string(payload) {
		t.Fatal("overwrite changed existing payload")
	}
	indexedDestination := filepath.Join(outputDir, "selected.png")
	if _, handled, err := c.saveAttachmentFromDiskByIndex("Work", "INBOX", "101", 1, indexedDestination); err != nil || !handled {
		t.Fatalf("index save=%v %v", handled, err)
	}
	got, err = os.ReadFile(indexedDestination)
	if err != nil || string(got) != string(image) {
		t.Fatalf("indexed payload=%v %v", got, err)
	}
	missing := filepath.Join(outputDir, "mismatch")
	if _, handled, err := c.saveAttachmentFromDiskSelection("Work", "INBOX", "101", "expected-other-name", 1, missing); !handled || err == nil {
		t.Fatalf("name mismatch=%v %v", handled, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("name mismatch published a file")
	}
	if events.Load() != 0 {
		t.Fatalf("cached attachment operation sent %d events", events.Load())
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".mail-attachment-") {
			t.Fatalf("staging file left behind: %s", entry.Name())
		}
	}
}

func TestLocalAttachmentDuplicateNamesUseExactMIMEIndex(t *testing.T) {
	c, root, events := localReadFixture(t)
	part := func(value string) string {
		return "Content-Type: text/plain\nContent-Disposition: attachment; filename=\"same.txt\"\n\n" + value
	}
	writeEMLX(t, attachmentFixturePath(root), attachmentMIME(part("first"), part("second")))
	attachments, handled, err := c.getAttachmentsFromDisk("Work", "INBOX", "101")
	if err != nil || !handled || len(attachments) != 2 || attachments[0].Index != 0 || attachments[1].Index != 1 {
		t.Fatalf("duplicate list=%#v %v %v", attachments, handled, err)
	}
	destination := filepath.Join(t.TempDir(), "second.txt")
	if _, handled, err := c.saveAttachmentFromDiskByIndex("Work", "INBOX", "101", 1, destination); err != nil || !handled {
		t.Fatalf("duplicate index save=%v %v", handled, err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "second" {
		t.Fatalf("duplicate index selected %q %v", got, err)
	}
	ambiguous := filepath.Join(t.TempDir(), "ambiguous.txt")
	if _, handled, err := c.saveAttachmentFromDisk("Work", "INBOX", "101", "same.txt", ambiguous); !handled || err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous name=%v %v", handled, err)
	}
	if _, err := os.Stat(ambiguous); !os.IsNotExist(err) {
		t.Fatal("ambiguous save published a file")
	}
	if events.Load() != 0 {
		t.Fatal("local index save called Mail")
	}
}

func TestLocalAttachmentIncompleteScopeAndDestinationGuards(t *testing.T) {
	c, root, events := localReadFixture(t)
	for _, message := range []string{
		"Content-Type: multipart/mixed; boundary=b\n\n--b\nContent-Type: application/octet-stream\nContent-Disposition: attachment; filename=\"a.bin\"\n\ntruncated",
		attachmentMIME("Content-Type: application/octet-stream\nContent-Disposition: attachment; filename=\"a.bin\"\nContent-Transfer-Encoding: base64\n\ninvalid!"),
		attachmentMIME("Content-Type: application/octet-stream\nContent-Disposition: attachment; filename=\"a.bin\"\nX-Apple-Content-Length: 100\n\n"),
	} {
		writeEMLX(t, attachmentFixturePath(root), message)
		if attachments, handled, err := c.getAttachmentsFromDisk("Work", "INBOX", "101"); err != nil || handled || attachments != nil {
			t.Fatalf("incomplete attachments=%#v %v %v", attachments, handled, err)
		}
		destination := filepath.Join(t.TempDir(), "a.bin")
		if _, handled, err := c.saveAttachmentFromDisk("Work", "INBOX", "101", "a.bin", destination); err != nil || handled {
			t.Fatalf("incomplete save=%v %v", handled, err)
		}
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatal("incomplete save published a file")
		}
		entries, err := os.ReadDir(filepath.Dir(destination))
		if err != nil || len(entries) != 0 {
			t.Fatalf("incomplete save left staging files=%v %v", entries, err)
		}
	}
	for _, id := range []string{"103", "104", "105"} {
		if _, handled, err := c.getAttachmentsFromDisk("Work", "INBOX", id); err != nil || handled {
			t.Fatalf("partial/missing %s=%v %v", id, handled, err)
		}
	}
	if attachments, handled, err := c.getAttachmentsFromDisk("Work", "INBOX", "102"); err != nil || !handled || len(attachments) != 0 {
		t.Fatalf("wrong physical account=%#v %v %v", attachments, handled, err)
	}
	writeEMLX(t, attachmentFixturePath(root), attachmentMIME("Content-Type: text/plain\nContent-Disposition: attachment; filename=\"../../unsafe.txt\"\n\npayload"))
	destination := filepath.Join(t.TempDir(), "safe-output")
	if _, handled, err := c.saveAttachmentFromDiskByIndex("Work", "INBOX", "101", 0, destination); !handled || err == nil {
		t.Fatalf("unsafe filename=%v %v", handled, err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("unsafe filename published a file")
	}
	if events.Load() != 0 {
		t.Fatal("disk helper performed automation")
	}
}
