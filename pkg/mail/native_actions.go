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

// attachmentMessage preserves the explicit mailbox lookup first, then checks
// the selected account's special Drafts mailbox for a freshly stored exact ID.
// A differently named mailbox never admits this fallback.
func (n *nativeMail) attachmentMessage(account, mailbox, id string) (appleevents.Value, error) {
	ref, err := n.scopedMessage(account, mailbox, id)
	if err == nil {
		// An explicitly active ordinary message cannot be a deleted Drafts
		// tombstone and must not depend on special-Drafts discovery.
		status, statusErr := n.get(appleevents.Property("isdl", ref))
		if statusErr == nil && status.Kind == "bool" && !status.Boolean {
			return ref, nil
		}
		// Ordinary mailbox resolution retains priority. Only its full owner
		// and ancestor identity can establish that it is special Drafts.
		acc, e := n.account(account)
		if e != nil {
			return appleevents.Value{}, e
		}
		special, e := n.outgoingDraftMailbox(acc)
		if IsNotFound(e) {
			return ref, nil
		}
		if e != nil {
			return appleevents.Value{}, e
		}
		accountID, e := n.stringProperty("ID  ", acc)
		if e != nil {
			return appleevents.Value{}, e
		}
		actualBox := ref.Fields["from"]
		actualName, e := n.stringProperty("pnam", actualBox)
		if e != nil {
			return appleevents.Value{}, e
		}
		specialName, e := n.stringProperty("pnam", special)
		if e != nil {
			return appleevents.Value{}, e
		}
		if actualName != specialName {
			return ref, nil
		}
		actualIdentity, _, e := n.canonicalMailbox(nativeMailboxRef{ref: actualBox, name: actualName}, accountID)
		if e != nil {
			return appleevents.Value{}, e
		}
		specialIdentity, _, e := n.canonicalMailbox(nativeMailboxRef{ref: special, name: specialName}, accountID)
		if e != nil {
			return appleevents.Value{}, e
		}
		if actualIdentity == specialIdentity {
			if e = n.requireActiveDraft(ref); e != nil {
				return appleevents.Value{}, e
			}
		}
		return ref, nil
	}
	var missing *NotFoundError
	if !errors.As(err, &missing) || (missing.Kind != "message" && missing.Kind != "mailbox") {
		return appleevents.Value{}, err
	}
	acc, lookupErr := n.account(account)
	if lookupErr != nil {
		return appleevents.Value{}, lookupErr
	}
	box, lookupErr := n.outgoingDraftMailbox(acc)
	if lookupErr != nil {
		if IsNotFound(lookupErr) {
			return appleevents.Value{}, err
		}
		return appleevents.Value{}, lookupErr
	}
	name, lookupErr := n.stringProperty("pnam", box)
	if lookupErr != nil {
		return appleevents.Value{}, lookupErr
	}
	if name != mailbox {
		return appleevents.Value{}, err
	}
	ref, lookupErr = n.message(box, id)
	if lookupErr != nil {
		return appleevents.Value{}, lookupErr
	}
	if lookupErr = n.requireActiveDraft(ref); lookupErr != nil {
		return appleevents.Value{}, lookupErr
	}
	return ref, nil
}

func (c *Client) nativeAttachments(account, mailbox, id string) ([]Attachment, error) {
	if attachments, handled, err := c.getAttachmentsFromDisk(account, mailbox, id); err != nil {
		if !isEnvelopeIndexUnavailable(err) {
			return nil, err
		}
	} else if handled {
		if len(attachments) > 0 {
			return attachments, nil
		}
		// An empty, completely cached message is authoritative. A missing index
		// row can lag behind a newly saved Mail draft and must be rechecked.
		_, absent, scopeErr := c.scopedLocalMessageFile(account, mailbox, id)
		if scopeErr != nil {
			return nil, scopeErr
		}
		if !absent {
			return attachments, nil
		}
	}
	var result []Attachment
	err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		ref, err := n.attachmentMessage(account, mailbox, id)
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
	return c.nativeSaveAttachmentScoped(account, mailbox, id, name, index, path, false)
}

func (c *Client) nativeDraftSaveAttachment(account, mailbox, id, name string, index int, path string) error {
	return c.nativeSaveAttachmentScoped(account, mailbox, id, name, index, path, true)
}

func (c *Client) nativeSaveAttachmentScoped(account, mailbox, id, name string, index int, path string, draft bool) error {
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
	// Preservation always resolves the account-owned special Drafts mailbox;
	// an ordinary named mailbox or stale cache cannot establish that identity.
	if !draft {
		if _, handled, err := c.saveLocalAttachment(account, mailbox, id, name, index, path); err != nil {
			var missing *NotFoundError
			staleAbsence := errors.As(err, &missing) && missing.Kind == "message"
			if !isEnvelopeIndexUnavailable(err) && !staleAbsence {
				return err
			}
		} else if handled {
			return nil
		}
	}

	return c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		var ref appleevents.Value
		var err error
		if draft {
			ref, err = n.scopedDraft(account, mailbox, id)
		} else {
			ref, err = n.attachmentMessage(account, mailbox, id)
		}
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
			if name != "" && !attachmentNamesEqual(attachments[index].Name, name) {
				return fmt.Errorf("attachment index %d has name %q, expected %q", index, attachments[index].Name, name)
			}
			selected = index
		} else {
			for i, attachment := range attachments {
				if attachmentNamesEqual(attachment.Name, name) {
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
