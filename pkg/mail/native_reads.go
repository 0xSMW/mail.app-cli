package mail

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func nativeAccountType(v appleevents.Value) string {
	switch v.Code {
	case "etpo":
		return "pop"
	case "etim":
		return "imap"
	case "etit":
		return "iCloud"
	case "etsm":
		return "smtp"
	}
	return "unknown"
}

// Validate Mail's descriptor types before translating them into the public
// JSON model. A coercion failure must never become an empty successful read.
func nativeRequireFields(fields map[string]appleevents.Value, kinds map[string]string) error {
	for code, kind := range kinds {
		value, ok := fields[code]
		if !ok || value.Kind != kind {
			return fmt.Errorf("Mail returned %q for property %q; expected %s", value.Kind, code, kind)
		}
	}
	return nil
}

func nativeRequireListMessageFields(fields map[string]appleevents.Value) error {
	if err := nativeRequireFields(fields, map[string]string{"ID  ": "int", "subj": "string", "sndr": "string", "rdrc": "date", "drcv": "date", "isrd": "bool", "isfl": "bool", "isdl": "bool"}); err != nil {
		return err
	}
	if fields["ID  "].Number <= 0 {
		return fmt.Errorf("Mail returned an invalid message ID: %d", fields["ID  "].Number)
	}
	return nil
}

func nativeRequireMessageFields(fields map[string]appleevents.Value) error {
	if err := nativeRequireListMessageFields(fields); err != nil {
		return err
	}
	if err := nativeRequireFields(fields, map[string]string{"msze": "int"}); err != nil {
		return err
	}
	if value := fields["meid"]; value.Kind != "string" && value.Kind != "null" {
		return fmt.Errorf("Mail returned %q for message id; expected text or missing value", value.Kind)
	}
	return nil
}

// accountFields returns one account's public property bag. Some Mail account
// subclasses refuse that read, so only explicit unsupported-read errors use
// separate native property reads. Callers validate the fields they require.
func (n *nativeMail) accountFields(ref appleevents.Value) (map[string]appleevents.Value, error) {
	properties, err := n.get(appleevents.Property("pALL", ref))
	if err == nil {
		if properties.Kind != "record" {
			return nil, fmt.Errorf("Mail returned %s for account properties; expected record", properties.Kind)
		}
		return properties.Fields, nil
	}
	if !nativeUnsupportedRead(err) {
		return nil, err
	}
	return n.directFields(ref, "ID  ", "pnam", "emad", "unme", "isac", "atyp")
}

func nativeAccountFromFields(f map[string]appleevents.Value) (Account, error) {
	if err := nativeRequireFields(f, map[string]string{"ID  ": "string", "pnam": "string", "emad": "list", "unme": "string", "isac": "bool", "atyp": "enum"}); err != nil {
		return Account{}, err
	}
	if f["ID  "].Text == "" {
		return Account{}, fmt.Errorf("Mail returned an empty account ID")
	}
	account := Account{ID: f["ID  "].Text, Name: f["pnam"].Text, UserName: f["unme"].Text, Enabled: f["isac"].Boolean, AccountType: nativeAccountType(f["atyp"])}
	for _, address := range f["emad"].Items {
		if address.Kind != "string" {
			return Account{}, fmt.Errorf("Mail returned %s for email address; expected text", address.Kind)
		}
		account.EmailAddresses = append(account.EmailAddresses, address.Text)
	}
	if len(account.EmailAddresses) > 0 {
		account.EmailAddress = account.EmailAddresses[0]
	}
	return account, nil
}

func (n *nativeMail) accounts() ([]Account, error) {
	projection, err := n.projectAccounts(nativeDiscoveryAccounts, "ID  ", "pnam", "emad", "unme", "isac", "atyp")
	if err != nil {
		return nil, err
	}
	result := make([]Account, 0, len(projection.finalIDs))
	seen := make(map[string]bool, len(projection.finalIDs))
	for i := range projection.finalIDs {
		row, err := projection.row(i, seen)
		if err != nil {
			return nil, err
		}
		account, err := nativeAccountFromFields(row)
		if err != nil {
			return nil, err
		}
		if err := projection.claimID(account.ID, seen); err != nil {
			return nil, err
		}
		result = append(result, account)
	}
	return result, nil
}

