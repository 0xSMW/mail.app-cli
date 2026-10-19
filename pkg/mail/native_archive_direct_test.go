package mail

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
	"github.com/0xSMW/mail.app-cli/v3/internal/mailax"
)

type directArchiveAX struct {
	trustedErr, prepareErr, archiveErr, pressErr, focusErr       error
	guardErrAt                                                   int
	guards, selections, presses, released, restored, focusChecks int
	attempted, focused                                           bool
	selectNext                                                   func(int) bool
	onArchive, onPress                                           func()
	onPrepare                                                    func(int64) error
	ownedID                                                      int64
}

func (a *directArchiveAX) Trusted() error                    { return a.trustedErr }
func (a *directArchiveAX) FrontApplication() (string, error) { return "com.example.editor", nil }
func (a *directArchiveAX) Restore(string) error              { a.restored++; return nil }
func (a *directArchiveAX) OwnedFocus(id int64) (bool, error) {
	a.ownedID = id
	a.focusChecks++
	return a.focused, a.focusErr
}

// Session's OwnedFocus method cannot overload Driver's OwnedFocus, so wrap it.
type directArchiveSession struct{ ax *directArchiveAX }

func (a *directArchiveAX) sessionPrepare(ctx context.Context, id int64) (mailax.Session, error) {
	a.ownedID = id
	if a.onPrepare != nil {
		if err := a.onPrepare(id); err != nil {
			return nil, err
		}
	}
	if a.prepareErr != nil {
		return nil, a.prepareErr
	}
	return &directArchiveSession{a}, nil
}

type directArchiveDriver struct{ *directArchiveAX }

func (a directArchiveDriver) Prepare(ctx context.Context, id int64) (mailax.Session, error) {
	return a.sessionPrepare(ctx, id)
}
func (s *directArchiveSession) SelectNext(ctx context.Context, subject string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.ax.selections++
	if s.ax.selectNext != nil {
		return s.ax.selectNext(s.ax.selections), nil
	}
	return s.ax.selections == 1, nil
}
func (s *directArchiveSession) Guard() error {
	s.ax.guards++
	if s.ax.guardErrAt == s.ax.guards {
		return fmt.Errorf("Mail focus or individual selection changed")
	}
	return nil
}
func (s *directArchiveSession) ValidateArchive() error {
	if s.ax.onArchive != nil {
		s.ax.onArchive()
	}
	return s.ax.archiveErr
}
func (s *directArchiveSession) Press() (bool, error) {
	if s.ax.attempted {
		s.ax.presses++
	}
	if s.ax.onPress != nil {
		s.ax.onPress()
	}
	return s.ax.attempted, s.ax.pressErr
}
func (s *directArchiveSession) OwnedFocus() (bool, error) {
	s.ax.focusChecks++
	return s.ax.focused, s.ax.focusErr
}
func (s *directArchiveSession) Release() { s.ax.released++ }

type directArchiveMail struct {
	account, box, stableMailboxRef, target                             appleevents.Value
	selected                                                           appleevents.Value
	selectedAccount, selectedMailbox, host                             string
	hostInvalid, preview, open, remains, existingOwner, malformedOwner bool
	created, closed                                                    int
	closedID                                                           int64
	createErr, closeErr, setErr                                        error
	identityInvalid                                                    bool
	events                                                             []appleevents.Event
	viewerID                                                           int64
}

