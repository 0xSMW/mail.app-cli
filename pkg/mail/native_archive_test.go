package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const nativeArchiveOwnedEvent = `printf '%s\n' 'mail-app-cli-phase {"name":"archive-viewer-owned","state":"done","note":"{\"viewerId\":42,\"previousApp\":\"com.example.editor\"}"}' >&2
`
const nativeArchivePressEvent = `printf '%s\n' 'mail-app-cli-phase {"name":"native-archive-press","state":"start"}' >&2
`

func fakeNativeArchiveTransaction(t *testing.T, main, cleanup string) string {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "transaction.log")
	t.Setenv("AUTOMATION_LOG", logPath)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	writeFakeOsaScript(t, `
last=""
for arg in "$@"; do last="$arg"; done
case "$last" in
 *"const owner ="*)
  printf 'cleanup\n' >> "$AUTOMATION_LOG"
  `+cleanup+`
  ;;
 *)
  printf 'execute\n' >> "$AUTOMATION_LOG"
  `+main+`
  ;;
esac
`)
	return logPath
}

func assertNativeArchiveInvocations(t *testing.T, path string, cleanup bool) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "execute\n"
	if cleanup {
		want += "cleanup\n"
	}
	if string(data) != want {
		t.Fatalf("invocations = %q, want %q (no replay)", data, want)
	}
}

func TestNativeArchiveTransactionResults(t *testing.T) {
	for _, tt := range []struct {
		name, main, cleanup                      string
		apply, wantError, uncertain, wantCleanup bool
	}{
		{name: "applied", main: nativeArchiveOwnedEvent + nativeArchivePressEvent + `printf '%s' '{"prepared":true,"applied":true}'`, apply: true, wantCleanup: true},
		{name: "prepared", main: nativeArchiveOwnedEvent + `printf '%s' '{"prepared":true,"applied":false}'`, wantCleanup: true},
		{name: "missing-owner", main: `printf '%s' '{"prepared":true,"applied":true}'`, apply: true, wantError: true},
		{name: "missing-prepared", main: nativeArchiveOwnedEvent + `printf '%s' '{"applied":true}'`, apply: true, wantError: true, wantCleanup: true},
		{name: "missing-applied", main: nativeArchiveOwnedEvent + `printf '%s' '{"prepared":true}'`, apply: true, wantError: true, wantCleanup: true},
		{name: "prepare-missing-applied", main: nativeArchiveOwnedEvent + `printf '%s' '{"prepared":true}'`, wantError: true, wantCleanup: true},
		{name: "prepare-null-applied", main: nativeArchiveOwnedEvent + `printf '%s' '{"prepared":true,"applied":null}'`, wantError: true, wantCleanup: true},
		{name: "null-prepared", main: nativeArchiveOwnedEvent + `printf '%s' '{"prepared":null,"applied":false}'`, wantError: true, wantCleanup: true},
		{name: "unexpected-applied", main: nativeArchiveOwnedEvent + `printf '%s' '{"prepared":true,"applied":true}'`, wantError: true, wantCleanup: true},
		{name: "malformed", main: nativeArchiveOwnedEvent + `printf '%s' 'not-json'`, apply: true, wantError: true, wantCleanup: true},
		{name: "malformed-after-press", main: nativeArchiveOwnedEvent + nativeArchivePressEvent + `printf '%s' 'not-json'`, apply: true, wantError: true, uncertain: true, wantCleanup: true},
		{name: "preparation-failure", main: nativeArchiveOwnedEvent + `printf 'preparation failed' >&2; exit 1`, apply: true, wantError: true, wantCleanup: true},
		{name: "press-child-failure", main: nativeArchiveOwnedEvent + nativeArchivePressEvent + `printf 'press failed' >&2; exit 1`, apply: true, wantError: true, uncertain: true, wantCleanup: true},
		{name: "cleanup-error", main: nativeArchiveOwnedEvent + `printf '%s' '{"prepared":true,"applied":false}'`, cleanup: `printf 'close failed' >&2; exit 1`, wantError: true, uncertain: true, wantCleanup: true},
		{name: "cleanup-incomplete", main: nativeArchiveOwnedEvent + `printf '%s' '{"prepared":true,"applied":false}'`, cleanup: `printf 'still-open'`, wantError: true, uncertain: true, wantCleanup: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := tt.cleanup
			if cleanup == "" {
				cleanup = "printf closed"
			}
			log := fakeNativeArchiveTransaction(t, tt.main, cleanup)
			err := NewClient().runNativeArchive("Test", "7", tt.apply)
			if (err != nil) != tt.wantError {
				t.Fatalf("error = %v, wantError %v", err, tt.wantError)
			}
			var uncertain *NativeArchiveError
			if errors.As(err, &uncertain) != tt.uncertain {
				t.Fatalf("error = %v, uncertain = %v, want %v", err, uncertain, tt.uncertain)
			}
			assertNativeArchiveInvocations(t, log, tt.wantCleanup)
		})
	}
}