func (c *Client) nativeAccountByName(name string) (*Account, error) {
	var account Account
	err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		ref, err := n.account(name)
		if err != nil {
			return err
		}
		fields, err := n.accountFields(ref)
		if err != nil {
			return err
		}
		account, err = nativeAccountFromFields(fields)
		if err != nil {
			return err
		}
		if account.Name != name {
			return fmt.Errorf("account name changed during metadata read: expected %q, got %q", name, account.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &account, nil
}

func (c *Client) nativeAccounts() ([]Account, error) {
	var result []Account
	err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error { var err error; result, err = n.accounts(); return err })
	return result, err
}

func (n *nativeMail) mailboxes(account string) ([]Mailbox, error) {
	acc, err := n.account(account)
	if err != nil {
		return nil, err
	}
	refs, err := n.mailboxRefs(acc)
	if err != nil {
		return nil, err
	}
	result := make([]Mailbox, 0, len(refs))
	for _, box := range refs {
		unread, err := n.intProperty("mbuc", box.ref)
		if err != nil {
			return nil, err
		}
		count, err := n.command("core", "cnte", map[string]appleevents.Value{"----": appleevents.All("mssg", box.ref)})
		if err != nil {
			return nil, err
		}
		if count.Kind != "int" {
			return nil, fmt.Errorf("Mail returned %s for message count; expected integer", count.Kind)
		}
		result = append(result, Mailbox{Name: box.path, UnreadCount: int(unread), TotalCount: int(count.Number), Account: account})
	}
	return result, nil
}

func (c *Client) nativeMailboxes(account string) ([]Mailbox, error) {
	var result []Mailbox
	err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error { var err error; result, err = n.mailboxes(account); return err })
	return result, err
}

func nativeMessageFields(f map[string]appleevents.Value, account, mailbox string) *Message {
	id := f["ID  "].Text
	if id == "" {
		id = strconv.FormatInt(f["ID  "].Number, 10)
	}
	return &Message{ID: id, RFCMessageID: f["meid"].Text, Subject: f["subj"].Text, Sender: f["sndr"].Text, DateReceived: f["rdrc"].Text, DateSent: f["drcv"].Text, Read: f["isrd"].Boolean, Flagged: f["isfl"].Boolean, Deleted: f["isdl"].Boolean, MessageSize: int(f["msze"].Number), Account: account, Mailbox: mailbox}
}

var nativeMessageCodes = []string{"ID  ", "meid", "subj", "sndr", "rdrc", "drcv", "isrd", "isfl", "isdl", "msze"}
var nativeListMessageCodes = []string{"ID  ", "subj", "sndr", "rdrc", "drcv", "isrd", "isfl", "isdl"}

type nativeMessageProjection int

const (
	nativeFullMessageProjection nativeMessageProjection = iota
	nativeListMessageProjection
)

func (projection nativeMessageProjection) codes() []string {
	if projection == nativeListMessageProjection {
		return nativeListMessageCodes
	}
	return nativeMessageCodes
}
func (projection nativeMessageProjection) validate(fields map[string]appleevents.Value) error {
	if projection == nativeListMessageProjection {
		return nativeRequireListMessageFields(fields)
	}
	return nativeRequireMessageFields(fields)
}

func (n *nativeMail) messageMetadata(ref appleevents.Value, account, mailbox string) (*Message, error) {
	return n.messageMetadataProjection(ref, account, mailbox, nativeFullMessageProjection)
}

func (n *nativeMail) messageMetadataProjection(ref appleevents.Value, account, mailbox string, projection nativeMessageProjection) (*Message, error) {
	fields, err := n.getFields(ref, projection.codes()...)
	if err != nil {
		return nil, err
	}
	if err := projection.validate(fields); err != nil {
		return nil, err
	}
	if ref.Kind == "object" && ref.Fields["form"].Code == "ID  " && ref.Fields["seld"].Kind == "int" && fields["ID  "].Number != ref.Fields["seld"].Number {
		return nil, fmt.Errorf("message ID changed during metadata read: expected %d, got %d", ref.Fields["seld"].Number, fields["ID  "].Number)
	}
	// The list projection intentionally retains the legacy fallback model:
	// messageSize is zero and RFC identity is omitted until an identity/detail
	// consumer explicitly asks for the full projection.
	return nativeMessageFields(fields, account, mailbox), nil
}

