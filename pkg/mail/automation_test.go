package mail

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func writeFakeOsaScript(t *testing.T, body string) {
	t.Helper()
	binDir := t.TempDir()
	path := filepath.Join(binDir, "osascript")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}
	t.Setenv("PATH", binDir)
}

func TestRunAutomationTimeoutIsClassifiable(t *testing.T) {
	writeFakeOsaScript(t, "/bin/sleep 2\n")

	_, err := NewClient().runJXAWithTimeout("slow", 50*time.Millisecond)
	if err == nil {
		t.Fatal("runJXAWithTimeout returned nil error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("errors.Is(err, context.DeadlineExceeded) = false for %v", err)
	}
	var timeoutErr *AutomationTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("errors.As(err, *AutomationTimeoutError) = false for %T", err)
	}
	if timeoutErr.Engine != "jxa" || timeoutErr.Timeout != 50*time.Millisecond {
		t.Fatalf("timeout error = %#v", timeoutErr)
	}
}

func TestRunAutomationSerializesSubprocesses(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "automation.log")
	t.Setenv("AUTOMATION_LOG", logPath)
	writeFakeOsaScript(t, `
last=""
for arg in "$@"; do last="$arg"; done
case "$last" in
  *first*) label=first ;;
  *second*) label=second ;;
  *) label=unknown ;;
esac
printf '%s-start\n' "$label" >> "$AUTOMATION_LOG"
/bin/sleep 0.1
printf '%s-end\n' "$label" >> "$AUTOMATION_LOG"
printf '%s\n' "$label"
`)

	client := NewClient()
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, script := range []string{"first", "second"} {
		script := script
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := client.runJXA(script)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("runJXA returned error: %v", err)
		}
	}

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read automation log: %v", err)
	}
	lines := strings.Fields(string(contents))
	if len(lines) != 4 {
		t.Fatalf("log lines = %q, want four entries", contents)
	}
	if !strings.HasSuffix(lines[0], "-start") || !strings.HasSuffix(lines[1], "-end") ||
		!strings.HasSuffix(lines[2], "-start") || !strings.HasSuffix(lines[3], "-end") {
		t.Fatalf("automation calls overlapped: %q", contents)
	}
	if strings.TrimSuffix(lines[0], "-start") != strings.TrimSuffix(lines[1], "-end") ||
		strings.TrimSuffix(lines[2], "-start") != strings.TrimSuffix(lines[3], "-end") {
		t.Fatalf("start/end pairs were interleaved: %q", contents)
	}
}

func TestRunAutomationSerializesSeparateProcesses(t *testing.T) {
	binDir, logPath, lockPath := writeLoggingFakeOsaScript(t)
	first := startAutomationHelper(t, binDir, logPath, lockPath, "first")
	second := startAutomationHelper(t, binDir, logPath, lockPath, "second")
	if err := first.Wait(); err != nil {
		t.Fatalf("first helper failed: %v", err)
	}
	if err := second.Wait(); err != nil {
		t.Fatalf("second helper failed: %v", err)
	}

	assertNonOverlappingAutomationLog(t, logPath)
}

func TestRunAutomationQueuedCallGetsFreshExecutionTimeout(t *testing.T) {
	binDir, logPath, lockPath := writeLoggingFakeOsaScript(t)
	holder := startAutomationHelper(t, binDir, logPath, lockPath, "holder")
	t.Cleanup(func() {
		if holder.Process != nil {
			_ = holder.Process.Kill()
		}
		_ = holder.Wait()
	})

	deadline := time.Now().Add(time.Second)
	for {
		contents, err := os.ReadFile(logPath)
		if err == nil && strings.Contains(string(contents), "holder-start") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("holder did not acquire the process lock")
		}
		time.Sleep(10 * time.Millisecond)
	}

	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", lockPath)
	t.Setenv("PATH", binDir)
	started := time.Now()
	output, err := NewClient().runJXAWithTimeout("fast", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("queued runJXAWithTimeout returned error: %v", err)
	}
	if output != "fast" {
		t.Fatalf("output = %q, want fast", output)
	}
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond {
		t.Fatalf("queued call elapsed = %s, want it to wait for the holder", elapsed)
	}
}

func TestRunAutomationTimesOutWhileWaitingForProcessLock(t *testing.T) {
	previousLockTimeout := automationLockTimeout
	automationLockTimeout = 50 * time.Millisecond
	t.Cleanup(func() { automationLockTimeout = previousLockTimeout })

	binDir, logPath, lockPath := writeLoggingFakeOsaScript(t)
	holder := startAutomationHelper(t, binDir, logPath, lockPath, "holder")
	t.Cleanup(func() {
		if holder.Process != nil {
			_ = holder.Process.Kill()
		}
		_ = holder.Wait()
	})
	waitForAutomationLog(t, logPath, "holder-start")

	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", lockPath)
	t.Setenv("PATH", binDir)
	_, err := NewClient().runJXAWithTimeout("waiter", time.Second)
	if err == nil {
		t.Fatal("runJXAWithTimeout returned nil error while lock was held")
	}
	var timeoutErr *AutomationLockTimeoutError
	if !errors.As(err, &timeoutErr) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want classifiable lock timeout", err)
	}
}

