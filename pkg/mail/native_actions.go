package mail

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func (c *Client) nativeMessageAction(account, mailbox, id, property string, value appleevents.Value) error {
	return c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		var ref appleevents.Value
		if err := n.phase("resolve_message", func() error { var err error; ref, err = n.scopedMessage(account, mailbox, id); return err }); err != nil {
			return err
		}
		if property == "delete" {
			return n.phase("trash", func() error {
				_, err := n.command("core", "delo", map[string]appleevents.Value{"----": ref})
				return err
			})
		}
		phase := "mark_read"
		if property == "isfl" {
			phase = "flag"
		}
		return n.phase(phase, func() error { return n.set(appleevents.Property(property, ref), value) })
	})
}

func (c *Client) nativeMove(account, mailbox, id, destination string, archive bool) error {
	return c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		acc, err := n.account(account)
		if err != nil {
			return err
		}
		source, err := n.mailbox(acc, mailbox)
		if err != nil {
			return err
		}
		var dest appleevents.Value
		if archive {
			dest, err = n.mailboxExact(acc, destination)
		} else {
			dest, err = n.mailbox(acc, destination)
		}
		if err != nil {
			return err
		}
		if archive && mailbox == destination {
			return nil
		}
		ref, err := n.message(source, id)
		if err != nil {
			return err
		}
		if _, err = n.command("core", "move", map[string]appleevents.Value{"----": ref, "insh": dest}); err != nil {
			return err
		}
		if archive {
			actual, err := n.stringProperty("pnam", dest)
			if err != nil {
				return err
			}
			if actual != destination {
				return fmt.Errorf("archive destination readback did not match %q", destination)
			}
		}
		return nil
	})
}

func (c *Client) nativeResolveArchive(account string) (string, error) {
	var destination string
	err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		acc, err := n.account(account)
		if err != nil {
			return err
		}
		for _, name := range []string{"All Mail", "Archive"} {
			box, err := n.mailboxExact(acc, name)
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					continue
				}
				return err
			}
			actual, err := n.stringProperty("pnam", box)
			if err != nil {
				return err
			}
			if actual != name {
				return fmt.Errorf("archive destination resolution returned an invalid mailbox: %q", actual)
			}
			destination = actual
			return nil
		}
		return notFound("archive mailbox", "")
	})
	return destination, err
}

func (n *nativeMail) attachments(ref appleevents.Value) ([]Attachment, []appleevents.Value, error) {
	refs, err := n.collection("attc", ref)
	if err != nil {
		return nil, nil, err
	}
	result := make([]Attachment, 0, len(refs))
	for i, attachment := range refs {
		f, err := n.getFields(attachment, "pnam", "atsz")
		if err != nil {
			return nil, nil, err
		}
		if err := nativeRequireFields(f, map[string]string{"pnam": "string", "atsz": "int"}); err != nil {
			return nil, nil, err
		}
		mime, err := n.stringProperty("attp", attachment)
		if err != nil {
			// Mail can reject this optional MIME getter even when the other
			// attachment properties are available. Keep this exception local.
			if !nativeOptionalProperty(err) && !nativeAppleError(err, -10000) {
				return nil, nil, err
			}
			mime = "unknown"
		}
		if mime == "" {
			mime = "unknown"
		}
		result = append(result, Attachment{Index: i, Name: f["pnam"].Text, FileSize: int(f["atsz"].Number), MimeType: mime})
	}
	return result, refs, nil
}

func (c *Client) nativeAttachments(account, mailbox, id string) ([]Attachment, error) {
	if attachments, handled, err := c.getAttachmentsFromDisk(account, mailbox, id); err != nil {
		if !isEnvelopeIndexUnavailable(err) {
			return nil, err
		}
	} else if handled {
		return attachments, nil
	}
	var result []Attachment
	err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		ref, err := n.scopedMessage(account, mailbox, id)
		if err != nil {
			var missing *NotFoundError
			if errors.As(err, &missing) && missing.Kind == "message" {
				result = []Attachment{}
				return nil
			}
			return err
		}
		result, _, err = n.attachments(ref)
		return err
	})
	return result, err
}

