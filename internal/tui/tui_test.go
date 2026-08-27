package tui

import (
	"time"

	"errors"
	"strconv"
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
		case "ctrl+r":
			msg = tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
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

func TestNoOpMoveKeepsRow(t *testing.T) {
	m := loadedModel(t)
	m.list.clearSelection()
	targets := m.list.targets()
	_ = m.mutate(targets, mail.BatchOptions{Action: "move", TargetMailbox: "inbox"}, mail.MoveMutator(false))
	if len(m.list.messages) != 3 {
		t.Fatalf("moving to the current mailbox removed rows: %d left", len(m.list.messages))
	}
}

func TestArchiveFromLabelKeepsRowButFolderLosesIt(t *testing.T) {
	m := loadedModel(t)
	m.list.messages[0].Mailbox = "Receipts"
	_ = m.mutate(m.list.targets(), mail.BatchOptions{Action: "archive"}, mail.ArchiveMutator(false))
	if len(m.list.messages) != 2 {
		t.Fatalf("archiving from an IMAP folder should remove the row: %d left", len(m.list.messages))
	}
	m = loadedModel(t)
	m.sidebar.entries = append(m.sidebar.entries, sidebarEntry{kind: entryMailbox, account: "Work", mailbox: "All Mail"})
	m.list.messages[0].Mailbox = "Receipts"
	_ = m.mutate(m.list.targets(), mail.BatchOptions{Action: "archive"}, mail.ArchiveMutator(false))
	if len(m.list.messages) != 3 {
		t.Fatalf("archiving from a Gmail label removed the row: %d left", len(m.list.messages))
	}
	m.list.messages[0].Mailbox = "Spam"
	_ = m.mutate(m.list.targets(), mail.BatchOptions{Action: "archive"}, mail.ArchiveMutator(false))
	if len(m.list.messages) != 2 {
		t.Fatalf("archiving from Gmail Spam should remove the row: %d left", len(m.list.messages))
	}
}

func TestSearchResultsKeepRowsOnArchive(t *testing.T) {
	m := loadedModel(t)
	m.list.setMessages(m.list.messages, false, listSource{search: "first"}, 0)
	_ = m.mutate(m.list.targets(), mail.BatchOptions{Action: "archive"}, mail.ArchiveMutator(false))
	if len(m.list.messages) != 3 {
		t.Fatalf("archiving a search hit removed the row: %d left", len(m.list.messages))
	}
}

func TestAccountPickerOpensCompose(t *testing.T) {
	m := loadedModel(t)
	m.sidebar.accounts = append(m.sidebar.accounts, mail.Account{Name: "Personal", EmailAddress: "p@example.test", Enabled: true})
	m.sidebar.selected = 0
	m = press(m, "c", "enter")
	if _, ok := m.modal.(*composeModal); !ok {
		t.Fatalf("choosing an account did not open the editor: %T", m.modal)
	}
}

func TestComposeFromAllInboxesAsksForAccount(t *testing.T) {
	m := loadedModel(t)
	m.sidebar.accounts = append(m.sidebar.accounts, mail.Account{Name: "Personal", EmailAddress: "p@example.test", Enabled: true})
	m.sidebar.selected = 0 // All inboxes
	m = press(m, "c")
	picker, ok := m.modal.(*mailboxPicker)
	if !ok || picker.title != "Send from" || len(picker.names) != 2 {
		t.Fatalf("compose from All inboxes did not ask for an account: %T %+v", m.modal, m.modal)
	}
}

func TestManualRefreshDefersWhileWritesPending(t *testing.T) {
	m := loadedModel(t)
	m = press(m, "e")
	if !m.writes.busy {
		t.Fatal("archive did not start a write")
	}
	m = press(m, "ctrl+r")
	if m.reloadAfterMailboxes || !m.refreshWanted {
		t.Fatalf("ctrl+r during a write should defer: reloadAfterMailboxes=%v refreshWanted=%v", m.reloadAfterMailboxes, m.refreshWanted)
	}
}

func TestMutationAbandonsInFlightRefresh(t *testing.T) {
	m := loadedModel(t)
	m = press(m, "ctrl+r")
	if !m.mailboxLane.inFlight() || !m.reloadAfterMailboxes {
		t.Fatal("ctrl+r did not start a mailbox reload")
	}
	staleID := m.mailboxLane.id
	m = press(m, "e")
	if m.mailboxLane.inFlight() || m.reloadAfterMailboxes {
		t.Fatal("archive left the refresh read in flight")
	}
	if !m.refreshWanted {
		t.Fatal("archive did not defer the refresh until the write drains")
	}
	if m.mailboxLane.accepts(requestResult{requestID: staleID}) {
		t.Fatal("stale refresh answer would still be accepted")
	}
}

func TestMutationAbandonsPendingUserSearch(t *testing.T) {
	m := loadedModel(t)
	_ = m.runSearch("invoice", false)
	if !m.searchLane.inFlight() || m.list.source.search != "" {
		t.Fatal("search did not start while the mailbox list stayed on screen")
	}
	staleID := m.searchLane.id
	m = press(m, "e")
	if m.searchLane.inFlight() || m.searchLane.accepts(requestResult{requestID: staleID}) {
		t.Fatal("archive left the pending search able to replace the list")
	}
	if !m.refreshWanted {
		t.Fatal("archive did not defer a refresh after abandoning the search")
	}
}

func TestReadsStartedDuringWriteAreDeferred(t *testing.T) {
	m := loadedModel(t)
	m = press(m, "e")
	if !m.writes.busy {
		t.Fatal("archive did not start a write")
	}
	for i, entry := range m.sidebar.entries {
		if entry.mailbox == "Receipts" {
			m.sidebar.cursor = i
		}
	}
	_ = m.sidebar.choose(&m)
	if m.listLane.inFlight() {
		t.Fatal("mailbox switch read the index while a write was pending")
	}
	if m.list.source.mailbox != "Receipts" || len(m.list.messages) != 0 || !m.refreshWanted {
		t.Fatalf("switch did not defer to the post-drain refresh: source=%+v rows=%d refreshWanted=%v", m.list.source, len(m.list.messages), m.refreshWanted)
	}
	if cmd := m.runSearch("invoice", false); cmd != nil || m.searchLane.inFlight() || m.list.source.search != "invoice" {
		t.Fatal("search during a write was not deferred")
	}
	m.list.hasMore = true
	m.list.cursor = len(m.list.messages)
	if cmd := m.loadMore(); cmd != nil || m.pageLane.inFlight() {
		t.Fatal("paging during a write was not deferred")
	}
}

func TestSettledStateOutlivesLaggingRefresh(t *testing.T) {
	m := loadedModel(t)
	unread := m.list.messages[0] // row 1 is unread
	m = press(m, "u")            // mark read
	m = press(m, "j")
	m = press(m, "e") // archive row 2
	now := time.Now()
	receipt := func(msg mail.Message) mail.BatchResult {
		return mail.BatchResult{Items: []mail.BatchItem{{ID: msg.ID, Account: msg.Account, SourceMailbox: msg.Mailbox, Status: "succeeded"}}}
	}
	m.recordSettled(mutationDoneMsg{result: receipt(unread), opts: mail.BatchOptions{Action: "mark", Read: true}}, now)
	archived := mail.Message{ID: "2", Account: "Work", Mailbox: "INBOX"}
	m.recordSettled(mutationDoneMsg{result: receipt(archived), opts: mail.BatchOptions{Action: "archive"}, removed: map[string]bool{bodyKey(archived): true}}, now)

	stale := []mail.Message{
		{ID: "1", Account: "Work", Mailbox: "INBOX", Subject: "First", Read: false},
		{ID: "2", Account: "Work", Mailbox: "INBOX", Subject: "Second", Read: true},
		{ID: "3", Account: "Work", Mailbox: "INBOX", Subject: "Third", Read: true},
	}
	rows, lagging := m.reconcile(stale, now)
	if !lagging || len(rows) != 2 || rows[0].ID != "1" || !rows[0].Read || rows[1].ID != "3" {
		t.Fatalf("stale page was not reconciled: lagging=%v rows=%+v", lagging, rows)
	}
	caughtUp := []mail.Message{
		{ID: "1", Account: "Work", Mailbox: "INBOX", Subject: "First", Read: true},
		{ID: "3", Account: "Work", Mailbox: "INBOX", Subject: "Third", Read: true},
	}
	if rows, lagging := m.reconcile(caughtUp, now); lagging || len(rows) != 2 {
		t.Fatalf("index that agrees was still treated as lagging: %v %+v", lagging, rows)
	}
	if _, held := m.settled[bodyKey(unread)]; held {
		t.Fatal("agreed read state was not forgotten")
	}
	if rows, lagging := m.reconcile(stale, now.Add(settledTTL+time.Second)); lagging || len(rows) != 3 {
		t.Fatalf("expired entries still applied: %v %+v", lagging, rows)
	}
}

func TestDeleteInSearchRemovesRowAndRefreshSyncsReader(t *testing.T) {
	m := loadedModel(t)
	hits := []mail.Message{
		{ID: "1", Account: "Work", Mailbox: "INBOX", Subject: "First", Read: true},
		{ID: "2", Account: "Work", Mailbox: "INBOX", Subject: "Second", Read: true},
	}
	m.list.setMessages(hits, false, listSource{search: "first"}, 0)
	m.reader.open = true
	_ = m.requestBody()
	m = press(m, "#")
	m = press(m, "y")
	if len(m.list.messages) != 1 || m.list.messages[0].ID != "2" {
		t.Fatalf("delete in search mode left the row: %+v", m.list.messages)
	}
	if !m.reader.open || m.readerKey != bodyKey(hits[1]) {
		t.Fatalf("reader did not follow the cursor off the deleted message: open=%v key=%q", m.reader.open, m.readerKey)
	}
	next, _ := m.Update(searchDoneMsg{requestResult: requestResult{requestID: m.searchLane.id}, query: "first", silent: true,
		result: mail.SearchResult{Complete: true}})
	m = next.(model)
	if m.reader.open {
		t.Fatal("reader stayed open with no rows left")
	}
}

func TestUnreadFromListDoesNotSuppressAutoRead(t *testing.T) {
	m := loadedModel(t)
	m = press(m, "j") // row 2 is read; the reader stays closed
	m = press(m, "u")
	if len(m.noAutoRead) != 0 {
		t.Fatalf("marking unread from the list guarded a message nobody is reading: %v", m.noAutoRead)
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

func TestPickerSanitizesNames(t *testing.T) {
	m := loadedModel(t)
	p := newMailboxPicker("Move to", []string{"Bad\x1b[2Jname"}, func(*model, string) tea.Cmd { return nil })
	// Styling adds its own escapes; the name's clear-screen sequence must not survive.
	if view := p.view(&m); strings.Contains(view, "\x1b[2J") {
		t.Fatalf("picker rendered a control sequence: %q", view)
	}
}

func TestFailedAutoMarkReadIsNotRetried(t *testing.T) {
	m := loadedModel(t)
	m.reader.open = true
	key := m.currentKey()
	m.suppressAutoRead(map[string]bool{key: true})
	if cmd := m.autoMarkRead(); cmd != nil {
		t.Fatal("automatic mark-read retried after a failure")
	}
	m = press(m, "u") // row 1 is unread, so u marks it read and lifts the guard
	if m.noAutoRead[key] {
		t.Fatal("explicit mark-read did not lift the guard")
	}
}

func TestExplicitUnreadSurvivesReaderRefresh(t *testing.T) {
	m := loadedModel(t)
	m = press(m, "j") // row 2 is read
	m.reader.open = true
	_ = m.requestBody()
	m = press(m, "u") // mark it unread while reading
	if !m.noAutoRead[m.currentKey()] {
		t.Fatal("marking unread did not suppress automatic mark-read")
	}
	if cmd := m.autoMarkRead(); cmd != nil {
		t.Fatal("reader re-marked an explicitly unread message")
	}
	m = press(m, "j") // moving off the message lifts the suppression
	_ = m.requestBody()
	if len(m.noAutoRead) != 0 {
		t.Fatalf("suppression did not lift on navigation: %v", m.noAutoRead)
	}
}

func TestBodyCacheStaysBounded(t *testing.T) {
	r := newReader(newStyles(false))
	shown := &mail.Message{ID: "shown", Account: "A", Mailbox: "M"}
	r.remember(bodyKey(*shown), shown)
	r.show(shown)
	for i := 0; i < bodyCacheSize*3; i++ {
		m := &mail.Message{ID: strconv.Itoa(i), Account: "A", Mailbox: "M"}
		r.remember(bodyKey(*m), m)
		// Revisit the displayed message each round, the pattern that used to leak.
		r.remember(bodyKey(*shown), shown)
	}
	if len(r.cache) > bodyCacheSize || len(r.order) > bodyCacheSize {
		t.Fatalf("cache = %d entries, order = %d, want at most %d", len(r.cache), len(r.order), bodyCacheSize)
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
