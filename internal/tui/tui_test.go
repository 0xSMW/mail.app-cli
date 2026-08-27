package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

func press(m model, keys ...string) model {
	for _, key := range keys {
		var msg tea.KeyPressMsg
		switch key {
		case "enter":
			msg = tea.KeyPressMsg{Code: tea.KeyEnter}
		case "esc":
			msg = tea.KeyPressMsg{Code: tea.KeyEscape}
		case "tab":
			msg = tea.KeyPressMsg{Code: tea.KeyTab}
		case "space":
			msg = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
		default:
			msg = tea.KeyPressMsg{Code: rune(key[0]), Text: key}
		}
		next, _ := m.Update(msg)
		m = next.(model)
	}
	return m
}

func loadedModel(t *testing.T) model {
	t.Helper()
	m := newModel(mail.NewClient(), Options{Color: true})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 150, Height: 40})
	m = next.(model)
	next, _ = m.Update(mailboxesLoadedMsg{
		requestResult: requestResult{requestID: m.mailboxLane.id},
		accounts:      []mail.Account{{Name: "Work", EmailAddress: "me@example.test", Enabled: true}},
		mailboxes:     []mail.Mailbox{{Account: "Work", Name: "INBOX", UnreadCount: 2}, {Account: "Work", Name: "Receipts"}},
	})
	m = next.(model)
	messages := []mail.Message{
		{ID: "1", Account: "Work", Mailbox: "INBOX", Subject: "First", Sender: "A <a@example.test>", Read: false},
		{ID: "2", Account: "Work", Mailbox: "INBOX", Subject: "Second", Sender: "B <b@example.test>", Read: true},
		{ID: "3", Account: "Work", Mailbox: "INBOX", Subject: "Third", Sender: "C <c@example.test>", Read: true},
	}
	next, _ = m.Update(messagesLoadedMsg{requestResult: requestResult{requestID: m.listLane.id}, messages: messages, source: m.sidebar.current().source(), limit: m.list.pageSize()})
	return next.(model)
}

func TestStaleResponsesAreDropped(t *testing.T) {
	m := loadedModel(t)
	stale := messagesLoadedMsg{requestResult: requestResult{requestID: m.listLane.id + 5}, messages: nil}
	next, _ := m.Update(stale)
	if got := len(next.(model).list.messages); got != 3 {
		t.Fatalf("stale response replaced the list: %d messages", got)
	}
}

func TestNavigationSelectionAndModals(t *testing.T) {
	m := loadedModel(t)
	if m.list.current() == nil || m.list.current().ID != "1" {
		t.Fatalf("cursor = %+v", m.list.current())
	}
	m = press(m, "j", "j")
	if m.list.current().ID != "3" {
		t.Fatalf("after j j cursor = %s", m.list.current().ID)
	}
	m = press(m, "k", "space")
	if !m.list.selected[bodyKey(m.list.messages[1])] {
		t.Fatal("space did not select the row")
	}
	if targets := m.list.targets(); len(targets) != 1 || targets[0].ID != "2" {
		t.Fatalf("targets = %+v", targets)
	}
	m = press(m, "esc")
	if len(m.list.selected) != 0 {
		t.Fatal("esc did not clear the selection")
	}

	m = press(m, "m")
	if _, ok := m.modal.(*mailboxPicker); !ok {
		t.Fatalf("m did not open the mailbox picker: %T", m.modal)
	}
	m = press(m, "esc")
	if m.modal != nil {
		t.Fatal("esc did not close the picker")
	}
	m = press(m, "#")
	if _, ok := m.modal.(*confirmModal); !ok {
		t.Fatalf("# did not ask for confirmation: %T", m.modal)
	}
	m = press(m, "n")
	if m.modal != nil || len(m.list.messages) != 3 {
		t.Fatal("declining the confirm changed state")
	}
	m = press(m, "c")
	c, ok := m.modal.(*composeModal)
	if !ok || c.account != "Work" {
		t.Fatalf("c did not open compose for the account: %T %+v", m.modal, m.modal)
	}
	view := m.View().Content
	if !strings.Contains(view, "New message") || !strings.Contains(view, "from Work") {
		t.Fatalf("compose view missing title: %q", view)
	}
}

func TestAutomaticMarkReadKeepsSelection(t *testing.T) {
	m := loadedModel(t)
	// space selects row 2 and advances; two k presses return to unread row 1.
	m = press(m, "j", "space", "k", "k")
	if len(m.list.selected) != 1 || m.list.current().ID != "1" {
		t.Fatalf("selection = %v, cursor = %v", m.list.selected, m.list.current())
	}
	m.reader.open = true
	if cmd := m.markCurrentRead(); cmd == nil {
		t.Fatal("unread message under the cursor should be marked read")
	}
	if len(m.list.selected) != 1 {
		t.Fatal("automatic mark-read cleared the selection")
	}
	m = press(m, "e")
	if len(m.list.selected) != 0 {
		t.Fatal("a user action should consume the selection")
	}
}

