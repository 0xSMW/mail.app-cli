package mail

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	defaultAutomationTimeout     = 30 * time.Second
	defaultAutomationLockTimeout = 30 * time.Second
	automationWaitDelay          = 2 * time.Second
)

// automationGate serializes every Mail.app automation subprocess in this
// process. A file lock extends that serialization to separate CLI processes.
// Mail.app's AppleScript/JXA bridge is not reliable under concurrent requests.
var automationGate = make(chan struct{}, 1)

// automationLockTimeout bounds queueing behind another mail-app-cli process.
// It is intentionally independent of a command's execution deadline: once an
// operation obtains the lock, it receives its full execution budget.
var automationLockTimeout = defaultAutomationLockTimeout

// AutomationTimeoutError reports a bounded Mail.app automation invocation that
// did not complete. It unwraps to context.DeadlineExceeded so callers can
// classify it with errors.Is, and exposes the engine and configured timeout for
// human-readable diagnostics. Trace is set when the script announced phases.
type AutomationTimeoutError struct {
	Engine  string
	Timeout time.Duration
	Trace   *PhaseTrace
	Cause   error
}

func (e *AutomationTimeoutError) Error() string {
	message := fmt.Sprintf("%s timed out after %s", e.Engine, e.Timeout)
	if detail := e.Trace.describe(); detail != "" {
		message += " " + detail
	}
	return message
}

func (e *AutomationTimeoutError) Unwrap() error {
	if e.Cause != nil {
		return e.Cause
	}
	return context.DeadlineExceeded
}

// AutomationLockTimeoutError reports a command that could not enter the
// serialized automation queue within its bounded wait period.
type AutomationLockTimeoutError struct {
	Engine  string
	Timeout time.Duration
}

func (e *AutomationLockTimeoutError) Error() string {
	return fmt.Sprintf("%s automation lock timed out after %s", e.Engine, e.Timeout)
}

func (e *AutomationLockTimeoutError) Unwrap() error {
	return context.DeadlineExceeded
}

func escapeJSString(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r >= 0x20 && r <= 0x7e {
				b.WriteRune(r)
				continue
			}
			if r <= 0xffff {
				fmt.Fprintf(&b, `\u%04X`, r)
				continue
			}
			r -= 0x10000
			high := 0xd800 + (r >> 10)
			low := 0xdc00 + (r & 0x3ff)
			fmt.Fprintf(&b, `\u%04X\u%04X`, high, low)
		}
	}
	return b.String()
}

func escapeAppleScriptString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\") // Escape backslashes first
	s = strings.ReplaceAll(s, "\"", "\\\"") // Escape double quotes
	return s
}

// jxaRFCMessageIDHelper reads Mail's optional messageId property without
// confusing its numeric local id with an RFC Message-ID header.
func jxaRFCMessageIDHelper() string {
	return `
function rfcMessageId(msg) {
	try {
        const raw = String(msg.messageId() || '');
        if (/[\x00-\x1f\x7f]/.test(raw)) return '';
        let value = raw.trim();
        if (value.startsWith('<') && value.endsWith('>')) value = value.slice(1, -1);
        const parts = value.split('@');
        if (parts.length !== 2) return '';
        const atom = /^[A-Za-z0-9!#$%&'*+\/=?^_\x60{|}~.\-]+$/;
        if (parts.some(p => !p || p.startsWith('.') || p.endsWith('.') || p.includes('..') || !atom.test(p))) return '';
        return '<' + value + '>';
	} catch (e) { return ''; }
}
`
}

func (c *Client) runAppleScript(script string) (string, error) {
	return runAutomation(c.Context(), "applescript", defaultAutomationTimeout, "-e", script)
}

func (c *Client) runJXA(script string) (string, error) {
	return c.runJXAWithTimeout(script, defaultAutomationTimeout)
}

func (c *Client) runJXAWithTimeout(script string, timeout time.Duration) (string, error) {
	if c.session != nil {
		return c.session.run(script, timeout)
	}
	return runAutomation(c.Context(), "jxa", timeout, "-l", "JavaScript", "-e", script)
}

