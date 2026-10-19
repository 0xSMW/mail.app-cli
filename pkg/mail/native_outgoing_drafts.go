package mail

import (
	"errors"
	"fmt"
	"net/mail"
	"os"
	"strings"

	ae "github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func (n *nativeMail) outgoingDraftMailbox(account ae.Value) (ae.Value, error) {
	id, e := n.stringProperty("ID  ", account)
	if e != nil {
		return ae.Value{}, e
	}
	root, e := n.get(ae.Property("drmb", ae.Null()))
	if e != nil {
		return ae.Value{}, e
	}
	v, e := n.get(ae.All("mbxp", root))
	if e != nil {
		return ae.Value{}, e
	}
	boxes, e := nativeList(v, "Drafts mailboxes")
	if e != nil {
		return ae.Value{}, e
	}
	var found ae.Value
	count := 0
	for _, box := range boxes {
		owner, e := n.get(ae.Property("mact", box))
		if e != nil {
			return ae.Value{}, e
		}
		if owner.Kind == "null" {
			continue
		}
		ownerID, e := n.stringProperty("ID  ", owner)
		if e != nil {
			return ae.Value{}, e
		}
		if ownerID == id {
			found = box
			count++
		}
	}
	if count == 0 {
		return ae.Value{}, notFound("Drafts mailbox", id)
	}
	if count != 1 {
		return ae.Value{}, fmt.Errorf("target account Drafts mailbox cannot be identified uniquely")
	}
	return found, nil
}

// A deleted Mail message can remain addressable in Drafts after deletion.
// Only an explicit false boolean establishes that it is an active draft.
func (n *nativeMail) requireActiveDraft(ref ae.Value) error {
	value, err := n.get(ae.Property("isdl", ref))
	if err != nil {
		return err
	}
	if value.Kind != "bool" {
		return fmt.Errorf("draft deleted status unavailable")
	}
	if value.Boolean {
		return notFound("draft", "")
	}
	return nil
}

func (n *nativeMail) outgoingSnapshotMessage(ref ae.Value, account string, box ae.Value) (*Message, error) {
	if err := n.requireActiveDraft(ref); err != nil {
		return nil, err
	}
	f, e := n.getFields(ref, "ID  ", "subj", "sndr", "ctnt", "mbxp")
	if e != nil {
		return nil, e
	}
	id := outgoingID(f["ID  "])
	if id == "" {
		return nil, fmt.Errorf("draft ID unavailable")
	}
	actualBox := f["mbxp"]
	name, e := n.stringProperty("pnam", actualBox)
	if e != nil {
		return nil, e
	}
	expectedName, e := n.stringProperty("pnam", box)
	if e != nil {
		return nil, e
	}
	if name == "" || name != expectedName {
		return nil, fmt.Errorf("draft mailbox readback mismatch")
	}
	owner, e := n.get(ae.Property("mact", actualBox))
	if e != nil {
		return nil, e
	}
	ownerName, e := n.stringProperty("pnam", owner)
	if e != nil {
		return nil, e
	}
	if ownerName != account {
		return nil, fmt.Errorf("draft account readback mismatch")
	}
	expectedOwner, e := n.get(ae.Property("mact", box))
	if e != nil {
		return nil, e
	}
	ownerID, e := n.stringProperty("ID  ", owner)
	if e != nil {
		return nil, e
	}
	expectedOwnerID, e := n.stringProperty("ID  ", expectedOwner)
	if e != nil {
		return nil, e
	}
	if ownerID == "" || ownerID != expectedOwnerID {
		return nil, fmt.Errorf("draft account ID readback mismatch")
	}

	subject, e := nativeString(f["subj"], "draft subject")
	if e != nil {
		return nil, e
	}
	sender, e := nativeString(f["sndr"], "draft sender")
	if e != nil {
		return nil, e
	}
	content, e := nativeString(f["ctnt"], "draft content")
	if e != nil {
		return nil, e
	}
	m := &Message{ID: id, Account: account, Mailbox: name, Subject: subject, Sender: sender, Content: content}
	recipients := []*[]string{&m.ToRecipients, &m.CcRecipients, &m.BccRecipients}
	for i, class := range []string{"trcp", "crcp", "brcp"} {
		values, e := n.outgoingRecipients(ref, class)
		if e != nil {
			return nil, e
		}
		*recipients[i] = values
	}

	return m, nil
}
func (n *nativeMail) outgoingDraftSnapshot(accountName, draftID string) (*draftSnapshot, ae.Value, error) {
	accounts, e := n.outgoingAccounts()
	if e != nil {
		return nil, ae.Value{}, e
	}
	var snapshot *draftSnapshot
	var found ae.Value
	accountMatches := 0
	for _, account := range accounts {
		name, e := n.stringProperty("pnam", account)
		if e != nil {
			return nil, ae.Value{}, e
		}
		if accountName != "" && name != accountName {
			continue
		}
		accountMatches++
		box, e := n.outgoingDraftMailbox(account)
		if e != nil {
			if errors.Is(e, ErrNotFound) {
				continue
			}
			return nil, ae.Value{}, e
		}
		ref, e := n.message(box, draftID)
		if e != nil {
			if errors.Is(e, ErrNotFound) {
				continue
			}
			return nil, ae.Value{}, e
		}
		m, e := n.outgoingSnapshotMessage(ref, name, box)
		if e != nil {
			if errors.Is(e, ErrNotFound) {
				continue
			}
			return nil, ae.Value{}, e
		}
		if m.ID != draftID {
			return nil, ae.Value{}, fmt.Errorf("draft ID readback mismatch")
		}
		source, e := n.get(ae.Property("raso", ref))
		if e != nil {
			return nil, ae.Value{}, e
		}
		raw, e := nativeString(source, "draft raw source")
		if e != nil {
			return nil, ae.Value{}, e
		}
		if e = reconcileDraftSourceRecipients(m, raw); e != nil {
			return nil, ae.Value{}, e
		}
		if snapshot != nil {
			return nil, ae.Value{}, fmt.Errorf("draft ID is ambiguous; specify an account")
		}
		snapshot = &draftSnapshot{Message: m, Source: raw}
		found = ref
	}
	if accountName != "" && accountMatches > 1 {
		return nil, ae.Value{}, fmt.Errorf("account name must resolve uniquely")
	}
	if snapshot == nil {
		return nil, ae.Value{}, notFound("draft", draftID)
	}
	return snapshot, found, nil
}

