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
}

type focusPane int

const (
	focusSidebar focusPane = iota
	focusList
	focusReader
)

type errMsg struct{ err error }

func (e errMsg) Error() string { return e.err.Error() }

type spinnerTickMsg struct{}

// warnMsg carries a pkg/mail warning (index fallback, content budget) that
// would otherwise be printed over the screen.
type warnMsg struct{ text string }
type ctrlCResetMsg struct{}
type refreshDueMsg struct{ id uint64 }
type bodyDebounceMsg struct{ key string }

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
	bodyLane    requestLane
	actionLane  requestLane

	loading      bool
	spinnerFrame int
	notice       string
	err          error
	helpHidden   bool
	ctrlCOnce    bool
	refreshID    uint64

	toast   notifyMsg
	toastID uint64

	// pendingOpen is a message ID to open once its mailbox list is loaded.
	pendingOpen string
	// pendingCompose is a reply or forward waiting on the body it quotes.
	pendingCompose *composeAfterBodyMsg
	// initCmd is the first load, begun in newModel so the request lane's
	// bookkeeping lands on the model Bubble Tea keeps rather than a copy.
	initCmd tea.Cmd
}

func newModel(client *mail.Client, opts Options) model {
	ctx, cancel := context.WithCancel(context.Background())
	m := model{
		client:  client,
		ctx:     ctx,
		cancel:  cancel,
		opts:    opts,
		styles:  newStyles(),
		focus:   focusList,
		loading: true,
	}
	m.sidebar = newSidebar()
	m.list = newList()
	m.reader = newReader()
	m.pendingOpen = opts.MessageID
	m.initCmd = m.loadMailboxes()
	return m
}

// Run starts the program. It returns when the user quits.
func Run(client *mail.Client, opts Options) error {
	m := newModel(client, opts)
	p := tea.NewProgram(m)
	previousWarn := mail.Warn
	mail.Warn = func(text string) { p.Send(warnMsg{text: text}) }
	defer func() { mail.Warn = previousWarn }()
	_, err := p.Run()
	m.cancel()
	return err
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
			return m, spinnerTick()
		}
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

	case bodyDebounceMsg:
		if m.currentKey() == msg.key {
			return m, m.loadBody()
		}
		return m, nil

	case bodyLoadedMsg:
		return m.onBodyLoaded(msg)

	case mutationDoneMsg:
		return m.onMutationDone(msg)

	case searchDoneMsg:
		return m.onSearchDone(msg)

	case composeDoneMsg:
		return m.onComposeDone(msg)

	case composeAfterBodyMsg:
		pending := msg
		m.pendingCompose = &pending
		return m, nil

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
	return m.mailboxLane.loading || m.listLane.loading || m.bodyLane.loading || m.actionLane.loading
}

