package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
)

func TestArchiveDoesNotTrustExplicitMailboxSource(t *testing.T) {
	if trustExplicitSource("archive", true) {
		t.Fatal("archive must resolve Gmail labels even when --mailbox was explicit")
	}
	if !trustExplicitSource("move", true) {
		t.Fatal("ordinary explicit move source should remain trusted")
	}
}

func TestItemsFromExplicitIndexedRefCarryFallbackIdentity(t *testing.T) {
	message := mail.Message{ID: "42", Account: "Work", Mailbox: "INBOX", Sender: "sender@example.com", Subject: "Subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42}
	items := itemsFromRefs([]messageRef{{ID: "42", Account: "Work", Mailbox: "All Mail", Envelope: &message}})
	if len(items) != 1 || items[0].SourceMailbox != "All Mail" || items[0].Identity.Sender != "sender@example.com" || items[0].Identity.MessageSize != 42 {
		t.Fatalf("items = %+v, want explicit source plus guarded fallback", items)
	}
}

func TestRunMessageBatchIdentityCaptureFailureFailsBeforeMutation(t *testing.T) {
	installBatchVerificationScript(t, "error")

	for _, tt := range []struct {
		name   string
		action string
		target string
	}{
		{name: "move", action: "move", target: "Processed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			result, err := runMessageBatch(mail.NewClient(), batchOptions{
				Action:        tt.action,
				TargetMailbox: tt.target,
				Verify:        true,
			}, []batchItem{{ID: "123", Account: "Work", SourceMailbox: "INBOX"}}, func(*mail.Client, *batchItem) error {
				return nil
			})
			if err == nil {
				t.Fatal("runMessageBatch() error = nil, want verification read failure")
			}
			if result.Succeeded != 0 || result.Failed != 1 {
				t.Fatalf("runMessageBatch() counts = succeeded %d, failed %d; want succeeded 0, failed 1", result.Succeeded, result.Failed)
			}
			item := result.Items[0]
			if item.Status != "failed" || !strings.Contains(item.Error, "capture stable identity") {
				t.Fatalf("runMessageBatch() item = %+v, want failed identity capture", item)
			}
			if !strings.Contains(item.Error, "verification read failed") {
				t.Fatalf("Error = %q, want read error", item.Error)
			}
		})
	}
}

func TestVerifyBatchMutationRefusesUncapturedIdentity(t *testing.T) {
	installBatchVerificationScript(t, "absent")

	for _, tt := range []struct {
		name   string
		action string
		target string
	}{
		{name: "archive", action: "archive", target: "All Mail"},
		{name: "move", action: "move", target: "Processed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			status, err := mail.VerifyMutation(mail.NewClient(), batchOptions{Action: tt.action}, batchItem{
				ID: "123", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: tt.target,
			})
			if err == nil || status != "applied_destination_unverified" {
				t.Fatalf("verifyBatchMutation() = (%q, %v), want uncaptured identity failure", status, err)
			}
		})
	}
}