// Exchange sometimes exposes empty stored-message recipient collections even
// when the source retains the recipients. Fill only missing collections; a
// nonempty collection must still agree with source during reconstruction.
func reconcileDraftSourceRecipients(message *Message, source string) error {
	if strings.TrimSpace(source) == "" {
		return nil
	}
	parsed, err := mail.ReadMessage(strings.NewReader(source))
	if err != nil {
		return fmt.Errorf("read draft recipient headers: %w", err)
	}
	for _, item := range []struct {
		key       string
		addresses *[]string
	}{{"To", &message.ToRecipients}, {"Cc", &message.CcRecipients}, {"Bcc", &message.BccRecipients}} {
		values := parsed.Header[item.key]
		if len(values) > 1 {
			return fmt.Errorf("draft source has duplicate %s recipient headers", item.key)
		}
		if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
			continue
		}
		list, err := mail.ParseAddressList(values[0])
		if err != nil {
			return fmt.Errorf("read draft %s recipients: %w", item.key, err)
		}
		if len(*item.addresses) == 0 {
			for _, address := range list {
				*item.addresses = append(*item.addresses, address.Address)
			}
		}
	}
	return nil
}

func (c *Client) readNativeDraftSnapshot(accountName, draftID string) (*draftSnapshot, error) {
	var snapshot *draftSnapshot
	err := c.withNativeAutomation(0, func(n *nativeMail) error {
		var e error
		snapshot, _, e = n.outgoingDraftSnapshot(accountName, draftID)
		return e
	})
	return snapshot, err
}
func (c *Client) deleteNativeUnchangedDraft(snapshot *draftSnapshot) error {
	return c.withNativeAutomation(0, func(n *nativeMail) error {
		current, ref, e := n.outgoingDraftSnapshot(snapshot.Message.Account, snapshot.Message.ID)
		if e != nil {
			if errors.Is(e, ErrNotFound) {
				// Accept only an explicitly deleted exact original under freshly
				// resolved account-owned Drafts, never a broad lookup failure.
				account, err := n.account(snapshot.Message.Account)
				if err != nil {
					return err
				}
				box, err := n.outgoingDraftMailbox(account)
				if err != nil {
					return err
				}
				name, err := n.stringProperty("pnam", box)
				if err != nil {
					return err
				}
				if name != snapshot.Message.Mailbox {
					return fmt.Errorf("original draft mailbox identity changed")
				}
				original, err := n.message(box, snapshot.Message.ID)
				if err != nil {
					return err
				}
				if statusErr := n.requireActiveDraft(original); errors.Is(statusErr, ErrNotFound) {
					return nil
				} else if statusErr != nil {
					return statusErr
				}
			}
			return e
		}
		if !sameDraftSnapshot(snapshot, current) {
			return fmt.Errorf("original draft changed after submission; retained")
		}
		if _, e = n.command("core", "delo", map[string]ae.Value{"----": ref}); e != nil {
			return e
		}
		account, e := n.account(snapshot.Message.Account)
		if e != nil {
			return e
		}
		box, e := n.outgoingDraftMailbox(account)
		if e != nil {
			return e
		}
		for i := 0; i < 30; i++ {
			var remaining ae.Value
			remaining, e = n.message(box, snapshot.Message.ID)
			if e == nil {
				e = n.requireActiveDraft(remaining)
			}
			if errors.Is(e, ErrNotFound) {
				return nil
			}
			if e != nil {
				return e
			}
			if e = n.outgoingPause(); e != nil {
				return e
			}
		}
		return fmt.Errorf("original draft removal could not be verified after submission")
	})
}
func (n *nativeMail) scopedDraft(accountName, mailboxName, draftID string) (ae.Value, error) {
	account, e := n.account(accountName)
	if e != nil {
		return ae.Value{}, e
	}
	box, e := n.outgoingDraftMailbox(account)
	if e != nil {
		return ae.Value{}, e
	}
	name, e := n.stringProperty("pnam", box)
	if e != nil {
		return ae.Value{}, e
	}
	if name != mailboxName {
		return ae.Value{}, fmt.Errorf("draft mailbox identity changed")
	}
	ref, e := n.message(box, draftID)
	if e != nil {
		return ae.Value{}, e
	}
	if e = n.requireActiveDraft(ref); e != nil {
		return ae.Value{}, e
	}
	return ref, nil
}

