// Package tui is the terminal mail client. It reads lists from the Envelope
// Index through pkg/mail and sends every write and body fetch through
// Mail.app automation, one at a time, without blocking the screen.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

// Options selects what the TUI opens on.
type Options struct {
	Account   string
	Mailbox   string
	MessageID string
	Color     bool
}

type focusPane int

const (
	focusSidebar focusPane = iota
	focusList
	focusReader
)

type errMsg struct{ err error }

type spinnerTickMsg struct{}
type ctrlCResetMsg struct{}
type refreshDueMsg struct{ id uint64 }
type bodyDebounceMsg struct{ key string }

// warnMsg carries a pkg/mail warning (index fallback, content budget) that
// would otherwise be printed over the screen.
type warnMsg struct{ text string }

// modal is whatever is open over the panes and holds every key while it is.
type modal interface {
	handleKey(m *model, msg tea.KeyPressMsg) (tea.Cmd, bool)
	handleMsg(m *model, msg tea.Msg) (tea.Cmd, bool)
	view(m *model) string
	helpBindings() []helpBinding
}

type model struct {
	client *mail.Client
	ctx    context.Context
	cancel context.CancelFunc
	opts   Options

	width, height int
	styles        styles
	focus         focusPane
	modal         modal

	sidebar sidebar
	list    list
	reader  reader

	mailboxLane requestLane
	listLane    requestLane
	pageLane    requestLane
	bodyLane    requestLane
	searchLane  requestLane

	// Writes never supersede each other: they queue and run one at a time
	// under a context that only a forced quit cancels.
	writes    writeQueue
	quitting  bool
	writeCtx  context.Context
	stopWrite context.CancelFunc

	spinning     bool
	spinnerFrame int
	// refreshWanted defers the index refresh until every queued write has
	// finished, so a reload cannot resurrect rows a pending write removes.
	refreshWanted bool
	// reloadAfterMailboxes makes ctrl+r reload the list once the refreshed
	// sidebar has settled the selection, rather than racing it.
	reloadAfterMailboxes bool
	notice               string
	err                  error
	helpHidden           bool
	ctrlCOnce            bool
	refreshID            uint64

	toast   notifyMsg
	toastID uint64

	// pendingOpen is a message ID to open once its mailbox list is loaded.
	pendingOpen string
	// pendingCompose is a reply or forward waiting on the body it quotes.
	pendingCompose *pendingCompose
	// initCmd is the first load, begun in newModel so the request lane's
	// bookkeeping lands on the model Bubble Tea keeps rather than a copy.
	initCmd tea.Cmd
	// fatal is why the program quit on its own; Run returns it.
	fatal error
}

func newModel(client *mail.Client, opts Options) model {
	ctx, cancel := context.WithCancel(context.Background())
	writeCtx, stopWrite := context.WithCancel(context.Background())
	m := model{
		client:      client,
		ctx:         ctx,
		cancel:      cancel,
		writeCtx:    writeCtx,
		stopWrite:   stopWrite,
		opts:        opts,
		styles:      newStyles(opts.Color),
		focus:       focusList,
		pendingOpen: opts.MessageID,
	}
	m.reader = newReader(m.styles)
	m.initCmd = m.loadMailboxes()
	return m
}

// Run starts the program. It returns when the user quits.
func Run(client *mail.Client, opts Options) error {
	m := newModel(client, opts)
	p := tea.NewProgram(m)
	client.SetWarn(func(text string) { p.Send(warnMsg{text: text}) })
	final, err := p.Run()
	m.cancel()
	m.stopWrite()
	if err != nil {
		return err
	}
	if fm, ok := final.(model); ok && fm.fatal != nil {
		return fm.fatal
	}
	return nil
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.initCmd, spinnerTick())
}

func spinnerTick() tea.Cmd {
	return tea.Tick(80*time.Millisecond, func(time.Time) tea.Msg { return spinnerTickMsg{} })
}