func (c *Client) nativeSaveAttachment(account, mailbox, id, name string, index int, path string) error {
	if strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0 {
		return fmt.Errorf("attachment save path must be non-empty and contain no NUL byte")
	}
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve attachment save path: %w", err)
	}
	path = resolvedPath
	if info, err := os.Stat(filepath.Dir(path)); err != nil || !info.IsDir() {
		return fmt.Errorf("attachment destination directory does not exist: %s", filepath.Dir(path))
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("attachment destination already exists: %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if index < -1 {
		return fmt.Errorf("invalid attachment index: %d", index)
	}
	// Cached listing and saving share one MIME traversal and verify the
	// expected filename before publishing the exact selected payload.
	if _, handled, err := c.saveLocalAttachment(account, mailbox, id, name, index, path); err != nil {
		if !isEnvelopeIndexUnavailable(err) {
			return err
		}
	} else if handled {
		return nil
	}
	return c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		ref, err := n.scopedMessage(account, mailbox, id)
		if err != nil {
			return err
		}
		attachments, refs, err := n.attachments(ref)
		if err != nil {
			return err
		}
		selected := -1
		if index >= 0 {
			if index >= len(refs) {
				return notFound("attachment", fmt.Sprint(index))
			}
			if name != "" && attachments[index].Name != name {
				return fmt.Errorf("attachment index %d has name %q, expected %q", index, attachments[index].Name, name)
			}
			selected = index
		} else {
			for i, attachment := range attachments {
				if attachment.Name == name {
					if selected >= 0 {
						return fmt.Errorf("attachment name is ambiguous: %s; select its index", name)
					}
					selected = i
				}
			}
		}
		if selected < 0 {
			return notFound("attachment", name)
		}
		return n.phase("save_attachment", func() error { return n.saveAttachmentExclusive(refs[selected], path) })
	})
}

// Mail saves into a private, newly-created staging directory. Publication is
// a same-directory hard link so an existing target or racing symlink is never
// overwritten, matching the local MIME route's destination contract.
func (n *nativeMail) saveAttachmentExclusive(ref appleevents.Value, destination string) error {
	parent := filepath.Dir(destination)
	stageDir, err := os.MkdirTemp(parent, ".mail-attachment-*")
	if err != nil {
		return err
	}
	if !filepath.IsAbs(stageDir) || filepath.Dir(stageDir) != parent || !strings.HasPrefix(filepath.Base(stageDir), ".mail-attachment-") {
		return fmt.Errorf("invalid attachment staging directory")
	}
	stageFile := filepath.Join(stageDir, "payload")
	defer func() {
		// Only the exact absolute paths created for this operation are removed.
		if info, err := os.Lstat(stageFile); err == nil && !info.IsDir() && filepath.IsAbs(stageFile) && filepath.Dir(stageFile) == stageDir {
			_ = os.Remove(stageFile)
		}
		if info, err := os.Lstat(stageDir); err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && filepath.IsAbs(stageDir) && filepath.Dir(stageDir) == parent && strings.HasPrefix(filepath.Base(stageDir), ".mail-attachment-") {
			_ = os.Remove(stageDir)
		}
	}()
	if _, err := n.command("core", "save", map[string]appleevents.Value{"----": ref, "kfil": appleevents.File(stageFile)}); err != nil {
		return err
	}
	info, err := os.Lstat(stageFile)
	if err != nil {
		return fmt.Errorf("Mail did not save attachment payload: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Mail attachment payload is not a regular file")
	}
	if err := os.Link(stageFile, destination); err != nil {
		return fmt.Errorf("publish attachment without overwrite: %w", err)
	}
	return nil
}

func (c *Client) nativeSync(account string, all bool) error {
	return c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		var refs []appleevents.Value
		if all {
			var err error
			refs, err = n.collection("mact", appleevents.Null())
			if err != nil {
				return err
			}
		} else {
			ref, err := n.account(account)
			if err != nil {
				return err
			}
			refs = []appleevents.Value{ref}
		}
		for _, ref := range refs {
			f, err := n.getFields(ref, "atyp", "isac")
			if err != nil {
				return err
			}
			if err := nativeRequireFields(f, map[string]string{"atyp": "enum", "isac": "bool"}); err != nil {
				return err
			}
			if all && !f["isac"].Boolean {
				continue
			}
			typ := f["atyp"].Code
			if typ == "etim" || typ == "etit" {
				if _, err = n.command("emal", "syac", map[string]appleevents.Value{"acna": ref}); err != nil {
					return err
				}
			}
			if _, err = n.command("emal", "chma", map[string]appleevents.Value{"acna": ref}); err != nil {
				return err
			}
		}
		return nil
	})
}