func (c *Client) nativeMessages(account, mailbox string, limit, offset int, unread, flagged, content bool, since string) ([]Message, error) {
	var result = []Message{}
	var sinceTime time.Time
	if strings.TrimSpace(since) != "" {
		unix, _, err := parseSinceUnix(since)
		if err != nil {
			return nil, err
		}
		sinceTime = time.Unix(unix, 0)
	}
	err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		acc, err := n.account(account)
		if err != nil {
			return err
		}
		box, err := n.mailbox(acc, mailbox)
		if err != nil {
			return err
		}
		actualMailbox, err := n.stringProperty("pnam", box)
		if err != nil {
			return err
		}
		// Read IDs and requested filters before pagination. Moderate mailboxes
		// reuse these columns for one envelope projection; larger collections
		// retrieve only the selected exact scoped IDs.
		all := appleevents.All("mssg", box)
		ids, err := n.get(appleevents.Property("ID  ", all))
		if err != nil {
			return err
		}
		if ids.Kind != "list" {
			return fmt.Errorf("Mail returned %s for message IDs; expected list", ids.Kind)
		}
		filterCodes := []string{"isdl"}
		if unread {
			filterCodes = append(filterCodes, "isrd")
		}
		if flagged {
			filterCodes = append(filterCodes, "isfl")
		}
		if !sinceTime.IsZero() {
			filterCodes = append(filterCodes, "rdrc")
		}
		filters := make(map[string]appleevents.Value, len(filterCodes))
		for _, code := range filterCodes {
			column, err := n.get(appleevents.Property(code, all))
			if err != nil {
				return err
			}
			if column.Kind != "list" || len(column.Items) != len(ids.Items) {
				return fmt.Errorf("Mail returned inconsistent %s message filter column", code)
			}
			filters[code] = column
		}

		seen := make(map[int64]bool, len(ids.Items))
		selected := make([]nativeSelectedMessage, 0)
		skipped := 0
		for i, id := range ids.Items {
			if id.Kind != "int" || id.Number <= 0 {
				return fmt.Errorf("message collection changed during filter read")
			}
			if seen[id.Number] {
				return fmt.Errorf("Mail returned duplicate message ID %d", id.Number)
			}
			seen[id.Number] = true
			for _, code := range filterCodes {
				expected := "bool"
				if code == "rdrc" {
					expected = "date"
				}
				if filters[code].Items[i].Kind != expected {
					return fmt.Errorf("Mail returned %s for %s message filter; expected %s", filters[code].Items[i].Kind, code, expected)
				}
			}
			if filters["isdl"].Items[i].Boolean || (unread && filters["isrd"].Items[i].Boolean) || (flagged && !filters["isfl"].Items[i].Boolean) {
				continue
			}
			if !sinceTime.IsZero() {
				date, err := time.Parse(time.RFC3339Nano, filters["rdrc"].Items[i].Text)
				if err != nil {
					return fmt.Errorf("invalid Mail date received: %w", err)
				}
				if date.Before(sinceTime) {
					continue
				}
			}
			if skipped < offset {
				skipped++
				continue
			}
			if limit <= 0 || len(selected) < limit {
				selected = append(selected, nativeSelectedMessage{id: id, index: i + 1})
			}
		}
		messages, err := n.listMessageMetadata(box, account, actualMailbox, ids, filters, selected)
		if err != nil {
			return err
		}
		for i := range messages {
			if content {
				ref, err := n.message(box, messages[i].ID)
				if err != nil {
					return err
				}
				messages[i].Content, err = n.stringProperty("ctnt", ref)
				if err != nil {
					return err
				}
				messages[i].ContentSource = "mail"
				postID, err := n.intProperty("ID  ", ref)
				if err != nil {
					return err
				}
				if postID != selected[i].id.Number {
					return fmt.Errorf("message ID changed during content read")
				}
			}
		}
		result = messages

		return nil
	})
	return result, err
}

func (n *nativeMail) recipients(ref appleevents.Value, class string) ([]string, error) {
	return n.recipientAddresses(ref, class, true)
}