func (c *Client) deleteNativeDraft(accountName, mailboxName, draftID string) error {
	return c.withNativeAutomation(0, func(n *nativeMail) error {
		ref, e := n.scopedDraft(accountName, mailboxName, draftID)
		if e != nil {
			return e
		}
		_, e = n.command("core", "delo", map[string]ae.Value{"----": ref})
		return e
	})
}
func (c *Client) nativeDraftAttachments(draft *Message) ([]Attachment, error) {
	var result []Attachment
	err := c.withNativeAutomation(0, func(n *nativeMail) error {
		ref, e := n.scopedDraft(draft.Account, draft.Mailbox, draft.ID)
		if e != nil {
			return e
		}
		v, e := n.get(ae.All("attc", ref))
		if e != nil {
			return e
		}
		items, e := nativeList(v, "draft attachment list")
		if e != nil {
			return e
		}
		result = make([]Attachment, 0, len(items))
		for i, item := range items {
			f, e := n.getFields(item, "pnam", "atsz")
			if e != nil {
				return e
			}
			name, e := nativeString(f["pnam"], "attachment name")
			if e != nil {
				return e
			}
			if f["atsz"].Kind != "int" {
				return fmt.Errorf("attachment size unavailable")
			}
			result = append(result, Attachment{Index: i, Name: name, FileSize: int(f["atsz"].Number)})
		}
		return nil
	})
	return result, err
}

// Replacement is permitted only after raw MIME proves that the original can
// be reproduced; rich content remains untouched when this proof fails.
func (c *Client) updateNativeDraft(accountName, draftID string, input DraftInput, attachments []string) (*Message, error) {
	snapshot, e := c.readNativeDraftSnapshot(accountName, draftID)
	if e != nil {
		return nil, e
	}
	body, mimeAttachments, e := reconstructibleDraft(snapshot)
	if e != nil {
		return nil, e
	}
	draft := snapshot.Message
	sender, e := draftSenderAddress(draft.Sender)
	if e != nil {
		return nil, e
	}
	replacement := draftReplacement(draft.Account, draft, sender, body, input)
	if replacement.Subject == draft.Subject && replacement.Body == body && len(attachments) == 0 {
		return draft, nil
	}
	dir, e := os.MkdirTemp("", "mail-app-cli-update-draft-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(dir)
	preserved, e := c.exportDraftAttachments(draft, dir)
	if e != nil {
		return nil, e
	}
	if e = compareDraftMIMEAttachments(preserved, mimeAttachments); e != nil {
		return nil, e
	}
	replacement.Attachments = append(preserved, attachments...)
	current, e := c.readNativeDraftSnapshot(draft.Account, draft.ID)
	if e != nil {
		return nil, e
	}
	if !sameDraftSnapshot(snapshot, current) {
		return nil, fmt.Errorf("draft changed while preparing replacement")
	}
	updated, e := c.CreateDraft(replacement)
	if e != nil {
		return updated, e
	}
	if e = c.deleteNativeUnchangedDraft(snapshot); e != nil {
		return updated, fmt.Errorf("created updated draft %s but original draft %s was retained: %w", updated.ID, draft.ID, e)
	}
	return updated, nil
}