func TestNativeArchiveCancellationCleansOwnedViewer(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline-%v", deadline), func(t *testing.T) {
			log := fakeNativeArchiveTransaction(t, nativeArchiveOwnedEvent+`printf 'owned\n' >> "$AUTOMATION_LOG"
/bin/sleep 5`, "printf closed")
			ctx, cancel := context.WithCancel(context.Background())
			if deadline {
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			}
			defer cancel()
			finished := make(chan error, 1)
			go func() { finished <- NewClient().WithContext(ctx).runNativeArchive("Test", "7", true) }()
			waitForAutomationLog(t, log, "owned")
			if !deadline {
				cancel()
			}
			select {
			case err := <-finished:
				want := context.Canceled
				if deadline {
					want = context.DeadlineExceeded
				}
				if !errors.Is(err, want) {
					t.Fatalf("error = %v, want %v", err, want)
				}
				var uncertain *NativeArchiveError
				if errors.As(err, &uncertain) {
					t.Fatalf("pre-press cancellation unexpectedly uncertain: %v", err)
				}
			case <-time.After(automationWaitDelay + 3*time.Second):
				t.Fatal("cancelled transaction did not finish cleanup")
			}
			data, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != "execute\nowned\ncleanup\n" {
				t.Fatalf("invocations = %q", data)
			}
		})
	}
}

func TestNativeArchiveScriptGuardsAndPress(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable for JXA mocks")
	}
	for _, tt := range []struct {
		scenario  string
		presses   int
		wantError bool
	}{
		{"non-gmail", 0, true}, {"gmail-lookalike", 0, true}, {"googlemail", 1, false}, {"apply", 1, false}, {"prepare", 0, false}, {"wrong-id", 0, true}, {"source", 0, true}, {"conversation", 0, true}, {"foreground", 0, true}, {"preview", 0, true}, {"late-selection", 0, true}, {"late-foreground", 0, true}, {"focus-window", 0, true}, {"press-error", 1, true},
	} {
		t.Run(tt.scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, "testdata/native_archive_mock.js", tt.scenario)
			cmd.Stdin = strings.NewReader(nativeArchiveScript)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("mock execution: %v: %s", err, out)
			}
			var got struct {
				Presses       int
				Output, Error string
				Events        []phaseEvent
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode mock: %v: %s", err, out)
			}
			if got.Presses != tt.presses || (got.Error != "") != tt.wantError {
				t.Fatalf("mock result = %s", out)
			}
			if !tt.wantError {
				var result struct{ Prepared, Applied bool }
				if err := json.Unmarshal([]byte(got.Output), &result); err != nil || !result.Prepared || result.Applied != (tt.scenario == "apply" || tt.scenario == "googlemail") {
					t.Fatalf("result = %s", out)
				}
			}
			if tt.scenario == "non-gmail" || tt.scenario == "gmail-lookalike" {
				if len(got.Events) != 0 {
					t.Fatalf("ineligible account created viewer: %#v", got.Events)
				}
			}
			if tt.scenario == "press-error" {
				if len(got.Events) < 3 || got.Events[1].Name != "native-archive-press" || got.Events[1].State != "start" || got.Events[2].State != "failed" {
					t.Fatalf("press failure events = %#v", got.Events)
				}
			}
		})
	}
}

