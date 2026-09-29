package mail

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestSessionTimeoutNamesPendingPhase(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `while IFS= read -r line; do
 printf '{"phase":{"name":"resolve_mailbox","state":"start","atMs":1}}\n'
 printf '{"phase":{"name":"resolve_mailbox","state":"done","atMs":2,"elapsedMs":40}}\n'
 printf '{"phase":{"name":"content","state":"start","atMs":3}}\n'
 /bin/sleep 30
done
`)
	s, err := newJXASession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = s.run("slow", 2*time.Second)
	var timeout *AutomationTimeoutError
	if !errors.As(err, &timeout) || timeout.Trace == nil {
		t.Fatalf("err = %v, want a traced timeout", err)
	}
	trace := timeout.Trace
	if trace.Pending != "content" || trace.LastCompleted != "resolve_mailbox" || len(trace.Phases) != 1 || trace.Phases[0].ElapsedMs != 40 || trace.PendingMs <= 0 {
		t.Fatalf("trace = %+v", trace)
	}
	if !strings.Contains(err.Error(), "during content") || !strings.Contains(err.Error(), "last completed resolve_mailbox") {
		t.Fatalf("message = %q", err)
	}
}

func TestSessionSkipsPhaseEventsBeforeResponse(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `while IFS= read -r line; do
 printf '{"phase":{"name":"content","state":"start","atMs":1}}\n'
 printf '{"phase":{"name":"content","state":"done","atMs":2,"elapsedMs":5}}\n'
 printf '{"output":"body"}\n'
done
`)
	s, err := newJXASession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		if out, err := s.run("read", 5*time.Second); err != nil || out != "body" {
			t.Fatalf("run %d = %q, %v", i, out, err)
		}
	}
}

func TestOneShotTimeoutNamesPendingPhase(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `printf 'mail-app-cli-phase {"name":"lookup_by_id","state":"start","atMs":1}\n' >&2
printf 'mail-app-cli-phase {"name":"lookup_by_id","state":"done","atMs":2,"elapsedMs":9,"note":"miss: no such object (-1728)"}\n' >&2
printf 'mail-app-cli-phase {"name":"enumerate_ids","state":"start","atMs":3}\n' >&2
/bin/sleep 30
`)
	_, trace, err := NewClient().getMessageDetailsTraced("Work", "Spam", "42", 2*time.Second)
	if !errors.Is(err, context.DeadlineExceeded) || trace == nil || trace.Pending != "enumerate_ids" || trace.LastCompleted != "lookup_by_id" || !strings.Contains(trace.Phases[0].Note, "-1728") {
		t.Fatalf("trace = %+v, err = %v", trace, err)
	}
}

func TestOneShotErrorOmitsPhaseEvents(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `printf 'mail-app-cli-phase {"name":"content","state":"start","atMs":1}\n' >&2
printf 'execution error: not authorized (-1743)\n' >&2
exit 1
`)
	_, err := NewClient().runJXA("script")
	if err == nil || strings.Contains(err.Error(), "mail-app-cli-phase") || !strings.Contains(err.Error(), "-1743") {
		t.Fatalf("err = %v", err)
	}
}