// --- Update ---

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case spinnerTickMsg:
		m.spinnerFrame++
		if m.anyLoading() {
			m.spinning = true
			return m, spinnerTick()
		}
		m.spinning = false
		return m, nil

	case ctrlCResetMsg:
		m.ctrlCOnce = false
		return m, nil

	case notifyMsg:
		return m, m.showToast(msg)

	case warnMsg:
		text := sanitizeLine(msg.text)
		if strings.Contains(text, "Envelope Index") {
			m.notice = "slow mode: Envelope Index unavailable"
		}
		return m, m.showToast(notifyMsg{text: text, kind: toastError})

	case toastExpiredMsg:
		if msg.id == m.toastID {
			m.toast = notifyMsg{}
		}
		return m, nil

	case errMsg:
		m.err = msg.err
		return m, nil

	case mailboxesLoadedMsg:
		return m.onMailboxesLoaded(msg)

	case messagesLoadedMsg:
		return m.onMessagesLoaded(msg)

	case pageLoadedMsg:
		return m.onPageLoaded(msg)

	case bodyDebounceMsg:
		if m.reader.open && m.currentKey() == msg.key {
			return m, m.spin(m.loadBody())
		}
		return m, nil

	case bodyLoadedMsg:
		return m.onBodyLoaded(msg)

	case writeDoneMsg:
		// The queue advances here, once, whatever the write reported.
		next := m.writes.next()
		if next == nil && m.quitting {
			return m, tea.Quit
		}
		updated, cmd := m.Update(msg.inner)
		m = updated.(model)
		if next == nil && m.refreshWanted {
			m.refreshWanted = false
			cmd = tea.Batch(cmd, m.scheduleRefresh())
		}
		return m, tea.Batch(next, cmd)

	case mutationDoneMsg:
		return m.onMutationDone(msg)

	case searchDoneMsg:
		return m.onSearchDone(msg)

	case composeDoneMsg:
		return m.onComposeDone(msg)

	case refreshDueMsg:
		if msg.id == m.refreshID {
			return m, m.reloadList(true)
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}

	if m.modal != nil {
		if cmd, handled := m.modal.handleMsg(&m, msg); handled {
			return m, cmd
		}
	}
	return m, nil
}

func (m *model) anyLoading() bool {
	return m.mailboxLane.loading || m.listLane.loading || m.pageLane.loading || m.bodyLane.loading || m.searchLane.loading || m.writes.busy
}

// spin starts the spinner chain if a load is now in flight and no chain is
// already running.
func (m *model) spin(cmd tea.Cmd) tea.Cmd {
	if m.spinning || !m.anyLoading() {
		return cmd
	}
	m.spinning = true
	return tea.Batch(cmd, spinnerTick())
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		if m.ctrlCOnce {
			// A forced quit abandons queued writes; the one running is
			// killed, which Mail.app may or may not have applied.
			m.cancel()
			m.stopWrite()
			return m, tea.Quit
		}
		m.ctrlCOnce = true
		return m, tea.Batch(notify("press ctrl+c again to quit"), tea.Tick(2*time.Second, func(time.Time) tea.Msg { return ctrlCResetMsg{} }))
	}

	if m.err != nil {
		if key == "esc" || key == "q" || key == "enter" {
			m.err = nil
		}
		return m, nil
	}

	var cmd tea.Cmd
	switch {
	case m.modal != nil:
		var open bool
		cmd, open = m.modal.handleKey(&m, msg)
		if !open {
			m.modal = nil
			m.layout()
		}
	case key == "?":
		m.helpHidden = !m.helpHidden
		m.layout()
	case key == "ctrl+r":
		m.reloadAfterMailboxes = true
		cmd = m.loadMailboxes()
	case key == "tab":
		m.cycleFocus(1)
	case key == "shift+tab":
		m.cycleFocus(-1)
	case key == "/":
		m.modal = newSearchModal()
	case key == "c":
		cmd = m.openCompose(composeNew)
	case m.focus != focusReader && len(key) == 1 && key[0] >= '1' && key[0] <= '9':
		cmd = m.sidebar.jumpTo(&m, int(key[0]-'1'))
	case m.focus == focusSidebar:
		cmd = m.sidebar.handleKey(&m, msg)
	case m.focus == focusReader:
		cmd = m.reader.handleKey(&m, msg)
	default:
		cmd = m.list.handleKey(&m, msg)
	}
	return m, m.spin(cmd)
}