func (m *model) startSpinner(cmd tea.Cmd) tea.Cmd {
	return tea.Batch(cmd, spinnerTick())
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "ctrl+c" {
		if m.ctrlCOnce {
			m.cancel()
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

	if m.modal != nil {
		cmd, open := m.modal.handleKey(&m, msg)
		if !open {
			m.modal = nil
			m.layout()
		}
		return m, m.startSpinner(cmd)
	}

	switch key {
	case "?":
		m.helpHidden = !m.helpHidden
		m.layout()
		return m, nil
	case "ctrl+r":
		return m, m.startSpinner(tea.Batch(m.loadMailboxes(), m.reloadList(false)))
	case "tab":
		m.cycleFocus(1)
		return m, nil
	case "shift+tab":
		m.cycleFocus(-1)
		return m, nil
	case "/":
		m.modal = newSearchModal(m.styles)
		return m, nil
	case "c":
		return m, m.openCompose(composeNew)
	}

	if idx, ok := digitShortcut(key); ok && m.focus != focusReader {
		if cmd := m.sidebar.jumpTo(&m, idx); cmd != nil {
			return m, m.startSpinner(cmd)
		}
		return m, nil
	}

	switch m.focus {
	case focusSidebar:
		return m, m.startSpinner(m.sidebar.handleKey(&m, msg))
	case focusReader:
		return m, m.startSpinner(m.reader.handleKey(&m, msg))
	default:
		return m, m.startSpinner(m.list.handleKey(&m, msg))
	}
}

func digitShortcut(key string) (int, bool) {
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		return int(key[0] - '1'), true
	}
	return 0, false
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
func (m *model) contentHeight() int {
	h := m.height - 3 - lipgloss.Height(m.helpView())
	return max(h, 3)
}

func (m *model) layout() {
	height := m.contentHeight()
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
	var b strings.Builder
	b.WriteString(m.headerView())
	b.WriteString("\n")
	b.WriteString(m.styles.chrome.Render(strings.Repeat("─", m.width)))
	b.WriteString("\n")

	content := m.contentView()
	if m.modal != nil {
		content = overlay(content, m.modal.view(&m), m.width, m.contentHeight())
	}
	if m.err != nil {
		content = overlay(content, m.errorBox(), m.width, m.contentHeight())
	}
	if toast := m.toastView(); toast != "" {
		content = overlayAt(content, toast, max(m.width-lipgloss.Width(toast)-1, 0), 0, m.width, m.contentHeight())
	}
	b.WriteString(content)
	b.WriteString("\n" + m.styles.chrome.Render(strings.Repeat("─", m.width)))
	b.WriteString("\n" + m.helpView())

	v := tea.NewView(b.String())
	v.AltScreen = true
	return v
}

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func (m model) headerView() string {
	title := m.styles.title.Render("mail")
	where := m.styles.muted.Render(m.list.title())
	right := ""
	if m.anyLoading() {
		right = m.styles.active.Render(spinnerFrames[m.spinnerFrame%len(spinnerFrames)])
	}
	if m.notice != "" {
		right = m.styles.error.Render(truncate(m.notice, m.width/2)) + " " + right
	}
	left := title + "  " + where
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m model) contentView() string {
	panes := []string{}
	if m.sidebarVisible() {
		panes = append(panes, m.sidebar.view(&m), m.verticalRule())
	}
	if m.reader.open && !m.threePane() {
		panes = append(panes, m.reader.view(&m))
	} else {
		panes = append(panes, m.list.view(&m))
		if m.reader.open {
			panes = append(panes, m.verticalRule(), m.reader.view(&m))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, panes...)
}

func (m model) verticalRule() string {
	return m.styles.chrome.Render(strings.TrimRight(strings.Repeat("│\n", m.contentHeight()), "\n"))
}

func (m model) errorBox() string {
	text := m.styles.error.Render("Error") + "\n\n" + truncate(sanitizeLine(m.err.Error()), max(m.width-10, 20)) + "\n\n" + m.styles.muted.Render("esc to dismiss")
	return m.styles.frame.BorderForeground(m.styles.palette.alert).Render(text)
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
	id, ctx := m.mailboxLane.begin(m.ctx)
	client := m.client
	return func() tea.Msg {
		accounts, err := client.Accounts(ctx)
		if err != nil {
			return mailboxesLoadedMsg{requestResult: requestResult{id, err}}
		}
		mailboxes, err := client.Mailboxes(ctx, "")
		return mailboxesLoadedMsg{requestResult: requestResult{id, err}, accounts: accounts, mailboxes: mailboxes}
	}
}

func (m model) onMailboxesLoaded(msg mailboxesLoadedMsg) (tea.Model, tea.Cmd) {
	cmd, ok := m.mailboxLane.settle(msg.requestResult)
	if !ok {
		m.loading = false
		return m, cmd
	}
	first := len(m.sidebar.entries) == 0
	m.sidebar.setData(msg.accounts, msg.mailboxes)
	if first {
		m.sidebar.selectInitial(m.opts.Account, m.opts.Mailbox)
		m.loading = false
		return m, m.startSpinner(m.reloadList(false))
	}
	return m, nil
}

type messagesLoadedMsg struct {
	requestResult
	messages []mail.Message
	silent   bool
	source   sidebarEntry
}

// reloadList fetches the current mailbox. silent refreshes keep the cursor
// and show no spinner; they follow a mutation.
func (m *model) reloadList(silent bool) tea.Cmd {
	if m.list.mode == listSearch {
		return nil
	}
	entry := m.sidebar.current()
	id, ctx := m.listLane.begin(m.ctx)
	if silent {
		m.listLane.loading = false
	}
	client := m.client
	limit := m.list.pageSize()
	return func() tea.Msg {
		var messages []mail.Message
		var err error
		if entry.unified {
			messages, err = client.ListUnified(ctx, "inbox", limit, 0, false)
		} else {
			messages, err = client.ListMessages(ctx, mail.ListOptions{Account: entry.account, Mailbox: entry.mailbox, Limit: limit})
		}
		return messagesLoadedMsg{requestResult: requestResult{id, err}, messages: messages, silent: silent, source: entry}
	}
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
	if m.pendingOpen != "" {
		if m.list.jumpToID(m.pendingOpen) {
			m.pendingOpen = ""
			cmds = append(cmds, m.openReader())
		} else {
			m.pendingOpen = ""
			cmds = append(cmds, notify("message "+m.opts.MessageID+" is not in the first page of "+msg.source.label()))
		}
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
	if cached, ok := m.reader.cache[key]; ok {
		m.reader.show(cached, m.styles)
		return nil
	}
	m.reader.showPlaceholder(m.list.current(), m.styles)
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return bodyDebounceMsg{key: key} })
}

func (m *model) loadBody() tea.Cmd {
	current := m.list.current()
	if current == nil {
		return nil
	}
	key := bodyKey(*current)
	id, ctx := m.bodyLane.begin(m.ctx)
	client := m.client
	msg := *current
	return func() tea.Msg {
		details, err := client.MessageDetails(ctx, msg.Account, msg.Mailbox, msg.ID)
		if err == nil && details == nil {
			err = fmt.Errorf("message %s is no longer in %s/%s", msg.ID, msg.Account, msg.Mailbox)
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
		m.reader.showError(msg.err, m.styles)
		return m, nil
	}
	m.reader.cache[msg.key] = msg.message
	if m.pendingCompose != nil && m.pendingCompose.key == msg.key {
		mode := m.pendingCompose.mode
		m.pendingCompose = nil
		return m, m.openCompose(mode)
	}
	if m.currentKey() == msg.key {
		m.reader.show(msg.message, m.styles)
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
	m.focus = focusList
	m.layout()
}

func (m *model) scheduleRefresh() tea.Cmd {
	m.refreshID++
	id := m.refreshID
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return refreshDueMsg{id: id} })
}