func newDirectArchiveMail() *directArchiveMail {
	m := &directArchiveMail{host: "imap.gmail.com", selectedAccount: "acct", selectedMailbox: "INBOX", viewerID: 42}
	m.account = appleevents.ByID("mact", appleevents.String("acct"), appleevents.Null())
	m.box = appleevents.Index("mbxp", 1, m.account)
	m.stableMailboxRef = appleevents.ByID("mbxp", appleevents.String("physical-inbox"), m.account)
	m.target = appleevents.ByID("mssg", appleevents.Int(7), m.stableMailboxRef)
	m.selected = m.target
	return m
}
func directClass(v appleevents.Value) string { return v.Fields["want"].Code }
func (m *directArchiveMail) get(v appleevents.Value) (appleevents.Value, error) {
	if v.Kind == "list" {
		out := make([]appleevents.Value, len(v.Items))
		for i, item := range v.Items {
			var e error
			out[i], e = m.get(item)
			if e != nil {
				return appleevents.Value{}, e
			}
		}
		return appleevents.List(out...), nil
	}
	class := directClass(v)
	if class == "mact" {
		if v.Fields["form"].Code != "test" || v.Fields["seld"].Kind != "comparison" || v.Fields["seld"].Fields["obj2"].Text != "Test" {
			return appleevents.Value{}, fmt.Errorf("archive account must use an exact typed name test")
		}
		return appleevents.List(m.account), nil
	}
	if class == "mbxp" {
		if v.Fields["form"].Code == "indx" && v.Fields["seld"].Kind == "int" && v.Fields["seld"].Number == 1 {
			return m.stableMailboxRef, nil
		}
		if directClass(v.Fields["from"]) == "mact" {
			return appleevents.List(m.box), nil
		}
		return appleevents.List(), nil
	}
	if class == "mvwr" {
		viewers := []appleevents.Value{appleevents.ByID("mvwr", appleevents.Int(99), appleevents.Null())}
		if m.open && !m.existingOwner {
			viewers = append(viewers, appleevents.ByID("mvwr", appleevents.Int(m.viewerID), appleevents.Null()))
		}
		return appleevents.List(viewers...), nil
	}
	if class != "prop" {
		return appleevents.Value{}, fmt.Errorf("unexpected direct archive get: %#v", v)
	}
	code := v.Fields["seld"].Code
	from := v.Fields["from"]
	switch code {
	case "pnam":
		if directClass(from) == "mact" {
			return appleevents.String("Test"), nil
		}
		if directClass(from) == "mbxp" && from.Fields["seld"].Kind == "ordinal" && from.Fields["seld"].Code == "all " {
			container := from.Fields["from"]
			if directClass(container) == "mact" {
				return appleevents.List(appleevents.String("INBOX")), nil
			}
			if directClass(container) == "mbxp" && container.Fields["seld"].Kind == "ordinal" && container.Fields["seld"].Code == "all " {
				return appleevents.List(appleevents.List()), nil
			}
			return appleevents.Value{}, fmt.Errorf("unexpected archive mailbox name matrix scope")
		}
		if !m.open {
			return appleevents.String("INBOX"), nil
		}
		return appleevents.String(m.selectedMailbox), nil
	case "host":
		if m.hostInvalid {
			return appleevents.Null(), nil
		}
		return appleevents.String(m.host), nil
	case "ID  ":
		switch directClass(from) {
		case "mact":
			return appleevents.String(from.Fields["seld"].Text), nil
		case "mvwr":
			return from.Fields["seld"], nil
		case "mssg":
			return from.Fields["seld"], nil
		}
	case "subj":
		return appleevents.String("Same subject"), nil
	case "sndr":
		return appleevents.String("Example <sender@example.test>"), nil
	case "drcv":
		if m.identityInvalid {
			return appleevents.String("invalid"), nil
		}
		return appleevents.Date(time.Unix(12345, 0)), nil
	case "msze":
		return appleevents.Int(123), nil
	case "mvpv":
		return appleevents.Bool(m.preview), nil
	case "msbx":
		return appleevents.List(m.stableMailboxRef), nil
	case "mact":
		if !m.open {
			return m.account, nil
		}
		return appleevents.ByID("mact", appleevents.String(m.selectedAccount), appleevents.Null()), nil
	case "mbxc":
		return appleevents.Null(), nil
	case "smgs":
		return appleevents.List(m.selected), nil
	}
	return appleevents.Value{}, fmt.Errorf("unexpected direct archive property: %s", code)
}
func (m *directArchiveMail) send(ctx context.Context, e appleevents.Event) (appleevents.Value, appleevents.Stats, error) {
	m.events = append(m.events, e)
	stats := appleevents.Stats{Events: 1, Attempted: true}
	if err := ctx.Err(); err != nil {
		return appleevents.Value{}, stats, err
	}
	switch e.ID {
	case "getd":
		v, err := m.get(e.Params["----"])
		return v, stats, err
	case "crel":
		m.created++
		if e.Params["kocl"].Code != "mvwr" || e.Params["prdt"].Fields["mvpv"].Kind != "bool" || e.Params["prdt"].Fields["mvpv"].Boolean {
			return appleevents.Value{}, stats, fmt.Errorf("viewer creation must hide preview")
		}
		if m.createErr != nil {
			return appleevents.Value{}, stats, m.createErr
		}
		m.open = true
		if m.malformedOwner {
			return appleevents.Null(), stats, nil
		}
		id := m.viewerID
		if m.existingOwner {
			id = 99
		}
		return appleevents.ByID("mvwr", appleevents.Int(id), appleevents.Null()), stats, nil
	case "setd":
		return appleevents.Null(), stats, m.setErr
	case "actv":
		if e.Class != "misc" || len(e.Params) != 0 {
			return appleevents.Value{}, stats, fmt.Errorf("Mail activation must use kAEMiscStandards/kAEActivate")
		}
		return appleevents.Null(), stats, nil
	case "clos":
		m.closed++
		window := e.Params["----"]
		if directClass(window) != "prop" || window.Fields["seld"].Code != "cwin" || e.Params["savo"].Code != "no  " {
			return appleevents.Value{}, stats, fmt.Errorf("invalid owned cleanup")
		}
		m.closedID = window.Fields["from"].Fields["seld"].Number
		if m.closeErr != nil {
			return appleevents.Value{}, stats, m.closeErr
		}
		if !m.remains {
			m.open = false
		}
		return appleevents.Null(), stats, nil
	}
	return appleevents.Value{}, stats, fmt.Errorf("unexpected direct archive event %s/%s", e.Class, e.ID)
}