func TestMarkReadAdjustsSidebarUnread(t *testing.T) {
	m := loadedModel(t)
	before := m.sidebar.entries[2].unread // Work/INBOX starts at 2
	m = press(m, "u")                     // row 1 is unread; u marks it read
	if got := m.sidebar.entries[2].unread; got != before-1 {
		t.Fatalf("sidebar unread = %d, want %d", got, before-1)
	}
	m = press(m, "e") // archiving a read message leaves the count alone
	if got := m.sidebar.entries[2].unread; got != before-1 {
		t.Fatalf("sidebar unread after archive = %d, want %d", got, before-1)
	}
}

func TestArchiveRemovesRowOptimistically(t *testing.T) {
	m := loadedModel(t)
	m = press(m, "e")
	if len(m.list.messages) != 2 || m.list.messages[0].ID != "2" {
		t.Fatalf("archive did not remove the cursor row: %+v", m.list.messages)
	}
	if !m.writes.busy {
		t.Fatal("archive did not start a write")
	}
	m = press(m, "e")
	if len(m.writes.pending) != 1 {
		t.Fatalf("second archive should queue behind the first, pending = %d", len(m.writes.pending))
	}
	m = press(m, "q")
	if !m.quitting {
		t.Fatal("q with writes in flight should wait for them")
	}
}

func TestReplyAllPrefillDropsOwnAddress(t *testing.T) {
	original := &mail.Message{
		Sender:       "Sender <s@example.test>",
		Subject:      "Hello",
		ToRecipients: []string{"me@example.test", "other@example.test"},
		CcRecipients: []string{"cc@example.test", "ME@example.test"},
		DateReceived: "2026-01-15T10:00:00Z",
		Content:      "line one\nline two",
	}
	c := newComposeModal(composeReplyAll, "Work", original, []string{"me@example.test"})
	if got := c.inputs[fieldTo].Value(); got != "s@example.test, other@example.test" {
		t.Fatalf("to = %q", got)
	}
	if got := c.inputs[fieldCc].Value(); got != "cc@example.test" {
		t.Fatalf("cc = %q", got)
	}
	if got := c.inputs[fieldSubject].Value(); got != "Re: Hello" {
		t.Fatalf("subject = %q", got)
	}
	if !strings.Contains(c.body.Value(), "> line two") {
		t.Fatalf("body = %q", c.body.Value())
	}
}

func TestParseAddressListKeepsNamesTogether(t *testing.T) {
	got, err := parseAddressList("Jane Doe <jane@example.test>, bob@example.test; Carl <carl@example.test>")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"jane@example.test", "bob@example.test", "carl@example.test"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("parseAddressList = %v", got)
	}
	got, _ = parseAddressList(`"Doe, Jane" <Jane@Example.test>, bob@example.test`)
	if strings.Join(got, ",") != "Jane@Example.test,bob@example.test" {
		t.Fatalf("quoted name = %v (case must be preserved)", got)
	}
	got, _ = parseAddressList(`"Doe; Jane" <jane@example.test>; bob@example.test`)
	if strings.Join(got, ",") != "jane@example.test,bob@example.test" {
		t.Fatalf("quoted name with semicolons = %v", got)
	}
}

func TestReplyToOwnSentMessageTargetsRecipients(t *testing.T) {
	original := &mail.Message{
		Sender:       "Me <me@example.test>",
		Subject:      "Ping",
		ToRecipients: []string{"them@example.test", "other@example.test"},
		Content:      "hi",
	}
	c := newComposeModal(composeReply, "Work", original, []string{"me@example.test"})
	if got := c.inputs[fieldTo].Value(); got != "them@example.test, other@example.test" {
		t.Fatalf("reply to own message: to = %q", got)
	}
	c = newComposeModal(composeReplyAll, "Work", original, []string{"me@example.test"})
	if got := c.inputs[fieldTo].Value(); got != "them@example.test, other@example.test" {
		t.Fatalf("reply-all to own message: to = %q", got)
	}
}

func TestReplyToOwnAliasTargetsRecipients(t *testing.T) {
	original := &mail.Message{
		Sender:       "Me <alias@example.test>",
		Subject:      "Ping",
		ToRecipients: []string{"them@example.test"},
		Content:      "hi",
	}
	c := newComposeModal(composeReply, "Work", original, []string{"me@example.test", "alias@example.test"})
	if got := c.inputs[fieldTo].Value(); got != "them@example.test" {
		t.Fatalf("reply to own alias: to = %q", got)
	}
}