func (c *Client) nativeMessageDetails(account, mailbox, id string, timeout time.Duration, preferDisk, verification bool) (*Message, *PhaseTrace, error) {
	var message *Message
	trace := &PhaseTrace{Phases: []AutomationPhase{}}
	var diskDiagnostics *BodyDiagnostics
	recheckMembership := false
	if preferDisk && !verification {
		started := time.Now()
		cached, handled, err := c.getMessageDetailsFromDisk(account, mailbox, id)
		phase := AutomationPhase{Name: "disk_body", ElapsedMs: time.Since(started).Milliseconds()}
		if err != nil {
			if !isEnvelopeIndexUnavailable(err) {
				return nil, trace, err
			}
			phase.Note = "unavailable: " + err.Error()
		}
		trace.Phases = append(trace.Phases, phase)
		trace.LastCompleted = phase.Name
		if handled {
			return cached, trace, nil
		}
		if cached != nil {
			diskDiagnostics = cached.BodyDiagnostics
		} else {
			recheckMembership = true
		}
	}

	err := c.withNativeAutomation(timeout, func(n *nativeMail) error {
		defer func() {
			trace.Phases = append(trace.Phases, n.phases...)
			trace.Pending = n.phaseName
			for _, p := range trace.Phases {
				if p.Error == "" {
					trace.LastCompleted = p.Name
				}
			}
		}()
		var acc, box, ref appleevents.Value
		found := true
		actualMailbox := mailbox
		if err := n.phase("resolve_account", func() error { var err error; acc, err = n.account(account); return err }); err != nil {
			return err
		}
		if err := n.phase("resolve_mailbox", func() error {
			var err error
			box, err = n.mailbox(acc, mailbox)
			if err != nil {
				return err
			}
			actualMailbox, err = n.stringProperty("pnam", box)
			return err
		}); err != nil {
			return err
		}
		if err := n.phase("lookup_by_id", func() error {
			if recheckMembership {
				// Mail can resolve by-ID specifiers globally, ignoring their
				// mailbox container. Confirm collection membership when the
				// index supplied no scoped envelope, including Gmail labels
				// whose physical owner may be a different mailbox.
				number, err := strconv.ParseInt(id, 10, 64)
				if err != nil || number <= 0 {
					return fmt.Errorf("invalid message ID: %q", id)
				}
				selector := appleevents.ByTest("mssg", appleevents.Comparison("=   ", appleevents.Property("ID  ", appleevents.Examined()), appleevents.Int(number)), box)
				value, err := n.get(appleevents.Property("ID  ", selector))
				if err != nil {
					return err
				}
				ids, err := nativeList(value, "scoped message IDs")
				if err != nil {
					return err
				}
				if len(ids) == 0 {
					found = false
					return nil
				}
				if len(ids) != 1 || ids[0].Kind != "int" || ids[0].Number != number {
					return fmt.Errorf("Mail returned inconsistent scoped message IDs")
				}
			}
			var err error
			ref, err = n.message(box, id)
			if errors.Is(err, ErrNotFound) {
				found = false
				return nil
			}
			return err
		}); err != nil {
			return err
		}
		if !found {
			return nil
		}
		if err := n.phase("metadata", func() error { var err error; message, err = n.messageMetadata(ref, account, actualMailbox); return err }); err != nil {
			return err
		}
		if verification {
			return nil
		}
		if err := n.phase("headers", func() error { return n.messageHeaders(ref, message) }); err != nil {
			return err
		}
		if err := n.phase("recipients", func() error {
			var err error
			message.ToRecipients, err = n.recipients(ref, "trcp")
			if err != nil {
				return err
			}
			message.CcRecipients, err = n.recipients(ref, "crcp")
			if err != nil {
				return err
			}
			message.BccRecipients, err = n.recipients(ref, "brcp")
			return err
		}); err != nil {
			return err
		}
		message.BodyDiagnostics = diskDiagnostics
		trimBodyAlternatives(diskDiagnostics, c.shared.bodyAlternatives)
		return n.phase("content", func() error {
			body, err := n.stringProperty("ctnt", ref)
			if err != nil {
				if nativeContentUnavailable(err) {
					message.ContentError = err.Error()
					message.ContentSource = "mail"
					return nil
				}
				return err
			}
			message.Content = body
			message.ContentSource = "mail"
			return nil
		})
	})
	var timeoutErr *AutomationTimeoutError
	if errors.As(err, &timeoutErr) {
		trace.PendingMs = timeoutErr.Trace.PendingMs
		timeoutErr.Trace = trace
	}
	return message, trace, err
}

func (c *Client) nativeMessageContentBatch(account, mailbox string, ids []string, timeout time.Duration) (map[string]string, error) {
	result := make(map[string]string, len(ids))
	err := c.withNativeAutomation(timeout, func(n *nativeMail) error {
		acc, err := n.account(account)
		if err != nil {
			return err
		}
		box, err := n.mailbox(acc, mailbox)
		if err != nil {
			return err
		}
		for _, id := range ids {
			ref, err := n.message(box, id)
			if err != nil {
				return err
			}
			body, err := n.stringProperty("ctnt", ref)
			if err != nil {
				return err
			}
			result[id] = body
		}
		return nil
	})
	return result, err
}