func directArchiveClient(t *testing.T, m *directArchiveMail, a *directArchiveAX) *Client {
	t.Helper()
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	// Any accidental scripting dispatch is a test failure, not a native fallback.
	writeFakeOsaScript(t, `printf 'unexpected script invocation' >&2; exit 1`)
	c := NewClient()
	if err := c.SetBackend(BackendNative); err != nil {
		t.Fatal(err)
	}
	c.shared.nativeSender = m.send
	c.shared.nativeAX = directArchiveDriver{a}
	return c
}

func TestNativeArchiveDirectGuardsAndCleanup(t *testing.T) {
	for _, tt := range []struct {
		name                               string
		setup                              func(*directArchiveMail, *directArchiveAX)
		apply, wantError, uncertain        bool
		created, presses, closed, restored int
	}{
		{name: "apply", apply: true, created: 1, presses: 1, closed: 1, restored: 1},
		{name: "prepare", created: 1, closed: 1, restored: 1},
		{name: "accessibility-denied", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { a.trustedErr = errors.New("denied") }, wantError: true},
		{name: "non-gmail", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.host = "imap.example.test" }, wantError: true},
		{name: "gmail-lookalike", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.host = "imap.gmail.com.example.test" }, wantError: true},
		{name: "malformed-host", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.hostInvalid = true }, wantError: true},
		{name: "malformed-identity", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.identityInvalid = true }, wantError: true},
		{name: "existing-viewer-not-owned", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.existingOwner = true }, wantError: true, uncertain: true, created: 1},
		{name: "creation-uncertain", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) {
			m.createErr = &appleevents.Error{Code: -1712, Attempted: true}
		}, wantError: true, uncertain: true, created: 1},
		{name: "creation-no-dispatch", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.createErr = &appleevents.Error{Code: -1743} }, wantError: true, created: 1},
		{name: "creation-malformed", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.malformedOwner = true }, wantError: true, uncertain: true, created: 1},
		{name: "prepare-failure", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { a.prepareErr = errors.New("owned list not ready") }, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "configuration-failure", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.setErr = errors.New("selection unavailable") }, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "preview-visible", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.preview = true }, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "wrong-account", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.selectedAccount = "other" }, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "wrong-mailbox", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) {
			a.selectNext = func(i int) bool { m.selectedMailbox = "Archive"; return true }
		}, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "wrong-message", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) {
			m.selected = appleevents.ByID("mssg", appleevents.Int(8), m.box)
		}, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "same-subject-candidate", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) {
			a.selectNext = func(i int) bool {
				if i == 1 {
					m.selected = appleevents.ByID("mssg", appleevents.Int(8), m.box)
				} else {
					m.selected = m.target
				}
				return i <= 2
			}
		}, created: 1, presses: 1, closed: 1, restored: 1},
		{name: "conversation-or-focus", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { a.guardErrAt = 1 }, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "late-focus", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { a.guardErrAt = 4 }, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "late-selection", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) {
			a.onArchive = func() { m.selected = appleevents.ByID("mssg", appleevents.Int(8), m.box) }
		}, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "ambiguous-or-disabled-control", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) {
			a.archiveErr = errors.New("ambiguous Archive AXIdentifier")
		}, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "press-guard-failure", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) {
			a.attempted = false
			a.pressErr = errors.New("focus changed")
		}, wantError: true, created: 1, closed: 1, restored: 1},
		{name: "press-uncertain", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { a.pressErr = errors.New("AXPress failed") }, wantError: true, uncertain: true, created: 1, presses: 1, closed: 1, restored: 1},
		{name: "cleanup-failure", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { m.closeErr = errors.New("close failed") }, wantError: true, uncertain: true, created: 1, presses: 1, closed: 1},
		{name: "cleanup-still-open", setup: func(m *directArchiveMail, a *directArchiveAX) { m.remains = true }, wantError: true, uncertain: true, created: 1, closed: 1},
		{name: "focus-check-failure-still-closes", setup: func(m *directArchiveMail, a *directArchiveAX) { a.focusErr = errors.New("AX no longer available") }, wantError: true, uncertain: true, created: 1, closed: 1},
		{name: "user-changed-focus", apply: true, setup: func(m *directArchiveMail, a *directArchiveAX) { a.focused = false }, created: 1, presses: 1, closed: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newDirectArchiveMail()
			a := &directArchiveAX{attempted: true, focused: true}
			if tt.setup != nil {
				tt.setup(m, a)
			}
			err := directArchiveClient(t, m, a).runNativeArchive("Test", "7", tt.apply)
			var uncertain *NativeArchiveError
			if (err != nil) != tt.wantError || errors.As(err, &uncertain) != tt.uncertain {
				t.Fatalf("error=%v uncertain=%v, want error=%v uncertain=%v", err, uncertain, tt.wantError, tt.uncertain)
			}
			if m.created != tt.created || a.presses != tt.presses || m.closed != tt.closed || a.restored != tt.restored {
				t.Fatalf("created/pressed/closed/restored=%d/%d/%d/%d, want %d/%d/%d/%d", m.created, a.presses, m.closed, a.restored, tt.created, tt.presses, tt.closed, tt.restored)
			}
			if m.closed > 0 && m.closedID != 42 {
				t.Fatalf("cleanup touched unowned viewer %d", m.closedID)
			}
			if a.ownedID != 0 && a.ownedID != 42 {
				t.Fatalf("AX touched unowned viewer %d", a.ownedID)
			}
			if tt.created == 1 && !m.existingOwner && !m.malformedOwner && m.createErr == nil && a.prepareErr == nil && m.setErr == nil && a.released != 1 {
				t.Fatalf("owned AX session releases=%d", a.released)
			}
		})
	}
}