func TestWriteReceiptWritesLegacyFinalReport(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w, err := output.New(output.FormatJSON, &stdout, &stderr, false, "", "messages batch archive", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	previousWriter := writer
	writer = w
	t.Cleanup(func() { writer = previousWriter })

	result, err := runMessageBatch(mail.NewClient(), batchOptions{Action: "move", TargetMailbox: "Processed"}, []batchItem{{
		ID: "42", Account: "Work", SourceMailbox: "INBOX", TargetMailbox: "Processed",
	}}, func(*mail.Client, *batchItem) error { return nil })
	if err != nil {
		t.Fatalf("runMessageBatch() error = %v", err)
	}
	reportFile := filepath.Join(t.TempDir(), "receipt.json")
	err = writeReceipt(result, batchOptions{Action: "move", TargetMailbox: "Processed"}, nil, nil, reportFile)
	if err != nil {
		t.Fatalf("writeReceipt() error = %#v, want nil", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no second error envelope", stderr.String())
	}

	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a receipt envelope: %v\n%s", err, stdout.String())
	}
	if !envelope.OK || envelope.Code != "" || envelope.ExitCode != 0 {
		t.Fatalf("envelope outcome = %+v, want successful mutation receipt", envelope)
	}
	if len(envelope.Notices) != 0 {
		t.Fatalf("notices = %q, want none", envelope.Notices)
	}

	data, ok := envelope.Data.(map[string]any)
	if !ok || data["succeeded"] != float64(1) {
		t.Fatalf("receipt data = %#v, want successful mutation", envelope.Data)
	}
	reportData, err := os.ReadFile(reportFile)
	if err != nil {
		t.Fatal(err)
	}
	var report batchResult
	if err := json.Unmarshal(reportData, &report); err != nil {
		t.Fatalf("legacy report = %q: %v", reportData, err)
	}
	if report.Succeeded != 1 {
		t.Fatalf("legacy report = %+v, want successful result", report)
	}
}

func TestWriteReceiptKeepsMutationFailureAuthoritative(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w, err := output.New(output.FormatJSON, &stdout, &stderr, false, "", "messages batch archive", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	previousWriter := writer
	writer = w
	t.Cleanup(func() { writer = previousWriter })

	mutationErr := &mail.BatchFailedError{Action: "archive", Failed: 1, Attempted: 1}
	err = writeReceipt(batchResult{Action: "archive", Matched: 1, Attempted: 1, Failed: 1, Items: []batchItem{{ID: "42", Status: "failed"}}}, batchOptions{Action: "archive"}, nil, mutationErr, filepath.Join(t.TempDir(), "missing", "receipt.json"))
	var failure *clierr.Error
	if !errors.As(err, &failure) || failure.Code != clierr.CodeMutationFailed || !failure.Reported {
		t.Fatalf("writeReceipt() error = %#v, want reported mutation failure", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no second error envelope", stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a receipt envelope: %v\n%s", err, stdout.String())
	}
	if envelope.OK || envelope.Code != string(clierr.CodeMutationFailed) || len(envelope.Notices) != 0 {
		t.Fatalf("envelope = %+v, want mutation failure without report notice", envelope)
	}
}

func TestWriteReceiptReportsAppliedMoveAndIncompleteVerificationSeparately(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w, err := output.New(output.FormatJSON, &stdout, &stderr, false, "", "messages move", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	previousWriter := writer
	writer = w
	t.Cleanup(func() { writer = previousWriter })

	result := batchResult{
		Action: "move", Matched: 1, Attempted: 1, Succeeded: 1, Unverified: 1,
		Items: []batchItem{{ID: "old", Status: "succeeded", VerifyStatus: "applied_destination_unverified", VerifyError: "mailbox not found: Archive"}},
	}
	mutationErr := &mail.BatchFailedError{Action: "move", Unverified: 1, Attempted: 1}
	err = writeReceipt(result, batchOptions{Action: "move", TargetMailbox: "Archive"}, nil, mutationErr, "")
	var failure *clierr.Error
	if !errors.As(err, &failure) || failure.Code != clierr.CodeMutationFailed || !failure.Reported {
		t.Fatalf("writeReceipt error = %#v, want reported incomplete verification", err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	var envelope output.Envelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("stdout is not a receipt envelope: %v\n%s", err, stdout.String())
	}
	data, ok := envelope.Data.(map[string]any)
	if envelope.OK || envelope.Code != string(clierr.CodeMutationFailed) || envelope.ExitCode != 6 || !ok {
		t.Fatalf("envelope = %+v, want mutation_failed receipt", envelope)
	}
	if data["succeeded"] != float64(1) || data["failed"] != float64(0) || data["unverified"] != float64(1) {
		t.Fatalf("receipt data = %#v, want succeeded=1 failed=0 unverified=1", data)
	}
}

// TestBatchReceiptCLIHelperProcess is intentionally a separate process: the
// parent test interrupts it while its fake osascript is in flight, exactly as
// a caller that later loses stdout would experience the command.
func TestBatchReceiptCLIHelperProcess(t *testing.T) {
	if os.Getenv("MAIL_APP_CLI_BATCH_RECEIPT_HELPER") != "1" {
		return
	}
	os.Exit(Run([]string{"messages", "mark", "1", "--account", "Work", "--mailbox", "INBOX", "--verify", "--json"}, os.Stdout, os.Stderr))
}

func TestMessagesMarkInterruptedProcessKeepsDurableJournalWhenStdoutIsLost(t *testing.T) {
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "mutation-started")
	fakeOsa := "#!/bin/sh\nprintf started > \"$MAIL_APP_CLI_TEST_MARKER\"\n/bin/sleep 10\nprintf null\n"
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(fakeOsa), 0o755); err != nil {
		t.Fatal(err)
	}
	// Keep the subprocess hermetic even if a future lookup path reaches the
	// Envelope Index: this fake is the only sqlite3 it can execute.
	if err := os.WriteFile(filepath.Join(binDir, "sqlite3"), []byte("#!/bin/sh\nprintf '[]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestBatchReceiptCLIHelperProcess$")
	command.Stdout = io.Discard // Deliberately emulate an inaccessible/truncated receipt stream.
	stderr, err := command.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Env = append(os.Environ(),
		"MAIL_APP_CLI_BATCH_RECEIPT_HELPER=1",
		"MAIL_APP_CLI_TEST_MARKER="+marker,
		"MAIL_APP_CLI_AUTOMATION_LOCK_PATH="+filepath.Join(home, "automation.lock"),
		"MAIL_APP_CLI_CONFIG="+filepath.Join(home, "config.json"),
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"HOME="+home,
		"PATH="+binDir,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}

	journalPath := ""
	journalLine := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "receipt journal: ") {
				journalLine <- strings.TrimPrefix(line, "receipt journal: ")
				return
			}
		}
		journalLine <- ""
	}()
	select {
	case journalPath = <-journalLine:
		if journalPath == "" {
			t.Fatal("child did not print a receipt journal path")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for receipt journal path")
	}
	if !filepath.IsAbs(journalPath) {
		t.Fatalf("journal path = %q, want absolute", journalPath)
	}
	waitForFile(t, marker)
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("interrupted child exited successfully")
	}

	data, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("journal is not parseable JSONL: %v\n%s", err, data)
		}
		events = append(events, event)
	}
	if len(events) < 3 || events[0]["event"] != "started" || events[1]["event"] != "mutation_started" {
		t.Fatalf("journal events = %#v, want started then mutation_started", events)
	}
	if got := events[len(events)-1]["event"]; got != "interrupted" {
		t.Fatalf("terminal event = %v, want interrupted; events = %#v", got, events)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q", path)
}

func installBatchVerificationScript(t *testing.T, mode string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \"$MAIL_APP_CLI_BATCH_VERIFY_MODE\" in\nerror)\n  case \"$*\" in\n    *\"mailbox not found:\"*) echo 'verification read failed' >&2; exit 1 ;;\n    *) printf null ;;\n  esac ;;\nabsent) printf null ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake osascript: %v", err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("MAIL_APP_CLI_BATCH_VERIFY_MODE", mode)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
}
