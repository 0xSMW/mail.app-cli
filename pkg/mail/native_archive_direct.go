package mail

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"github.com/0xSMW/mail.app-cli/v3/internal/mailax"
)

func (c *Client) archiveAX() mailax.Driver {
	if c.shared.nativeAX != nil {
		return c.shared.nativeAX
	}
	return mailax.New()
}

func gmailArchiveHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "imap.gmail.com" || host == "imap.googlemail.com"
}

func (n *nativeMail) gmailArchiveAccount(name string) (bool, error) {
	account, err := n.account(name)
	if err != nil {
		return false, err
	}
	host, err := n.get(appleevents.Property("host", account))
	if err != nil {
		return false, err
	}
	if host.Kind != "string" {
		return false, fmt.Errorf("native archive account server name has an invalid type")
	}
	return gmailArchiveHost(host.Text), nil
}

type nativeArchiveIdentity struct {
	ID              int64
	Subject, Sender string
	Sent            time.Time
	Size            int64
}

func (n *nativeMail) archiveIdentity(message appleevents.Value) (nativeArchiveIdentity, error) {
	f, err := n.getFields(message, "ID  ", "subj", "sndr", "drcv", "msze")
	if err != nil {
		return nativeArchiveIdentity{}, err
	}
	if f["ID  "].Kind != "int" || f["ID  "].Number <= 0 || f["subj"].Kind != "string" || f["sndr"].Kind != "string" || f["drcv"].Kind != "date" || f["msze"].Kind != "int" || f["msze"].Number < 0 {
		return nativeArchiveIdentity{}, fmt.Errorf("native archive message identity is incomplete")
	}
	sent, err := time.Parse(time.RFC3339Nano, f["drcv"].Text)
	if err != nil {
		return nativeArchiveIdentity{}, fmt.Errorf("native archive message date is invalid: %w", err)
	}
	return nativeArchiveIdentity{ID: f["ID  "].Number, Subject: f["subj"].Text, Sender: f["sndr"].Text, Sent: sent.UTC(), Size: f["msze"].Number}, nil
}

// archiveSelection validates the independently selected AX row using Mail's
// exact message identity and selected mailbox/account, with preview disabled.
func (n *nativeMail) archiveSelection(viewer appleevents.Value, accountID string, expected nativeArchiveIdentity) (bool, error) {
	preview, err := n.get(appleevents.Property("mvpv", viewer))
	if err != nil {
		return false, err
	}
	if preview.Kind != "bool" || preview.Boolean {
		return false, fmt.Errorf("Mail preview became visible or could not be verified")
	}
	boxes, err := n.get(appleevents.Property("msbx", viewer))
	if err != nil {
		return false, err
	}
	if boxes.Kind != "list" || len(boxes.Items) != 1 {
		return false, fmt.Errorf("Mail source selection changed")
	}
	name, err := n.get(appleevents.Property("pnam", boxes.Items[0]))
	if err != nil {
		return false, err
	}
	if name.Kind != "string" || !strings.EqualFold(name.Text, "INBOX") {
		return false, fmt.Errorf("Mail source selection changed")
	}
	selectedAccount, err := n.get(appleevents.Property("mact", boxes.Items[0]))
	if err != nil {
		return false, err
	}
	id, err := n.get(appleevents.Property("ID  ", selectedAccount))
	if err != nil {
		return false, err
	}
	if id.Kind != "string" || id.Text != accountID {
		return false, fmt.Errorf("Mail source account selection changed")
	}
	selected, err := n.get(appleevents.Property("smgs", viewer))
	if err != nil {
		return false, err
	}
	if selected.Kind != "list" {
		return false, fmt.Errorf("Mail selected messages has an invalid type")
	}
	if len(selected.Items) != 1 {
		return false, nil
	}
	actual, err := n.archiveIdentity(selected.Items[0])
	if err != nil {
		return false, err
	}
	return actual == expected, nil
}