func TestRunAutomationTimeoutDoesNotWaitForDetachedDescendant(t *testing.T) {
	binDir, logPath, lockPath := writeLoggingFakeOsaScript(t)
	t.Setenv("AUTOMATION_LOG", logPath)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", lockPath)
	t.Setenv("PATH", binDir)

	started := time.Now()
	_, err := NewClient().runJXAWithTimeout("detached", 200*time.Millisecond)
	if err == nil {
		t.Fatal("runJXAWithTimeout returned nil error")
	}
	if elapsed := time.Since(started); elapsed > 200*time.Millisecond+automationWaitDelay+time.Second {
		t.Fatalf("timed out invocation exceeded its bounded wait: %s", elapsed)
	}
	var timeoutErr *AutomationTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("error = %v, want AutomationTimeoutError", err)
	}

	// The fake osascript writes its descendant's pid asynchronously; under
	// load it can land after the 200ms timeout fires.
	waitForAutomationLog(t, logPath, "detached-pid=")
	contents, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatalf("read descendant pid: %v", readErr)
	}
	for _, field := range strings.Fields(string(contents)) {
		if strings.HasPrefix(field, "detached-pid=") {
			pid, parseErr := strconv.Atoi(strings.TrimPrefix(field, "detached-pid="))
			if parseErr != nil {
				t.Fatalf("parse descendant pid: %v", parseErr)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return
		}
	}
	t.Fatal("fake osascript did not report detached descendant pid")
}

func TestAutomationHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_AUTOMATION_HELPER") != "1" {
		return
	}
	if _, err := NewClient().runJXA(os.Getenv("AUTOMATION_HELPER_SCRIPT")); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func writeLoggingFakeOsaScript(t *testing.T) (binDir, logPath, lockPath string) {
	t.Helper()
	binDir = t.TempDir()
	logPath = filepath.Join(t.TempDir(), "automation.log")
	lockPath = filepath.Join(t.TempDir(), "automation.lock")
	path := filepath.Join(binDir, "osascript")
	body := `#!/bin/sh
last=""
for arg in "$@"; do last="$arg"; done
printf '%s-start\n' "$last" >> "$AUTOMATION_LOG"
case "$last" in
  fast) /bin/sleep 0.01 ;;
  detached)
    /usr/bin/perl -MPOSIX=setsid -e 'setsid(); sleep 5' &
    printf 'detached-pid=%s\n' "$!" >> "$AUTOMATION_LOG"
    /bin/sleep 5
    ;;
  *) /bin/sleep 0.2 ;;
esac
printf '%s-end\n' "$last" >> "$AUTOMATION_LOG"
printf '%s\n' "$last"
`
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}
	return binDir, logPath, lockPath
}

func waitForAutomationLog(t *testing.T, logPath, entry string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		contents, err := os.ReadFile(logPath)
		if err == nil && strings.Contains(string(contents), entry) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("automation log did not contain %q", entry)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func startAutomationHelper(t *testing.T, binDir, logPath, lockPath, script string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestAutomationHelperProcess$")
	cmd.Env = append(os.Environ(),
		"GO_WANT_AUTOMATION_HELPER=1",
		"AUTOMATION_HELPER_SCRIPT="+script,
		"AUTOMATION_LOG="+logPath,
		"MAIL_APP_CLI_AUTOMATION_LOCK_PATH="+lockPath,
		"PATH="+binDir,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start automation helper: %v", err)
	}
	return cmd
}

func assertNonOverlappingAutomationLog(t *testing.T, logPath string) {
	t.Helper()
	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read automation log: %v", err)
	}
	lines := strings.Fields(string(contents))
	if len(lines) != 4 {
		t.Fatalf("log lines = %q, want four entries", contents)
	}
	if !strings.HasSuffix(lines[0], "-start") || !strings.HasSuffix(lines[1], "-end") ||
		!strings.HasSuffix(lines[2], "-start") || !strings.HasSuffix(lines[3], "-end") {
		t.Fatalf("automation calls overlapped: %q", contents)
	}
	if strings.TrimSuffix(lines[0], "-start") != strings.TrimSuffix(lines[1], "-end") ||
		strings.TrimSuffix(lines[2], "-start") != strings.TrimSuffix(lines[3], "-end") {
		t.Fatalf("start/end pairs were interleaved: %q", contents)
	}
}