func TestNativeArchiveDirectCancellationNeverReplays(t *testing.T) {
	for _, afterPress := range []bool{false, true} {
		t.Run(fmt.Sprint(afterPress), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m := newDirectArchiveMail()
			a := &directArchiveAX{attempted: true, focused: true}
			if afterPress {
				a.onPress = cancel
			} else {
				a.onArchive = cancel
			}
			err := directArchiveClient(t, m, a).WithContext(ctx).runNativeArchive("Test", "7", true)
			var uncertain *NativeArchiveError
			if !errors.Is(err, context.Canceled) || errors.As(err, &uncertain) != afterPress {
				t.Fatalf("cancellation=%v uncertain=%v afterPress=%v", err, uncertain, afterPress)
			}
			want := 0
			if afterPress {
				want = 1
			}
			if a.presses != want || m.closed != 1 || m.closedID != 42 || a.released != 1 {
				t.Fatalf("presses=%d closed=%d id=%d released=%d", a.presses, m.closed, m.closedID, a.released)
			}
		})
	}
}

func TestNativeArchiveDirectAccountProbeAndPreflight(t *testing.T) {
	for _, host := range []string{"imap.gmail.com", " IMAP.GOOGLEMAIL.COM. ", "imap.gmail.com.example.test", "imap.gmail.com..", "imap.example.test"} {
		t.Run(host, func(t *testing.T) {
			m := newDirectArchiveMail()
			m.host = host
			a := &directArchiveAX{}
			c := directArchiveClient(t, m, a)
			got, err := c.archiveUsesNative("Test", "INBOX", "All Mail")
			want := host == "imap.gmail.com" || host == " IMAP.GOOGLEMAIL.COM. "
			if err != nil || got != want {
				t.Fatalf("probe=%v %v", got, err)
			}
			events := len(m.events)
			if _, err = c.archiveUsesNative("Test", "INBOX", "All Mail"); err != nil {
				t.Fatal(err)
			}
			if len(m.events) != events {
				t.Fatal("account decision was not cached")
			}
			if err = c.preflightNativeArchive(); err != nil {
				t.Fatal(err)
			}
			if len(m.events) != events {
				t.Fatal("AX preflight dispatched an Apple Event")
			}
			if m.created != 0 || a.presses != 0 {
				t.Fatal("probe or preflight mutated Mail")
			}
		})
	}
}