func TestNativeArchiveNonGmailAllMailUsesGenericMove(t *testing.T) {
	log := filepath.Join(t.TempDir(), "dispatch.log")
	t.Setenv("NATIVE_DISPATCH_LOG", log)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `
case "$*" in
 *mail-app-cli-native-archive-account*) printf 'probe\n' >> "$NATIVE_DISPATCH_LOG"; printf false ;;
 *"move targetMessage"*)
  case "$*" in *'account "Ordinary IMAP"'*) ;; *) exit 1 ;; esac
  printf 'move\n' >> "$NATIVE_DISPATCH_LOG"; printf 'All Mail' ;;
 *) printf 'unexpected\n' >> "$NATIVE_DISPATCH_LOG"; exit 1 ;;
esac
`)
	client := NewClient()
	native, err := client.archiveUsesNative("Ordinary IMAP", "INBOX", "All Mail")
	if err != nil || native {
		t.Fatalf("native=%v err=%v", native, err)
	}
	if err := client.ArchiveMessageToDestination("Ordinary IMAP", "INBOX", "7", "All Mail"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "probe\nmove\n" {
		t.Fatalf("dispatch=%q err=%v", data, err)
	}
}

func TestNativeArchiveAccountProbeInvalidFailsBeforeMutation(t *testing.T) {
	for _, response := range []string{"null", `"true"`, "{}", "[]", "not-json", "true false"} {
		t.Run(response, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "dispatch.log")
			t.Setenv("NATIVE_DISPATCH_LOG", log)
			t.Setenv("NATIVE_PROBE_RESPONSE", response)
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
			writeFakeOsaScript(t, `
case "$*" in
 *mail-app-cli-native-archive-account*) printf 'probe\n' >> "$NATIVE_DISPATCH_LOG"; printf '%s' "$NATIVE_PROBE_RESPONSE" ;;
 *) printf 'mutation\n' >> "$NATIVE_DISPATCH_LOG"; printf 'All Mail' ;;
esac
`)
			err := NewClient().ArchiveMessageToDestination("Test", "INBOX", "7", "All Mail")
			if err == nil {
				t.Fatal("invalid account probe accepted")
			}
			data, readErr := os.ReadFile(log)
			if readErr != nil || string(data) != "probe\n" {
				t.Fatalf("dispatch=%q err=%v", data, readErr)
			}
		})
	}
}

func TestNativeArchiveAccountProbeCacheAndReset(t *testing.T) {
	log := filepath.Join(t.TempDir(), "probes.log")
	t.Setenv("NATIVE_DISPATCH_LOG", log)
	t.Setenv("NATIVE_PROBE_RESPONSE", "false")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	writeFakeOsaScript(t, `printf 'probe\n' >> "$NATIVE_DISPATCH_LOG"
printf '%s' "$NATIVE_PROBE_RESPONSE"`)
	client := NewClient()
	for _, tuple := range []struct {
		account, source, destination string
		want                         bool
	}{
		{"Test", "INBOX", "Archive", false}, {"Test", "Sent", "All Mail", false}, {"Test", "INBOX", "All Mail", false},
	} {
		got, err := client.archiveUsesNative(tuple.account, tuple.source, tuple.destination)
		if err != nil || got != tuple.want {
			t.Fatalf("native=%v err=%v tuple=%+v", got, err, tuple)
		}
	}
	t.Setenv("NATIVE_PROBE_RESPONSE", "true")
	// Copies share the per-account cache; changing the fixture cannot change a
	// cached decision until an explicit refresh invalidates that decision.
	got, err := client.WithContext(context.Background()).archiveUsesNative("Test", "INBOX", "All Mail")
	if err != nil || got {
		t.Fatalf("cached native=%v err=%v", got, err)
	}
	data, err := os.ReadFile(log)
	if err != nil || string(data) != "probe\n" {
		t.Fatalf("cached probes=%q err=%v", data, err)
	}
	client.ResetAccountCache()
	got, err = client.archiveUsesNative("Test", "INBOX", "All Mail")
	if err != nil || !got {
		t.Fatalf("refreshed native=%v err=%v", got, err)
	}
	data, err = os.ReadFile(log)
	if err != nil || string(data) != "probe\nprobe\n" {
		t.Fatalf("refreshed probes=%q err=%v", data, err)
	}
}
