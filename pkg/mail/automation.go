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
// human-readable diagnostics.
type AutomationTimeoutError struct {
	Engine  string
	Timeout time.Duration
}

func (e *AutomationTimeoutError) Error() string {
	return fmt.Sprintf("%s timed out after %s", e.Engine, e.Timeout)
}

func (e *AutomationTimeoutError) Unwrap() error {
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

func (c *Client) runAppleScript(script string) (string, error) {
	return runAutomation("applescript", defaultAutomationTimeout, "-e", script)
}

func (c *Client) runJXA(script string) (string, error) {
	return runAutomation("jxa", defaultAutomationTimeout, "-l", "JavaScript", "-e", script)
}

func (c *Client) runJXAWithTimeout(script string, timeout time.Duration) (string, error) {
	return runAutomation("jxa", timeout, "-l", "JavaScript", "-e", script)
}

func runAutomation(engine string, timeout time.Duration, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = defaultAutomationTimeout
	}

	lockCtx, cancelLockWait := context.WithTimeout(context.Background(), automationLockTimeout)
	defer cancelLockWait()

	if err := acquireAutomationGate(lockCtx); err != nil {
		return "", automationLockError(engine, err)
	}
	defer releaseAutomationGate()

	releaseProcessLock, err := acquireAutomationProcessLock(lockCtx)
	if err != nil {
		return "", automationLockError(engine, err)
	}
	defer releaseProcessLock()

	executionCtx, cancelExecution := context.WithTimeout(context.Background(), timeout)
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
		return "", fmt.Errorf("%s error: %v - %s", engine, err, stderr.String())
	}

	err = cmd.Wait()
	if errors.Is(executionCtx.Err(), context.DeadlineExceeded) {
		return "", &AutomationTimeoutError{Engine: engine, Timeout: timeout}
	}
	if err != nil {
		return "", fmt.Errorf("%s error: %v - %s", engine, err, stderr.String())
	}
	return strings.TrimSpace(out.String()), nil
}

func automationLockError(engine string, err error) error {
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
function isInboxName(name) {
	return String(name || '').toLowerCase() === 'inbox';
}

function findMailbox(acc, requestedName, names) {
	if (isInboxName(requestedName)) {
		try { return acc.inbox(); } catch (e) {}
	}
	const found = findMailboxByNames(acc.mailboxes(), names);
	if (found !== null) {
		return found;
	}
	try {
		const byName = acc.mailboxes.byName(requestedName);
		byName.name();
		return byName;
	} catch (e) {}
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
		} catch (e) {}
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
	if value {
		return "true"
	}
	return "false"
}

func appleScriptStringList(values []string) string {
	escaped := make([]string, 0, len(values))
	for _, value := range values {
		escaped = append(escaped, `"`+escapeAppleScriptString(value)+`"`)
	}
	return strings.Join(escaped, ", ")
}

func appleScriptRecipientBlock(kind string, values []string) string {
	if len(values) == 0 {
		return ""
	}
	return fmt.Sprintf(`
		repeat with addr in {%s}
			make new %s recipient at end of %s recipients with properties {address:addr}
		end repeat
`, appleScriptStringList(values), kind, kind)
}
