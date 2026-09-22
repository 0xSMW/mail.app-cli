package mail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// jxaSession reuses one serial bridge process within a bounded chunk. Each
// request still returns to Go, so RunBatch fsyncs its receipt between mailbox
// phases exactly as it does with individual osascript invocations. It is not
// a daemon and is never shared by concurrent callers.
type jxaSession struct {
	ctx     context.Context
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser
	decoder *json.Decoder
	wait    chan error
	release func()
	closed  bool
	failure error
	mu      sync.Mutex
}

var errSessionInterrupted = errors.New("jxa session interrupted")

const jxaSessionScript = `
ObjC.import('Foundation');
const input = $.NSFileHandle.fileHandleWithStandardInput;
const output = $.NSFileHandle.fileHandleWithStandardOutput;
let pending = '';
function reply(value) {
    const data = $(JSON.stringify(value) + '\n').dataUsingEncoding($.NSUTF8StringEncoding);
    output.writeData(data);
}
while (true) {
    const data = input.availableData;
    if (data.length === 0) break;
    pending += ObjC.unwrap($.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding));
    let newline;
    while ((newline = pending.indexOf('\n')) >= 0) {
        const line = pending.slice(0, newline);
        pending = pending.slice(newline + 1);
        try {
            const decoded = $.NSData.alloc.initWithBase64EncodedStringOptions($(line), 0);
            const script = ObjC.unwrap($.NSString.alloc.initWithDataEncoding(decoded, $.NSUTF8StringEncoding));
            const value = eval(script);
            reply({output: value === undefined || value === null ? '' : String(value)});
        } catch (e) {
            reply({error: String(e)});
        }
    }
}
`

func newJXASession(ctx context.Context) (*jxaSession, error) {
	lockCtx, cancel := context.WithTimeout(ctx, automationLockTimeout)
	defer cancel()
	if err := acquireAutomationGate(lockCtx); err != nil {
		return nil, automationLockError("jxa", ctx, err)
	}
	releaseLock, err := acquireAutomationProcessLock(lockCtx)
	if err != nil {
		releaseAutomationGate()
		return nil, automationLockError("jxa", ctx, err)
	}
	s := &jxaSession{ctx: ctx, wait: make(chan error, 1), release: func() { releaseLock(); releaseAutomationGate() }}
	s.cmd = exec.CommandContext(ctx, "osascript", "-l", "JavaScript", "-e", jxaSessionScript)
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	s.cmd.Cancel = func() error {
		if s.cmd.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			return s.cmd.Process.Kill()
		}
		return nil
	}
	s.cmd.WaitDelay = automationWaitDelay
	s.stdin, err = s.cmd.StdinPipe()
	if err != nil {
		s.release()
		return nil, err
	}
	s.stdout, err = s.cmd.StdoutPipe()
	if err != nil {
		s.stdin.Close()
		s.release()
		return nil, err
	}
	// Protocol errors are reported on stdout. Do not accumulate unbounded
	// subprocess stderr while the batch is running.
	s.cmd.Stderr = io.Discard
	s.decoder = json.NewDecoder(s.stdout)
	if err := s.cmd.Start(); err != nil {
		s.stdin.Close()
		s.stdout.Close()
		s.release()
		return nil, err
	}
	go func() { s.wait <- s.cmd.Wait() }()
	return s, nil
}

func (s *jxaSession) run(script string, timeout time.Duration) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		if s.failure != nil {
			return "", s.failure
		}
		return "", fmt.Errorf("jxa session closed")
	}
	if err := s.ctx.Err(); err != nil {
		s.failure = err
		s.closeLocked()
		return "", err
	}
	if timeout <= 0 {
		timeout = defaultAutomationTimeout
	}
	type response struct {
		Output *string `json:"output"`
		Error  string  `json:"error"`
	}
	type exchange struct {
		value response
		err   error
	}
	done := make(chan exchange, 1)
	go func() {
		// ASCII framing keeps Foundation's availableData decoding safe even
		// when a pipe read splits a non-ASCII script character.
		_, err := io.WriteString(s.stdin, base64.StdEncoding.EncodeToString([]byte(script))+"\n")
		var value response
		if err == nil {
			err = s.decoder.Decode(&value)
		}
		if err == nil && value.Output == nil && value.Error == "" {
			err = fmt.Errorf("missing bridge response")
		}
		done <- exchange{value, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		if result.err != nil {
			// A broken bridge cannot tell us whether the last mutation ran.
			s.failure = fmt.Errorf("%w: %v", errSessionInterrupted, result.err)
			s.closeLocked()
			return "", s.failure
		}
		if result.value.Error != "" {
			return "", fmt.Errorf("jxa error: %s", result.value.Error)
		}
		return *result.value.Output, nil
	case <-s.ctx.Done():
		s.failure = s.ctx.Err()
	case <-timer.C:
		s.failure = &AutomationTimeoutError{Engine: "jxa", Timeout: timeout}
	}
	s.closeLocked()
	<-done // pipes are closed and the child reaped before releasing the caller
	return "", s.failure
}

func (s *jxaSession) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

func (s *jxaSession) closeLocked() {
	if s.closed {
		return
	}
	s.closed = true
	_ = s.stdin.Close()
	_ = s.cmd.Cancel()
	_ = s.stdout.Close()
	<-s.wait
	s.release()
}