func (m *model) cycleFocus(delta int) {
	panes := []focusPane{focusList}
	if m.sidebarVisible() {
		panes = append([]focusPane{focusSidebar}, panes...)
	}
	if m.reader.open {
		panes = append(panes, focusReader)
	}
	current := 0
	for i, pane := range panes {
		if pane == m.focus {
			current = i
		}
	}
	m.focus = panes[(current+delta+len(panes))%len(panes)]
}

// --- Layout ---

const (
	sidebarWidth      = 26
	threePaneMinWidth = 140
	minSidebarWidth   = 90
)

func (m *model) sidebarVisible() bool {
	return m.width >= minSidebarWidth && !(m.reader.open && !m.threePane())
}

func (m *model) threePane() bool {
	return m.width >= threePaneMinWidth
}

// contentHeight is what is left after the header, its rule, the footer
// rule, and the help bar.
func contentHeight(height int, help string) int {
	return max(height-3-lipgloss.Height(help), 3)
}

func (m *model) layout() {
	height := contentHeight(m.height, m.helpView())
	listWidth := m.width
	if m.sidebarVisible() {
		listWidth -= sidebarWidth + 1
	}
	readerWidth := m.width
	if m.reader.open && m.threePane() {
		readerWidth = listWidth * 55 / 100
		listWidth -= readerWidth + 1
	}
	m.sidebar.resize(sidebarWidth, height)
	m.list.resize(max(listWidth, 20), height)
	m.reader.resize(max(readerWidth, 20), height)
}

// --- View ---