func (c *Client) runNativeArchiveDirect(account, messageID string, apply bool) error {
	if id, err := strconv.ParseUint(messageID, 10, 53); err != nil || id == 0 {
		return fmt.Errorf("invalid native archive message ID: %q", messageID)
	}
	return c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error { return n.archiveInboxDirect(account, messageID, apply) })
}

func (n *nativeMail) archiveViewerIDs() (map[int64]bool, error) {
	viewers, err := n.collection("mvwr", appleevents.Null())
	if err != nil {
		return nil, err
	}
	ids := make(map[int64]bool, len(viewers))
	for _, viewer := range viewers {
		id, err := n.get(appleevents.Property("ID  ", viewer))
		if err != nil {
			return nil, err
		}
		if id.Kind != "int" || id.Number <= 0 || ids[id.Number] {
			return nil, fmt.Errorf("Mail returned ambiguous viewer ownership")
		}
		ids[id.Number] = true
	}
	return ids, nil
}

// archiveInboxDirect executes within the native operation's existing gates.
// Its cleanup is bounded and ignores cancellation, so an interrupted operation
// cannot strand a known owned viewer or accidentally replay an uncertain press.
func (n *nativeMail) archiveInboxDirect(accountName, messageID string, apply bool) (err error) {
	if id, e := strconv.ParseUint(messageID, 10, 53); e != nil || id == 0 {
		return fmt.Errorf("invalid native archive message ID: %q", messageID)
	}
	driver := n.client.archiveAX()
	if err = driver.Trusted(); err != nil {
		return err
	}
	account, err := n.account(accountName)
	if err != nil {
		return err
	}
	host, err := n.get(appleevents.Property("host", account))
	if err != nil {
		return err
	}
	if host.Kind != "string" || !gmailArchiveHost(host.Text) {
		return fmt.Errorf("account is no longer configured for Gmail archive")
	}
	accountID, err := n.get(appleevents.Property("ID  ", account))
	if err != nil {
		return err
	}
	if accountID.Kind != "string" || accountID.Text == "" {
		return fmt.Errorf("native archive account identity is incomplete")
	}
	source, err := n.mailbox(account, "INBOX")
	if err != nil {
		return err
	}
	message, err := n.message(source, messageID)
	if err != nil {
		return err
	}
	expected, err := n.archiveIdentity(message)
	if err != nil {
		return err
	}
	existing, err := n.archiveViewerIDs()
	if err != nil {
		return err
	}
	previous, err := driver.FrontApplication()
	if err != nil {
		return err
	}
	if previous == "" {
		return fmt.Errorf("cannot identify the previous foreground application")
	}
	if err = n.ctx.Err(); err != nil {
		return err
	}
	created, err := n.command("core", "crel", map[string]appleevents.Value{
		"kocl": appleevents.TypeCode("mvwr"), "prdt": appleevents.Record(map[string]appleevents.Value{"mvpv": appleevents.Bool(false)}),
	})
	if err != nil {
		var ae *appleevents.Error
		if errors.As(err, &ae) && !ae.Attempted {
			return err
		}
		return &NativeArchiveError{fmt.Errorf("native archive viewer creation outcome is uncertain; inspect Mail before retrying: %w", err)}
	}
	if created.Kind != "object" && created.Kind != "opaque" {
		return &NativeArchiveError{fmt.Errorf("native archive ownership record unreadable; inspect Mail before retrying")}
	}
	id, err := n.get(appleevents.Property("ID  ", created))
	if err != nil || id.Kind != "int" || id.Number <= 0 || existing[id.Number] {
		return &NativeArchiveError{fmt.Errorf("native archive viewer ownership could not be established; inspect Mail before retrying: %w", errors.Join(err, fmt.Errorf("invalid or preexisting viewer ID")))}
	}
	viewerID := id.Number
	viewer := appleevents.ByID("mvwr", appleevents.Int(viewerID), appleevents.Null())
	var session mailax.Session
	pressed := false
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(n.ctx), 5*time.Second)
		defer cancel()
		cleanup := *n
		cleanup.ctx = ctx
		cleanupErr := cleanup.cleanupArchiveViewer(viewerID, viewer, session, driver, previous)
		if session != nil {
			session.Release()
		}
		if cleanupErr != nil {
			err = &NativeArchiveError{errors.Join(err, fmt.Errorf("native archive window %d cleanup failed; inspect Mail before retrying: %w", viewerID, cleanupErr))}
		} else if err != nil && pressed {
			err = &NativeArchiveError{fmt.Errorf("native Archive was issued; outcome is uncertain, reconcile without replaying: %w", err)}
		}
	}()
	if err = n.set(appleevents.Property("msbx", viewer), appleevents.List(source)); err != nil {
		return err
	}
	if err = n.set(appleevents.Property("smgs", viewer), appleevents.List(message)); err != nil {
		return err
	}
	if err = n.set(appleevents.Property("pvis", appleevents.Property("cwin", viewer)), appleevents.Bool(true)); err != nil {
		return err
	}
	// Public kAEMiscStandards/kAEActivate activates Mail before the AX bridge
	// waits for this viewer's exact window to become stably focused.
	if _, err = n.command("misc", "actv", nil); err != nil {
		return err
	}
	session, err = driver.Prepare(n.ctx, viewerID)
	if err != nil {
		return err
	}
	if session == nil {
		return fmt.Errorf("native archive returned no owned Accessibility session")
	}
	for {
		// Read preview before changing AX selection: selecting a row must never
		// cause an implicit mark-read through a newly visible preview pane.
		preview, e := n.get(appleevents.Property("mvpv", viewer))
		if e != nil {
			return e
		}
		if preview.Kind != "bool" || preview.Boolean {
			return fmt.Errorf("Mail preview became visible or could not be verified")
		}
		found, e := session.SelectNext(n.ctx, expected.Subject)
		if e != nil {
			return e
		}
		if !found {
			return fmt.Errorf("could not safely select the exact individual message in Mail; archive it in Mail directly")
		}
		matches, e := n.archiveSelection(viewer, accountID.Text, expected)
		if e != nil {
			return e
		}
		if matches {
			break
		}
	}
	guard := func() error {
		if e := n.ctx.Err(); e != nil {
			return e
		}
		if e := session.Guard(); e != nil {
			return e
		}
		matches, e := n.archiveSelection(viewer, accountID.Text, expected)
		if e != nil {
			return e
		}
		if !matches {
			return fmt.Errorf("Mail message selection changed")
		}
		return session.Guard()
	}
	if err = guard(); err != nil {
		return err
	}
	if err = session.ValidateArchive(); err != nil {
		return err
	}
	if err = guard(); err != nil {
		return err
	}
	if apply {
		return n.phase("native-archive-press", func() error {
			if e := n.ctx.Err(); e != nil {
				return e
			}
			var e error
			pressed, e = session.Press()
			if e == nil && !pressed {
				return fmt.Errorf("Mail Archive did not report a dispatched press")
			}
			if e == nil {
				e = n.ctx.Err()
			}
			return e
		})
	}
	return nil
}

func (n *nativeMail) cleanupArchiveViewer(viewerID int64, viewer appleevents.Value, session mailax.Session, driver mailax.Driver, previous string) error {
	ids, err := n.archiveViewerIDs()
	if err != nil {
		return err
	}
	if !ids[viewerID] {
		return nil
	}
	ownedFocus := false
	var focusErr error
	if session != nil {
		ownedFocus, focusErr = session.OwnedFocus()
	} else {
		ownedFocus, focusErr = driver.OwnedFocus(viewerID)
	}
	if _, err = n.command("core", "clos", map[string]appleevents.Value{"----": appleevents.Property("cwin", viewer), "savo": appleevents.Enum("no  ")}); err != nil {
		return errors.Join(focusErr, err)
	}
	ids, err = n.archiveViewerIDs()
	if err != nil {
		return errors.Join(focusErr, err)
	}
	if ids[viewerID] {
		return errors.Join(focusErr, fmt.Errorf("owned Mail viewer remains open"))
	}
	if focusErr == nil && ownedFocus && previous != "com.apple.mail" {
		return errors.Join(focusErr, driver.Restore(previous))
	}
	return focusErr
}
