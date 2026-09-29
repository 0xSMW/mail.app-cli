package mail

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// phaseMarker prefixes a phase event on stderr when a script runs as a
// one-shot osascript. A bridge session carries the same event on stdout.
const phaseMarker = "mail-app-cli-phase "

// phaseEvent is written by a script as each named step starts and ends. It
// leaves the process before the step's Apple Events are sent, so the record
// survives the process being killed on timeout.
type phaseEvent struct {
	Name      string `json:"name"`
	State     string `json:"state"`
	AtMs      int64  `json:"atMs"`
	ElapsedMs int64  `json:"elapsedMs"`
	Note      string `json:"note,omitempty"`
	Error     string `json:"error,omitempty"`
}

// AutomationPhase is one finished step of a bridge script.
type AutomationPhase struct {
	Name      string `json:"name"`
	ElapsedMs int64  `json:"elapsedMs"`
	Note      string `json:"note,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PhaseTrace says how far a bridge script got. Pending names the step that
// had started but not returned when the trace was taken.
type PhaseTrace struct {
	Phases        []AutomationPhase `json:"phases"`
	LastCompleted string            `json:"lastCompleted,omitempty"`
	Pending       string            `json:"pending,omitempty"`
	PendingMs     int64             `json:"pendingMs,omitempty"`
}

func buildPhaseTrace(events []phaseEvent, now time.Time) *PhaseTrace {
	if len(events) == 0 {
		return nil
	}
	trace := &PhaseTrace{Phases: []AutomationPhase{}}
	var pending *phaseEvent
	for i := range events {
		event := events[i]
		switch event.State {
		case "start":
			pending = &events[i]
		case "done", "failed":
			trace.Phases = append(trace.Phases, AutomationPhase{Name: event.Name, ElapsedMs: event.ElapsedMs, Note: event.Note, Error: event.Error})
			if event.State == "done" {
				trace.LastCompleted = event.Name
			}
			pending = nil
		}
	}
	if pending != nil {
		trace.Pending = pending.Name
		if elapsed := now.UnixMilli() - pending.AtMs; pending.AtMs > 0 && elapsed > 0 {
			trace.PendingMs = elapsed
		}
	}
	return trace
}

// splitPhaseEvents separates phase events from the rest of a one-shot
// script's stderr, so diagnostics never leak into an error message.
func splitPhaseEvents(stderr string) ([]phaseEvent, string) {
	if !strings.Contains(stderr, phaseMarker) {
		return nil, stderr
	}
	var events []phaseEvent
	var rest []string
	for _, line := range strings.Split(stderr, "\n") {
		payload, ok := strings.CutPrefix(line, phaseMarker)
		if !ok {
			rest = append(rest, line)
			continue
		}
		var event phaseEvent
		// A kill can cut the last line short; an unreadable event is dropped.
		if err := json.Unmarshal([]byte(payload), &event); err == nil && event.Name != "" {
			events = append(events, event)
		}
	}
	return events, strings.Join(rest, "\n")
}

func (t *PhaseTrace) describe() string {
	if t == nil {
		return ""
	}
	var parts []string
	if t.Pending != "" {
		part := "during " + t.Pending
		if t.PendingMs > 0 {
			part += fmt.Sprintf(" (%s in phase)", (time.Duration(t.PendingMs) * time.Millisecond).Round(time.Millisecond))
		}
		parts = append(parts, part)
	}
	if t.LastCompleted != "" {
		parts = append(parts, "last completed "+t.LastCompleted)
	} else {
		parts = append(parts, "no phase completed")
	}
	return strings.Join(parts, "; ")
}

// BridgePhaseError is a Mail.app error raised inside a named script phase.
type BridgePhaseError struct {
	Engine  string
	Phase   string
	Message string
	Trace   *PhaseTrace
}

func (e *BridgePhaseError) Error() string {
	if e.Phase == "" {
		return fmt.Sprintf("%s error: %s", e.Engine, e.Message)
	}
	return fmt.Sprintf("%s error during %s: %s", e.Engine, e.Phase, e.Message)
}

// jxaPhaseHelper defines phase(name, fn), which runs fn as a named step. The
// step's start is announced before fn runs. phaseLog collects finished steps
// for the script's own result.
func jxaPhaseHelper() string {
	return `
ObjC.import('Foundation');
const phaseLog = [];
let currentPhase = '';

function describeError(e) {
	let text = String(e && e.message ? e.message : e);
	if (e && e.errorNumber !== undefined && text.indexOf('(' + e.errorNumber + ')') < 0) {
		text += ' (' + e.errorNumber + ')';
	}
	return text;
}

function emitPhase(event) {
	event.atMs = Date.now();
	if (typeof reply === 'function') {
		reply({phase: event});
		return;
	}
	const line = $('` + phaseMarker + `' + JSON.stringify(event) + '\n').dataUsingEncoding($.NSUTF8StringEncoding);
	$.NSFileHandle.fileHandleWithStandardError.writeData(line);
}

function phase(name, fn) {
	const started = Date.now();
	const step = {name: name};
	currentPhase = name;
	emitPhase({name: name, state: 'start'});
	try {
		const value = fn(step);
		step.elapsedMs = Date.now() - started;
		phaseLog.push(step);
		emitPhase({name: name, state: 'done', elapsedMs: step.elapsedMs, note: step.note});
		return value;
	} catch (e) {
		step.elapsedMs = Date.now() - started;
		step.error = describeError(e);
		phaseLog.push(step);
		emitPhase({name: name, state: 'failed', elapsedMs: step.elapsedMs, error: step.error});
		throw e;
	}
}
`
}