func (m model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("")
	}
	help := m.helpView()
	height := contentHeight(m.height, help)
	rule := m.styles.chrome.Render(strings.Repeat("─", m.width))

	content := m.contentView(height)
	if m.modal != nil {
		content = overlay(content, m.modal.view(&m), m.width, height)
	}
	if m.err != nil {
		content = overlay(content, m.errorBox(), m.width, height)
	}
	if toast := m.toastView(); toast != "" {
		content = overlayAt(content, toast, max(m.width-lipgloss.Width(toast)-1, 0), 0, m.width, height)
	}

	v := tea.NewView(m.headerView() + "\n" + rule + "\n" + content + "\n" + rule + "\n" + help)
	v.AltScreen = true
	return v
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m model) headerView() string {
	left := m.styles.title.Render("mail") + "  " + m.styles.muted.Render(m.list.title())
	right := ""
	if m.anyLoading() {
		right = m.styles.active.Render(spinnerFrames[m.spinnerFrame%len(spinnerFrames)])
	}
	if m.notice != "" {
		right = m.styles.error.Render(truncate(m.notice, m.width/2)) + " " + right
	}
	gap := max(m.width-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right
}

func (m model) contentView(height int) string {
	rule := m.styles.chrome.Render(strings.TrimRight(strings.Repeat("│\n", height), "\n"))
	var panes []string
	if m.sidebarVisible() {
		panes = append(panes, m.sidebar.view(&m), rule)
	}
	if m.reader.open && !m.threePane() {
		panes = append(panes, m.reader.view(&m))
	} else {
		panes = append(panes, m.list.view(&m))
		if m.reader.open {
			panes = append(panes, rule, m.reader.view(&m))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, panes...)
}

func (m model) errorBox() string {
	text := m.styles.error.Render("Error") + "\n\n" + truncate(sanitizeLine(m.err.Error()), max(m.width-10, 20)) + "\n\n" + m.styles.muted.Render("esc to dismiss")
	return m.styles.errorFrame.Render(text)
}

func overlay(base, layer string, width, height int) string {
	x := max((width-lipgloss.Width(layer))/2, 0)
	y := max((height-lipgloss.Height(layer))/2, 0)
	return overlayAt(base, layer, x, y, width, height)
}

func overlayAt(base, layer string, x, y, width, height int) string {
	compositor := lipgloss.NewCompositor(
		lipgloss.NewLayer(base).Z(0),
		lipgloss.NewLayer(layer).X(x).Y(y).Z(1),
	)
	canvas := lipgloss.NewCanvas(width, height)
	compositor.Draw(canvas, canvas.Bounds())
	return canvas.Render()
}

// --- Data loading ---

type mailboxesLoadedMsg struct {
	requestResult
	accounts  []mail.Account
	mailboxes []mail.Mailbox
}

func (m *model) loadMailboxes() tea.Cmd {
	id, ctx := m.mailboxLane.begin(m.ctx, false)
	client := m.client.WithContext(ctx)
	return func() tea.Msg {
		accounts, err := client.Accounts()
		if err != nil {
			return mailboxesLoadedMsg{requestResult: requestResult{id, err}}
		}
		mailboxes, err := client.Mailboxes("")
		return mailboxesLoadedMsg{requestResult: requestResult{id, err}, accounts: accounts, mailboxes: mailboxes}
	}
}

func (m model) onMailboxesLoaded(msg mailboxesLoadedMsg) (tea.Model, tea.Cmd) {
	cmd, ok := m.mailboxLane.settle(msg.requestResult)
	if !ok {
		return m, cmd
	}
	first := len(m.sidebar.entries) == 0
	changed := m.sidebar.setData(msg.accounts, msg.mailboxes)
	if first {
		if err := m.sidebar.selectInitial(m.opts.Account, m.opts.Mailbox); err != nil {
			m.fatal = err
			return m, tea.Quit
		}
	}
	reload := first || changed || m.reloadAfterMailboxes
	m.reloadAfterMailboxes = false
	if changed && !first {
		m.list.leaveSearch()
		m.closeReader()
	}
	if reload {
		return m, m.spin(m.reloadList(false))
	}
	return m, nil
}

type messagesLoadedMsg struct {
	requestResult
	messages []mail.Message
	silent   bool
	source   listSource
}

// pageLoadedMsg carries an older page of the current mailbox.
type pageLoadedMsg struct {
	requestResult
	messages []mail.Message
	source   listSource
	pageSize int
}

// reloadList fetches the current mailbox or re-runs the current search.
// silent refreshes keep the cursor and show no spinner; they follow a
// mutation.
func (m *model) reloadList(silent bool) tea.Cmd {
	if m.list.source.search != "" {
		return m.runSearch(m.list.source.search, silent)
	}
	// A background refresh yields to a search still in flight; a mailbox
	// the user chose supersedes it.
	if m.searchLane.loading && silent {
		return nil
	}
	m.searchLane.abandon()
	source := m.sidebar.current().source()
	m.pageLane.abandon()
	id, ctx := m.listLane.begin(m.ctx, silent)
	client := m.client.WithContext(ctx)
	limit := m.list.pageSize()
	return func() tea.Msg {
		messages, err := listPage(client, source, limit, 0)
		return messagesLoadedMsg{requestResult: requestResult{id, err}, messages: messages, silent: silent, source: source}
	}
}

func listPage(client *mail.Client, source listSource, limit, offset int) ([]mail.Message, error) {
	if source.unified {
		return client.ListUnified("inbox", limit, offset)
	}
	return client.ListMessages(mail.MailboxListRequest{AccountName: source.account, MailboxName: source.mailbox, Limit: limit, Offset: offset})
}

// loadMore fetches the page after the last loaded row. It is a separate
// lane so it never cancels, and is never mistaken for, a reload.
func (m *model) loadMore() tea.Cmd {
	if !m.list.nearEnd() || m.pageLane.loading || m.listLane.loading {
		return nil
	}
	source := m.list.source
	offset := len(m.list.messages)
	limit := m.list.pageSize()
	id, ctx := m.pageLane.begin(m.ctx, false)
	client := m.client.WithContext(ctx)
	return func() tea.Msg {
		messages, err := listPage(client, source, limit, offset)
		return pageLoadedMsg{requestResult: requestResult{id, err}, messages: messages, source: source, pageSize: limit}
	}
}

func (m model) onPageLoaded(msg pageLoadedMsg) (tea.Model, tea.Cmd) {
	cmd, ok := m.pageLane.settle(msg.requestResult)
	if !ok || msg.source != m.list.source {
		return m, cmd
	}
	m.list.appendPage(msg.messages, msg.pageSize)
	if id := m.pendingOpen; id != "" {
		return m, m.openPending(id, msg.source)
	}
	return m, nil
}

// openPending opens the --message target once it is loaded, paging further
// while the mailbox has more.
func (m *model) openPending(id string, source listSource) tea.Cmd {
	if m.list.jumpToID(id) {
		m.pendingOpen = ""
		return m.openReader()
	}
	if m.list.hasMore {
		m.list.cursor = max(len(m.list.messages)-1, 0)
		return m.loadMore()
	}
	m.pendingOpen = ""
	return notifyProblem("message " + id + " is not in " + source.label())
}

func (m model) onMessagesLoaded(msg messagesLoadedMsg) (tea.Model, tea.Cmd) {
	cmd, ok := m.listLane.settle(msg.requestResult)
	if !ok {
		return m, cmd
	}
	m.list.setMessages(msg.messages, msg.silent, msg.source)
	if !strings.HasPrefix(m.notice, "slow mode") {
		m.notice = ""
	}
	var cmds []tea.Cmd
	if id := m.pendingOpen; id != "" {
		cmds = append(cmds, m.openPending(id, msg.source))
	}
	if m.reader.open {
		cmds = append(cmds, m.requestBody())
	}
	return m, tea.Batch(cmds...)
}

// --- Bodies ---

type bodyLoadedMsg struct {
	requestResult
	key     string
	message *mail.Message
}

func (m *model) currentKey() string {
	msg := m.list.current()
	if msg == nil {
		return ""
	}
	return bodyKey(*msg)
}

func bodyKey(msg mail.Message) string {
	return msg.Account + "\x00" + msg.Mailbox + "\x00" + msg.ID
}

// requestBody debounces a body fetch so scrolling does not queue one
// automation call per row.
func (m *model) requestBody() tea.Cmd {
	key := m.currentKey()
	if key == "" {
		return nil
	}
	if cached, ok := m.reader.cached(key); ok {
		// A fetch still in flight for the previous row must not report
		// against this one.
		m.bodyLane.abandon()
		m.reader.show(cached)
		if !cached.Read {
			return m.markCurrentRead()
		}
		return nil
	}
	m.reader.showPlaceholder(m.list.current())
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return bodyDebounceMsg{key: key} })
}