func TestDetailReadSeparatesAbsentFromFailed(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	tests := []struct {
		name   string
		output string
		check  func(*testing.T, *Message, *PhaseTrace, error)
	}{
		{"absent", `{"message":null,"failure":null,"phases":[{"name":"lookup_by_id","elapsedMs":3,"note":"miss"},{"name":"enumerate_ids","elapsedMs":20,"note":"12 ids"}]}`, func(t *testing.T, message *Message, trace *PhaseTrace, err error) {
			if err != nil || message != nil || trace.LastCompleted != "enumerate_ids" {
				t.Fatalf("got %+v, %+v, %v", message, trace, err)
			}
		}},
		{"automation error", `{"message":null,"failure":{"phase":"content","message":"AppleEvent timed out. (-1712)","number":-1712},"phases":[{"name":"lookup_by_id","elapsedMs":3},{"name":"content","elapsedMs":900,"error":"AppleEvent timed out. (-1712)"}]}`, func(t *testing.T, message *Message, trace *PhaseTrace, err error) {
			var phaseErr *BridgePhaseError
			if !errors.As(err, &phaseErr) || phaseErr.Phase != "content" || message != nil || trace.LastCompleted != "lookup_by_id" || IsNotFound(err) {
				t.Fatalf("got %+v, %+v, %v", message, trace, err)
			}
		}},
		{"missing mailbox", `{"message":null,"failure":{"phase":"resolve_mailbox","message":"mailbox not found: Spam","number":0},"phases":[]}`, func(t *testing.T, message *Message, trace *PhaseTrace, err error) {
			var missing *NotFoundError
			if !errors.As(err, &missing) || missing.Kind != "mailbox" {
				t.Fatalf("err = %v", err)
			}
		}},
		{"missing account", `{"message":null,"failure":{"phase":"resolve_account","message":"No such object. (-1728)","number":-1728},"phases":[]}`, func(t *testing.T, message *Message, trace *PhaseTrace, err error) {
			var missing *NotFoundError
			if !errors.As(err, &missing) || missing.Kind != "account" {
				t.Fatalf("err = %v", err)
			}
		}},
		{"read", `{"message":{"id":"42","content":"body","toRecipients":["a@example.com"]},"failure":null,"phases":[{"name":"content","elapsedMs":12}]}`, func(t *testing.T, message *Message, trace *PhaseTrace, err error) {
			if err != nil || message == nil || message.Content != "body" || len(trace.Phases) != 1 {
				t.Fatalf("got %+v, %+v, %v", message, trace, err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			writeFakeOsaScript(t, "printf '%s\\n' '"+test.output+"'\n")
			message, trace, err := NewClient().getMessageDetailsTraced("Work", "Spam", "42", time.Second)
			test.check(t, message, trace, err)
		})
	}
}

// The phase helper is plain JavaScript, so it runs under the real JXA engine
// without sending Mail.app an Apple Event.
func TestPhaseHelperRunsUnderRealJXA(t *testing.T) {
	if _, err := os.Stat("/usr/bin/osascript"); err != nil {
		t.Skip("osascript unavailable")
	}
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	script := jxaMailboxLookupHelper() + jxaPhaseHelper() + `
let failure = null;
try {
	phase('first', (step) => { step.note = 'ok'; return 1; });
	phase('second', () => { const e = new Error('boom'); e.errorNumber = -1712; throw e; });
} catch (e) {
	failure = {phase: currentPhase, message: describeError(e), transport: isTransportError(e)};
}
JSON.stringify({failure: failure, phases: phaseLog});
`
	want := `{"failure":{"phase":"second","message":"boom (-1712)","transport":true},"phases":[{"name":"first","note":"ok","elapsedMs":0},{"name":"second","elapsedMs":0,"error":"boom (-1712)"}]}`
	elapsed := regexp.MustCompile(`"elapsedMs":\d+`)
	// Elapsed times are real; only the shape is compared.
	normalize := func(out string) string { return elapsed.ReplaceAllString(out, `"elapsedMs":0`) }
	out, err := NewClient().runJXA(script)
	if err != nil || normalize(out) != want {
		t.Fatalf("one-shot = %q, %v", out, err)
	}
	session, err := newJXASession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for i := 0; i < 2; i++ {
		out, err = session.run(script, 20*time.Second)
		if err != nil || normalize(out) != want {
			t.Fatalf("session run %d = %q, %v", i, out, err)
		}
	}
	// The session holds the automation lock until it is closed.
	session.Close()

	slow := jxaPhaseHelper() + `
phase('quick', () => null);
phase('stalled', () => { delay(20); });
'unreachable';
`
	for name, run := range map[string]func() error{
		"one-shot": func() error { _, err := NewClient().runJXAWithTimeout(slow, 3*time.Second); return err },
		"session": func() error {
			stalled, err := newJXASession(context.Background())
			if err != nil {
				return err
			}
			defer stalled.Close()
			_, err = stalled.run(slow, 3*time.Second)
			return err
		},
	} {
		var timeout *AutomationTimeoutError
		if err := run(); !errors.As(err, &timeout) || timeout.Trace == nil || timeout.Trace.Pending != "stalled" || timeout.Trace.LastCompleted != "quick" {
			t.Fatalf("%s: err = %v", name, err)
		}
	}
}
