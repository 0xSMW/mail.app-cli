package mail

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// holdAutomationExecutionLocks supplies the same ownership required by a
// transaction that invokes the unlocked runner for execution and cleanup.
func holdAutomationExecutionLocks(t *testing.T) {
	t.Helper()
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := acquireAutomationGate(ctx); err != nil {
		t.Fatalf("acquire gate: %v", err)
	}
	t.Cleanup(releaseAutomationGate)
	release, err := acquireAutomationProcessLock(ctx)
	if err != nil {
		t.Fatalf("acquire process lock: %v", err)
	}
	t.Cleanup(release)
}

func TestRunAutomationUnlockedRetainsEventsAfterCancellation(t *testing.T) {
	holdAutomationExecutionLocks(t)
	readyPath := filepath.Join(t.TempDir(), "ready")
	t.Setenv("AUTOMATION_LOG", readyPath)
	writeFakeOsaScript(t, `
printf '%s\n' 'mail-app-cli-phase {"name":"viewer-created","state":"done","note":"viewerID=42"}' >&2
printf 'ready\n' > "$AUTOMATION_LOG"
/bin/sleep 5
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type result struct {
		execution automationExecution
		err       error
	}
	finished := make(chan result, 1)
	go func() {
		execution, err := runAutomationUnlocked(ctx, "jxa", 10*time.Second)
		finished <- result{execution, err}
	}()
	waitForAutomationLog(t, readyPath, "ready")
	started := time.Now()
	cancel()
	select {
	case got := <-finished:
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("error = %v, want context.Canceled", got.err)
		}
		if len(got.execution.Events) != 1 || got.execution.Events[0].Note != "viewerID=42" {
			t.Fatalf("retained events = %#v", got.execution.Events)
		}
		if elapsed := time.Since(started); elapsed > automationWaitDelay+time.Second {
			t.Fatalf("cancellation exceeded bounded wait: %s", elapsed)
		}
	case <-time.After(automationWaitDelay + 2*time.Second):
		t.Fatal("cancelled runner did not return within bounded wait")
	}

	// Cleanup must execute without reacquiring locks still held by this caller,
	// even though the original transaction context has been cancelled.
	writeFakeOsaScript(t, "printf ' cleaned up \\n'\n")
	cleanup, err := runAutomationUnlocked(context.WithoutCancel(ctx), "jxa", time.Second)
	if err != nil || cleanup.Output != "cleaned up" {
		t.Fatalf("cleanup = %#v, %v", cleanup, err)
	}
}

func TestRunAutomationUnlockedRetainsEventsOnCommandError(t *testing.T) {
	holdAutomationExecutionLocks(t)
	writeFakeOsaScript(t, `
printf '%s\n' 'mail-app-cli-phase {"name":"viewer-created","state":"done","note":"viewerID=42"}' >&2
printf '%s\n' 'bridge failed' >&2
printf '%s' 'mail-app-cli-phase {"name":' >&2
printf ' partial output \n'
exit 1
`)
	execution, err := runAutomationUnlocked(context.Background(), "jxa", time.Second)
	if err == nil || !strings.Contains(err.Error(), "bridge failed") {
		t.Fatalf("error = %v, want command diagnostic", err)
	}
	if strings.Contains(err.Error(), phaseMarker) {
		t.Fatalf("phase records leaked into diagnostic: %v", err)
	}
	if execution.Output != "partial output" || len(execution.Events) != 1 || execution.Events[0].Note != "viewerID=42" {
		t.Fatalf("execution = %#v", execution)
	}
}

func TestRunAutomationUnlockedTimeoutRetainsEventsAndTrace(t *testing.T) {
	holdAutomationExecutionLocks(t)
	writeFakeOsaScript(t, `
printf '%s\n' 'mail-app-cli-phase {"name":"viewer-created","state":"done","note":"viewerID=42"}' >&2
printf '%s\n' 'mail-app-cli-phase {"name":"archive","state":"start"}' >&2
/bin/sleep 5
`)
	const timeout = time.Second
	started := time.Now()
	execution, err := runAutomationUnlocked(context.Background(), "jxa", timeout)
	var timeoutErr *AutomationTimeoutError
	if !errors.As(err, &timeoutErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want classifiable timeout", err)
	}
	if elapsed := time.Since(started); elapsed > timeout+automationWaitDelay+time.Second {
		t.Fatalf("timeout exceeded bounded wait: %s", elapsed)
	}
	if len(execution.Events) != 2 || timeoutErr.Trace == nil || timeoutErr.Trace.LastCompleted != "viewer-created" || timeoutErr.Trace.Pending != "archive" {
		t.Fatalf("execution = %#v, trace = %#v", execution, timeoutErr.Trace)
	}
}
