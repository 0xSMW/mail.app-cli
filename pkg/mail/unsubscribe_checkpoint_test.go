package mail

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

const unsubscribeFileLimitEnv = "MAIL_UNSUBSCRIBE_FILE_LIMIT_PATH"

func unsubscribeCheckpointTestRequest() UnsubscribeScanRequest {
	return UnsubscribeScanRequest{Scopes: []SearchMailbox{{Account: "Synthetic", Mailbox: "INBOX"}}}
}

// The child limits only regular-file growth. The actual save performs its real
// os.File.Write; no filesystem method or checkpoint implementation is replaced.
// A limit below the serialized state yields a partial write or EFBIG on Unix.
// Keeping the hard limit unchanged permits restoration before test reporting.
func TestUnsubscribeCheckpointFileSizeHelper(t *testing.T) {
	path := os.Getenv(unsubscribeFileLimitEnv)
	if path == "" {
		return
	}
	if !filepath.IsAbs(path) {
		t.Fatal("helper requires an absolute temporary checkpoint path")
	}
	var original syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); err != nil {
		t.Fatal(err)
	}
	const capBytes = 64
	if original.Max < capBytes {
		t.Fatal("hard file-size limit is too small for deterministic injection")
	}
	limited := original
	limited.Cur = capBytes
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); err != nil {
		t.Fatal(err)
	}
	state := newUnsubscribeCheckpoint(unsubscribeCheckpointTestRequest())
	err := saveUnsubscribeCheckpoint(path, &state)
	if restoreErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); restoreErr != nil {
		t.Fatalf("restore file-size limit: %v", restoreErr)
	}
	if !errors.Is(err, syscall.EFBIG) && !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("save must reach injected file-write failure, got %v", err)
	}
}

func failUnsubscribeCheckpointWrite(t *testing.T, path string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestUnsubscribeCheckpointFileSizeHelper$", "-test.count=1")
	command.Env = append(os.Environ(), unsubscribeFileLimitEnv+"="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("real filesystem fault helper: %v\n%s", err, output)
	}
}

func TestUnsubscribeCheckpointInitialWriteFailureIsUnpublished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	failUnsubscribeCheckpointWrite(t, path)
	if info, err := os.Lstat(path); !os.IsNotExist(err) {
		if err != nil {
			t.Fatal(err)
		}
		_, _, resumeErr := loadUnsubscribeCheckpoint(path, unsubscribeCheckpointTestRequest())
		restartReq := unsubscribeCheckpointTestRequest()
		restartReq.Restart = true
		_, _, restartErr := loadUnsubscribeCheckpoint(path, restartReq)
		t.Fatalf("failed initial write published %d bytes; resume=%v restart=%v", info.Size(), resumeErr, restartErr)
	}
	// An interrupted unpublished first save must remain retryable through the
	// normal loader and saver, rather than requiring manual checkpoint removal.
	if _, exists, err := loadUnsubscribeCheckpoint(path, unsubscribeCheckpointTestRequest()); err != nil || exists {
		t.Fatalf("unpublished state should load as absent: exists=%v err=%v", exists, err)
	}
	state := newUnsubscribeCheckpoint(unsubscribeCheckpointTestRequest())
	if err := saveUnsubscribeCheckpoint(path, &state); err != nil {
		t.Fatalf("fresh retry: %v", err)
	}
	if _, exists, err := loadUnsubscribeCheckpoint(path, unsubscribeCheckpointTestRequest()); err != nil || !exists {
		t.Fatalf("retry must persist valid checksummed state: exists=%v err=%v", exists, err)
	}
}

func TestUnsubscribeCheckpointReplacementWriteFailurePreservesState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	state := newUnsubscribeCheckpoint(unsubscribeCheckpointTestRequest())
	if err := saveUnsubscribeCheckpoint(path, &state); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	failUnsubscribeCheckpointWrite(t, path)
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("replacement fault changed published checkpoint: err=%v", err)
	}
	if _, exists, err := loadUnsubscribeCheckpoint(path, unsubscribeCheckpointTestRequest()); err != nil || !exists {
		t.Fatalf("previous state must remain valid: exists=%v err=%v", exists, err)
	}
}

func TestUnsubscribeCheckpointHealthyPersistenceAndLockControls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	req := unsubscribeCheckpointTestRequest()
	state := newUnsubscribeCheckpoint(req)
	if err := saveUnsubscribeCheckpoint(path, &state); err != nil {
		t.Fatal(err)
	}
	state.EndedAt = "2026-11-09T12:00:01Z"
	if err := saveUnsubscribeCheckpoint(path, &state); err != nil {
		t.Fatal(err)
	}
	loaded, exists, err := loadUnsubscribeCheckpoint(path, req)
	if err != nil || !exists || loaded.EndedAt != state.EndedAt || loaded.Checksum != unsubscribeStateChecksum(loaded) {
		t.Fatalf("initial save/replacement round trip: exists=%v err=%v state=%+v", exists, err, loaded)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private checkpoint mode: info=%v err=%v", info, err)
	}
	release, err := lockUnsubscribeCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	competingRelease, competingErr := lockUnsubscribeCheckpoint(path)
	if competingRelease != nil {
		competingRelease()
	}
	release()
	if !errors.Is(competingErr, ErrInvalidUnsubscribeCheckpoint) {
		t.Fatalf("competing writer must fail: %v", competingErr)
	}
	release, err = lockUnsubscribeCheckpoint(path)
	if err != nil {
		t.Fatalf("released persistent lock must be reusable: %v", err)
	}
	release()
}

func TestUnsubscribeCheckpointRejectsUnsafeDestinations(t *testing.T) {
	for _, kind := range []string{"nonprivate", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "checkpoint.json")
			target := path
			mode := os.FileMode(0644)
			if kind == "symlink" {
				target, mode = filepath.Join(dir, "target.json"), 0600
			}
			original := []byte("synthetic destination must remain untouched")
			if err := os.WriteFile(target, original, mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(target, mode); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			state := newUnsubscribeCheckpoint(unsubscribeCheckpointTestRequest())
			if err := saveUnsubscribeCheckpoint(path, &state); !errors.Is(err, ErrInvalidUnsubscribeCheckpoint) {
				t.Fatalf("unsafe destination accepted: %v", err)
			}
			after, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatalf("unsafe destination changed: err=%v", err)
			}
		})
	}
}