func TestAccountAddressesIncludesAliases(t *testing.T) {
	s := sidebar{accounts: []mail.Account{{
		Name:           "Work",
		EmailAddress:   "Primary@Example.test",
		EmailAddresses: []string{"primary@example.test", "Alias@Example.test"},
	}}}
	// Addresses keep their case; the duplicate is dropped case-insensitively.
	if got := strings.Join(s.accountAddresses("Work"), ","); got != "Primary@Example.test,Alias@Example.test" {
		t.Fatalf("account addresses = %q", got)
	}
}

func TestReplyAllDeduplicatesToAndCc(t *testing.T) {
	original := &mail.Message{
		Sender:       "Sender <sender@example.test>",
		Subject:      "Ping",
		ToRecipients: []string{"other@example.test", "OTHER@example.test"},
		CcRecipients: []string{"other@example.test", "cc@example.test", "CC@example.test"},
		Content:      "hi",
	}
	c := newComposeModal(composeReplyAll, "Work", original, []string{"me@example.test"})
	if got := c.inputs[fieldTo].Value(); got != "sender@example.test, other@example.test" {
		t.Fatalf("reply-all deduplicated to = %q", got)
	}
	if got := c.inputs[fieldCc].Value(); got != "cc@example.test" {
		t.Fatalf("reply-all deduplicated cc = %q", got)
	}
}

func TestSelectInitialUnknownAccountIsTypedNotFound(t *testing.T) {
	s := sidebar{entries: []sidebarEntry{{kind: entryUnified}}}
	err := s.selectInitial("Missing", "")
	var notFound *mail.NotFoundError
	if !errors.As(err, &notFound) || notFound.Kind != "account" || notFound.Name != "Missing" {
		t.Fatalf("selectInitial error = %#v", err)
	}
	if got := clierr.Classify(err).Code; got != clierr.CodeNotFound {
		t.Fatalf("classified code = %q, want %q", got, clierr.CodeNotFound)
	}
}

func TestParseAddressListRejectsMalformedParts(t *testing.T) {
	if _, err := parseAddressList("bad <>, good@example.test"); err == nil {
		t.Fatal("malformed recipient beside a valid one was accepted")
	}
	if _, err := parseAddressList("nobody, good@example.test"); err == nil {
		t.Fatal("bare word recipient was accepted")
	}
	if _, err := parseAddressList("bad@example.test garbage; good@example.test"); err == nil {
		t.Fatal("address with trailing garbage was accepted")
	}
}

func TestComposePrefillSanitizesHeaders(t *testing.T) {
	original := &mail.Message{
		Sender:       "Evil\x1b[31m <evil@example.test>",
		Subject:      "Sub\x1b]0;pwned\x07ject",
		ToRecipients: []string{"a\x1b[2Jb@example.test"},
		Content:      "hi",
	}
	c := newComposeModal(composeReplyAll, "Work", original, []string{"me@example.test"})
	for _, value := range []string{c.inputs[fieldTo].Value(), c.inputs[fieldSubject].Value(), c.body.Value()} {
		if strings.ContainsRune(value, '\x1b') || strings.ContainsRune(value, '\x07') {
			t.Fatalf("compose widget received control characters: %q", value)
		}
	}
}

func TestSanitizeLineStripsEscapes(t *testing.T) {
	if got := sanitizeLine("evil\x1b[31m subject\r\n"); got != "evil[31m subject" {
		t.Fatalf("sanitizeLine = %q", got)
	}
	if got := sanitizeBody("  indented\n\tcode\x07 \n"); got != "  indented\n    code\n" {
		t.Fatalf("sanitizeBody = %q", got)
	}
}

func TestRemoveKeepsCursorOnFollowingRow(t *testing.T) {
	m := loadedModel(t)
	m = press(m, "space") // selects row 1, cursor advances to row 2
	if m.list.current().ID != "2" {
		t.Fatalf("cursor after space = %s", m.list.current().ID)
	}
	m.list.remove(map[string]bool{bodyKey(m.list.messages[0]): true})
	if m.list.current().ID != "2" {
		t.Fatalf("cursor after removing row 1 = %s, want 2", m.list.current().ID)
	}
	m.list.cursor = 1
	m.list.remove(map[string]bool{bodyKey(m.list.messages[1]): true})
	if m.list.current() == nil || m.list.current().ID != "2" {
		t.Fatalf("cursor after removing its own row = %v", m.list.current())
	}
}
