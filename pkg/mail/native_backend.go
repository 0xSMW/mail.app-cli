package mail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

const BackendNative = "native"
const BackendLegacy = "legacy"

func (c *Client) SetBackend(backend string) error {
	backend = strings.ToLower(strings.TrimSpace(backend))
	if backend == "" {
		backend = strings.ToLower(strings.TrimSpace(os.Getenv("MAIL_APP_CLI_BACKEND")))
	}
	if backend != "" && backend != "auto" && backend != BackendNative && backend != BackendLegacy {
		return fmt.Errorf("backend must be native, legacy, or auto")
	}
	c.shared.backend = backend
	return nil
}

func (c *Client) Backend() string {
	mode := c.shared.backend
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(os.Getenv("MAIL_APP_CLI_BACKEND")))
	}
	if mode == BackendLegacy {
		return BackendLegacy
	}
	if mode == BackendNative || c.shared.nativeSender != nil || appleevents.Available() {
		return BackendNative
	}
	return BackendLegacy
}

func (c *Client) nativeEnabled() bool { return c.Backend() == BackendNative }

// nativeMail owns a serialized, bounded operation. The same lock protects all
// its events, so resolution, dispatch, readback, and cleanup cannot interleave.
type nativeMail struct {
	client          *Client
	ctx             context.Context
	sender          appleevents.Sender
	phases          []AutomationPhase
	phaseName       string
	phaseStarted    time.Time
	splitProperties bool
}

func (c *Client) withNativeAutomation(timeout time.Duration, fn func(*nativeMail) error) error {
	if err := c.Done(); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = defaultAutomationTimeout
	}
	lockCtx, cancelLock := context.WithTimeout(c.Context(), automationLockTimeout)
	defer cancelLock()
	if err := acquireAutomationGate(lockCtx); err != nil {
		return automationLockError("native", c.Context(), err)
	}
	defer releaseAutomationGate()
	release, err := acquireAutomationProcessLock(lockCtx)
	if err != nil {
		return automationLockError("native", c.Context(), err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(c.Context(), timeout)
	defer cancel()
	sender := c.shared.nativeSender
	if sender == nil {
		sender = appleevents.Send
	}
	n := &nativeMail{client: c, ctx: ctx, sender: sender}
	err = fn(n)
	var archiveErr *NativeArchiveError
	var mutationErr *MutationError
	if errors.As(err, &archiveErr) || errors.As(err, &mutationErr) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		trace := &PhaseTrace{Phases: n.phases, Pending: n.phaseName}
		if len(n.phases) > 0 {
			trace.LastCompleted = n.phases[len(n.phases)-1].Name
		}
		if n.phaseName != "" {
			trace.PendingMs = time.Since(n.phaseStarted).Milliseconds()
		}
		// Preserve the dispatch cause for mutation code that reconciles uncertain outcomes.
		return &AutomationTimeoutError{Engine: "native", Timeout: timeout, Trace: trace, Cause: err}
	}
	return err
}

func (n *nativeMail) phase(name string, fn func() error) error {
	n.phaseName = name
	started := time.Now()
	n.phaseStarted = started
	err := fn()
	if err == nil {
		n.phases = append(n.phases, AutomationPhase{Name: name, ElapsedMs: time.Since(started).Milliseconds()})
		n.phaseName = ""
	}
	return err
}

func (n *nativeMail) event(class, id string, params map[string]appleevents.Value) (appleevents.Value, error) {
	if err := n.ctx.Err(); err != nil {
		return appleevents.Value{}, err
	}
	if class == "core" && id == "getd" && requestsNativeContent(params["----"]) {
		nativeContentRequestCount.Add(1)
	}
	v, stats, err := n.sender(n.ctx, appleevents.Event{Class: class, ID: id, Params: params, Timeout: defaultAutomationTimeout})
	recordNativeEvent(stats)
	return v, err
}

func requestsNativeContent(v appleevents.Value) bool {
	if v.Kind == "object" && v.Fields["want"].Code == "prop" && v.Fields["seld"].Code == "ctnt" {
		return true
	}
	for _, item := range v.Items {
		if requestsNativeContent(item) {
			return true
		}
	}
	for _, field := range v.Fields {
		if requestsNativeContent(field) {
			return true
		}
	}
	return false
}
func (n *nativeMail) get(object appleevents.Value) (appleevents.Value, error) {
	return n.event("core", "getd", map[string]appleevents.Value{"----": object})
}
func (n *nativeMail) set(object, value appleevents.Value) error {
	_, err := n.event("core", "setd", map[string]appleevents.Value{"----": object, "data": value})
	return err
}
func (n *nativeMail) command(class, id string, params map[string]appleevents.Value) (appleevents.Value, error) {
	return n.event(class, id, params)
}
func (n *nativeMail) stringProperty(code string, object appleevents.Value) (string, error) {
	v, err := n.get(appleevents.Property(code, object))
	if err != nil {
		return "", err
	}
	if v.Kind != "string" {
		return "", fmt.Errorf("Mail property %s returned %s; expected string", code, v.Kind)
	}
	return v.Text, nil
}
func (n *nativeMail) intProperty(code string, object appleevents.Value) (int64, error) {
	v, err := n.get(appleevents.Property(code, object))
	if err != nil {
		return 0, err
	}
	if v.Kind != "int" {
		return 0, fmt.Errorf("Mail property %s returned %s; expected integer", code, v.Kind)
	}
	return v.Number, nil
}
func (n *nativeMail) boolProperty(code string, object appleevents.Value) (bool, error) {
	v, err := n.get(appleevents.Property(code, object))
	if err != nil {
		return false, err
	}
	if v.Kind != "bool" {
		return false, fmt.Errorf("Mail property %s returned %s; expected boolean", code, v.Kind)
	}
	return v.Boolean, nil
}

func nativeMissing(err error) bool {
	var e *appleevents.Error
	return errors.As(err, &e) && e.Code == -1728
}

// getFields first asks Mail to resolve a list of property specifiers in one
// read. Older targets can refuse this shape; only that read is split into
// separate property requests. Transport and permission errors stay failures.
func (n *nativeMail) getFields(object appleevents.Value, codes ...string) (map[string]appleevents.Value, error) {
	refs := make([]appleevents.Value, len(codes))
	for i, code := range codes {
		refs[i] = appleevents.Property(code, object)
	}
	var v appleevents.Value
	var err error
	if !n.splitProperties {
		v, err = n.get(appleevents.List(refs...))
	}
	if err == nil && v.Kind == "list" && len(v.Items) == len(codes) {
		out := make(map[string]appleevents.Value, len(codes))
		for i, code := range codes {
			out[code] = v.Items[i]
		}
		return out, nil
	}
	if err != nil {
		var ae *appleevents.Error
		if !errors.As(err, &ae) || (ae.Code != -1708 && ae.Code != -1700) {
			return nil, err
		}
		n.splitProperties = true
	}
	out := make(map[string]appleevents.Value, len(codes))
	for _, code := range codes {
		value, e := n.get(appleevents.Property(code, object))
		if e != nil {
			return nil, e
		}
		out[code] = value
	}
	return out, nil
}