func (m *model) loadBody() tea.Cmd {
	current := m.list.current()
	if current == nil {
		return nil
	}
	key := bodyKey(*current)
	id, ctx := m.bodyLane.begin(m.ctx, false)
	client := m.client.WithContext(ctx)
	account, mailbox, msgID := current.Account, current.Mailbox, current.ID
	return func() tea.Msg {
		details, err := client.MessageDetails(account, mailbox, msgID)
		if err == nil && details == nil {
			err = fmt.Errorf("message %s is no longer in %s/%s", msgID, account, mailbox)
		}
		return bodyLoadedMsg{requestResult: requestResult{id, err}, key: key, message: details}
	}
}

func (m model) onBodyLoaded(msg bodyLoadedMsg) (tea.Model, tea.Cmd) {
	if !m.bodyLane.accepts(msg.requestResult) {
		return m, nil
	}
	m.bodyLane.finish()
	if msg.err != nil {
		if m.pendingCompose != nil && m.pendingCompose.key == msg.key {
			m.pendingCompose = nil
			return m, notifyError("could not load the message to quote", msg.err)
		}
		if m.currentKey() == msg.key {
			m.reader.showError(msg.err)
		}
		return m, nil
	}
	m.reader.remember(msg.key, msg.message)
	if pending := m.pendingCompose; pending != nil {
		m.pendingCompose = nil
		if pending.key == msg.key && m.modal == nil {
			return m, m.composeFor(pending.mode, msg.message)
		}
	}
	if m.reader.open && m.currentKey() == msg.key {
		m.reader.show(msg.message)
		if !msg.message.Read {
			// Reading marks read, the way every mail client does.
			return m, m.markCurrentRead()
		}
	}
	return m, nil
}

func (m *model) openReader() tea.Cmd {
	if m.list.current() == nil {
		return nil
	}
	m.reader.open = true
	m.focus = focusReader
	m.layout()
	return m.requestBody()
}

func (m *model) closeReader() {
	m.reader.open = false
	m.bodyLane.abandon()
	m.pendingCompose = nil
	m.focus = focusList
	m.layout()
}

func (m *model) scheduleRefresh() tea.Cmd {
	m.refreshID++
	id := m.refreshID
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return refreshDueMsg{id: id} })
}