func runAutomation(parent context.Context, engine string, timeout time.Duration, args ...string) (string, error) {
	lockCtx, cancelLockWait := context.WithTimeout(parent, automationLockTimeout)
	defer cancelLockWait()

	if err := acquireAutomationGate(lockCtx); err != nil {
		return "", automationLockError(engine, parent, err)
	}
	defer releaseAutomationGate()

	releaseProcessLock, err := acquireAutomationProcessLock(lockCtx)
	if err != nil {
		return "", automationLockError(engine, parent, err)
	}
	defer releaseProcessLock()

	execution, err := runAutomationUnlocked(parent, engine, timeout, args...)
	if err != nil {
		return "", err
	}
	return execution.Output, nil
}

type automationExecution struct {
	Output string
	Events []phaseEvent
}

// runAutomationUnlocked executes while its caller holds both automationGate
// and the automation process lock. It never acquires or releases either lock,
// allowing a transaction to run bounded cleanup with context.WithoutCancel
// before releasing ownership. Completed phase records survive execution errors
// and caller cancellation so the caller can recover resources it created.
func runAutomationUnlocked(parent context.Context, engine string, timeout time.Duration, args ...string) (automationExecution, error) {
	if timeout <= 0 {
		timeout = defaultAutomationTimeout
	}

	executionCtx, cancelExecution := context.WithTimeout(parent, timeout)
	defer cancelExecution()

	cmd := exec.CommandContext(executionCtx, "osascript", args...)
	// osascript can leave helper processes behind. Put each invocation in its
	// own process group so a timeout can terminate the complete operation.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil || errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return cmd.Process.Kill()
	}
	// Bound Wait even if a descendant retains inherited descriptors after the
	// leader is cancelled. CommandContext invokes Cancel before this delay.
	cmd.WaitDelay = automationWaitDelay
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return automationExecution{}, fmt.Errorf("%s error: %v - %s", engine, err, stderr.String())
	}

	started := time.Now()
	err := cmd.Wait()
	recordLegacyProcess(time.Since(started))
	events, diagnostics := splitPhaseEvents(stderr.String())
	execution := automationExecution{Output: strings.TrimSpace(out.String()), Events: events}
	if parentErr := parent.Err(); parentErr != nil {
		return execution, parentErr
	}
	if errors.Is(executionCtx.Err(), context.DeadlineExceeded) {
		return execution, &AutomationTimeoutError{Engine: engine, Timeout: timeout, Trace: buildPhaseTrace(events, time.Now())}
	}
	if err != nil {
		return execution, fmt.Errorf("%s error: %v - %s", engine, err, diagnostics)
	}
	return execution, nil
}

// automationLockError classifies a failure to enter the automation queue.
// The caller's own cancellation or deadline is returned as is; only the
// queue's wait budget running out is a lock timeout.
func automationLockError(engine string, parent context.Context, err error) error {
	if parentErr := parent.Err(); parentErr != nil {
		return parentErr
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &AutomationLockTimeoutError{Engine: engine, Timeout: automationLockTimeout}
	}
	return fmt.Errorf("%s automation lock: %w", engine, err)
}

func acquireAutomationGate(ctx context.Context) error {
	select {
	case automationGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseAutomationGate() {
	<-automationGate
}

func automationLockPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH")); override != "" {
		return override, nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("find user cache directory: %w", err)
	}
	return filepath.Join(cacheDir, "mail-app-cli", "automation.lock"), nil
}

