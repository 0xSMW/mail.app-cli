package mail

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ValidateDraftAttachments resolves local paths and rejects missing, unreadable,
// or non-regular files before any draft is changed. Commas are literal in paths.
func ValidateDraftAttachments(paths []string) ([]string, error) {
	var resolved []string
	for _, path := range paths {
		if path == "" {
			return nil, fmt.Errorf("attachment path is empty")
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(absolute)
		if err != nil {
			return nil, fmt.Errorf("attachment %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("attachment %q is not a regular file", path)
		}
		file, err := os.Open(absolute)
		if err != nil {
			return nil, fmt.Errorf("attachment %q: %w", path, err)
		}
		info, err = file.Stat()
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("attachment %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("attachment %q is not a regular file", path)
		}
		resolved = append(resolved, absolute)
	}
	return resolved, nil
}

func draftAttachmentScript(paths []string) string {
	var script strings.Builder
	for i, path := range paths {
		// Do not swallow Mail errors: an incomplete draft is not success.
		fmt.Fprintf(&script, "\ntell content\nmake new attachment with properties {file name:(POSIX file \"%s\")} at after the last paragraph\nend tell\n", escapeAppleScriptString(path))
		fmt.Fprintf(&script, `
repeat 50 times
 if (count attachments of content) >= %d then exit repeat
 delay 0.1
end repeat
if (count attachments of content) < %d then error "Attachment did not finish loading"
`, i+1, i+1)
	}
	return script.String()
}

// Unlike the best-effort public listing, preservation must distinguish an
// empty attachment list from an inaccessible draft or failed property read.
func (c *Client) draftAttachments(draft *Message) ([]Attachment, error) {
	script := fmt.Sprintf(`
const mail = Application('Mail');
const requestedMailbox = '%s';
%s
%s
const acc = mail.accounts.byName('%s');
const mbox = %s;
const msg = messageById(mbox, '%s');
if (msg === null) throw new Error('Draft not found');
JSON.stringify(msg.mailAttachments().map((att, index) => ({index, name: att.name(), fileSize: att.fileSize()})));
`, escapeJSString(draft.Mailbox), jxaMailboxLookupHelper(), jxaMessageByIdHelper(), escapeJSString(draft.Account), jxaMailboxLookupExpression(draft.Mailbox), escapeJSString(draft.ID))
	output, err := c.runJXA(script)
	if err != nil {
		return nil, err
	}
	var attachments []Attachment
	if err := json.Unmarshal([]byte(output), &attachments); err != nil {
		return nil, fmt.Errorf("read draft attachments: %w", err)
	}
	if attachments == nil {
		return nil, fmt.Errorf("draft attachment list is unavailable")
	}
	return attachments, nil
}

func (c *Client) exportDraftAttachments(draft *Message, dir string) ([]string, error) {
	attachments, err := c.draftAttachments(draft)
	if err != nil {
		return nil, err
	}
	var paths []string
	for i, attachment := range attachments {
		name := attachment.Name
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
			return nil, fmt.Errorf("cannot preserve attachment name %q", name)
		}
		// Separate directories preserve filenames even when two attachments have
		// the same name. No existing file is overwritten.
		folder := filepath.Join(dir, fmt.Sprint(i))
		if err := os.Mkdir(folder, 0700); err != nil {
			return nil, err
		}
		path := filepath.Join(folder, name)
		if err := c.SaveAttachmentByIndex(draft.Account, draft.Mailbox, draft.ID, name, attachment.Index, path); err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("attachment %q was not fully saved", name)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (c *Client) verifyDraftAttachments(draft *Message, paths []string) error {
	dir, err := os.MkdirTemp("", "mail-app-cli-verify-attachments-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	actual, err := c.exportDraftAttachments(draft, dir)
	if err != nil {
		return err
	}
	return compareDraftAttachments(actual, paths)
}

// Mail documents fileSize as approximate. Compare exported bytes, not that
// estimate, before replacing a draft containing potentially irreplaceable files.
func compareDraftAttachments(actual, paths []string) error {
	if len(actual) != len(paths) {
		return fmt.Errorf("expected %d attachments, found %d", len(paths), len(actual))
	}
	type identity struct {
		name string
		hash string
	}
	identify := func(path string) (identity, error) {
		file, err := os.Open(path)
		if err != nil {
			return identity{}, err
		}
		defer file.Close()
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			return identity{}, err
		}
		return identity{filepath.Base(path), string(hash.Sum(nil))}, nil
	}
	expected := make(map[identity]int)
	for _, path := range paths {
		key, err := identify(path)
		if err != nil {
			return err
		}
		expected[key]++
	}
	for _, path := range actual {
		key, err := identify(path)
		if err != nil {
			return err
		}
		if expected[key] == 0 {
			return fmt.Errorf("unexpected or incomplete attachment %q", key.name)
		}
		expected[key]--
	}
	return nil
}