func TestNativeArchiveDirectPressDeadlinePreservesUncertainty(t *testing.T) {
	m := newDirectArchiveMail()
	a := &directArchiveAX{attempted: true, focused: true, pressErr: context.DeadlineExceeded}
	err := directArchiveClient(t, m, a).runNativeArchive("Test", "7", true)
	var uncertain *NativeArchiveError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &uncertain) {
		t.Fatalf("press deadline lost uncertainty: %v", err)
	}
	if a.presses != 1 || m.closed != 1 || m.closedID != 42 {
		t.Fatalf("deadline press/cleanup=%d/%d viewer=%d", a.presses, m.closed, m.closedID)
	}
}

func TestNativeArchiveDirectActivationPrecedesOwnedAXPreparation(t *testing.T) {
	m := newDirectArchiveMail()
	a := &directArchiveAX{attempted: true, focused: true}
	prepared := false
	a.onPrepare = func(id int64) error {
		prepared = true
		if id != 42 || len(m.events) < 2 {
			return fmt.Errorf("AX preparation lacks an owned visible viewer")
		}
		activate := m.events[len(m.events)-1]
		visible := m.events[len(m.events)-2]
		if activate.Class != "misc" || activate.ID != "actv" || len(activate.Params) != 0 {
			return fmt.Errorf("AX preparation preceded raw Mail activation")
		}
		property := visible.Params["----"]
		if visible.Class != "core" || visible.ID != "setd" || property.Fields["seld"].Code != "pvis" || !visible.Params["data"].Boolean || property.Fields["from"].Fields["from"].Fields["seld"].Number != 42 {
			return fmt.Errorf("activation did not follow owned window visibility")
		}
		return nil
	}
	if err := directArchiveClient(t, m, a).runNativeArchive("Test", "7", false); err != nil {
		t.Fatal(err)
	}
	if !prepared || a.presses != 0 || m.closedID != 42 {
		t.Fatalf("prepared=%v presses=%d closedID=%d", prepared, a.presses, m.closedID)
	}
}