func acquireAutomationProcessLock(ctx context.Context) (func(), error) {
	path, err := automationLockPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create automation lock directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open automation lock: %w", err)
	}

	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("acquire automation lock: %w", err)
		}

		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func jxaBool(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func jxaMailboxLookupExpression(mailboxName string) string {
	return jxaMailboxLookupExpressionFor(mailboxName, "requestedMailbox")
}

func jxaMailboxLookupExpressionFor(mailboxName, variableName string) string {
	if isArchiveAlias(mailboxName) {
		return fmt.Sprintf("findMailbox(acc, %s, ['All Mail', 'Archive'])", variableName)
	}
	return fmt.Sprintf("findMailbox(acc, %s, [%s])", variableName, variableName)
}

func jxaMailboxLookupHelper() string {
	return `
// A timed-out or severed Apple Event says nothing about whether the object
// exists, so lookups rethrow it instead of reporting the object missing.
function isTransportError(e) {
	const number = e ? e.errorNumber : undefined;
	return number === -1712 || number === -609 || number === -600;
}

function isInboxName(name) {
	return String(name || '').toLowerCase() === 'inbox';
}

function findMailbox(acc, requestedName, names) {
	if (isInboxName(requestedName)) {
		try {
			const inbox = acc.inbox();
			if (inbox !== null && inbox !== undefined && inbox.name()) return inbox;
		} catch (e) { if (isTransportError(e)) throw e; }
		// Exchange can expose a null inbox accessor while listing Inbox normally.
		names = names.concat(['Inbox', 'INBOX']);
	}
	let found;
	try {
		found = findMailboxByBulkNames(acc, names);
	} catch (e) {
		if (isTransportError(e)) throw e;
		found = findMailboxByNames(acc.mailboxes(), names);
	}
	if (found !== null) {
		return found;
	}
	try {
		const byName = acc.mailboxes.byName(requestedName);
		byName.name();
		return byName;
	} catch (e) { if (isTransportError(e)) throw e; }
	return null;
}

// findMailboxByBulkNames visits mailboxes in the same order as
// findMailboxByNames, but reads each level's names in one Apple Event and
// only descends into mailboxes that have children. It throws when Mail.app
// answers inconsistently, and the caller then walks one mailbox at a time.
function findMailboxByBulkNames(container, names) {
	const list = container.mailboxes;
	const levelNames = list.name();
	if (levelNames.length === 0) {
		return null;
	}
	const childNames = list.mailboxes.name();
	if (childNames.length !== levelNames.length) {
		throw new Error('mailbox list changed during lookup');
	}
	for (let i = 0; i < levelNames.length; i++) {
		if (names.includes(levelNames[i])) {
			const mailbox = list[i];
			if (mailbox.name() !== levelNames[i]) {
				throw new Error('mailbox list changed during lookup');
			}
			return mailbox;
		}
		if (childNames[i] && childNames[i].length > 0) {
			const child = findMailboxByBulkNames(list[i], names);
			if (child !== null) {
				return child;
			}
		}
	}
	return null;
}

function findMailboxByNames(mailboxes, names) {
	for (let i = 0; i < mailboxes.length; i++) {
		const mailbox = mailboxes[i];
		try {
			if (names.includes(mailbox.name())) {
				return mailbox;
			}
			const child = findMailboxByNames(mailbox.mailboxes(), names);
			if (child !== null) {
				return child;
			}
		} catch (e) { if (isTransportError(e)) throw e; }
	}
	return null;
}
`
}

func jxaMessageByIdHelper() string {
	return `
function messageById(mbox, messageId) {
	let msg = null;
	try {
		msg = mbox.messages.byId(Number(messageId));
		msg.id();
		return msg;
	} catch (e) {
		msg = null;
	}
	try {
		const allIds = mbox.messages.id();
		const targetIdx = allIds.findIndex(id => String(id) === messageId);
		if (targetIdx >= 0) {
			return mbox.messages.at(targetIdx);
		}
	} catch (e) {}
	return null;
}
`
}

func appleScriptBool(value bool) string {
	return jxaBool(value)
}

func appleScriptStringList(values []string) string {
	escaped := make([]string, 0, len(values))
	for _, value := range values {
		escaped = append(escaped, `"`+escapeAppleScriptString(value)+`"`)
	}
	return strings.Join(escaped, ", ")
}
