package mail

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var ErrInvalidUnsubscribeCheckpoint = errors.New("invalid unsubscribe checkpoint")

type unsubscribeCheckpoint struct {
	Kind              string                   `json:"kind"`
	Version           int                      `json:"version"`
	Checksum          string                   `json:"checksum"`
	Scopes            []SearchMailbox          `json:"scopes"`
	Since             string                   `json:"since"`
	Cursor            string                   `json:"cursor"`
	TraversalComplete bool                     `json:"traversalComplete"`
	Coverage          []ScanCoverage           `json:"coverage"`
	Pending           []ScanMessage            `json:"pending"`
	Observations      []UnsubscribeObservation `json:"observations"`
	Pages             int                      `json:"pages"`
	StartedAt         string                   `json:"startedAt"`
	EndedAt           string                   `json:"endedAt"`
}

func unsubscribeCheckpointError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidUnsubscribeCheckpoint, fmt.Sprintf(format, args...))
}

func newUnsubscribeCheckpoint(req UnsubscribeScanRequest) unsubscribeCheckpoint {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	s := unsubscribeCheckpoint{Kind: "unsubscribe-discovery", Version: 1, Scopes: req.Scopes, Since: req.Since, Coverage: []ScanCoverage{}, Pending: []ScanMessage{}, Observations: []UnsubscribeObservation{}, StartedAt: now, EndedAt: now}
	for _, scope := range req.Scopes {
		s.Coverage = append(s.Coverage, ScanCoverage{SearchMailbox: scope})
	}
	return s
}

func unsubscribeCheckpointPath(value string) (string, error) {
	if value != "" {
		path, err := filepath.Abs(value)
		if err != nil {
			return "", err
		}
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil || !parent.IsDir() {
			return "", unsubscribeCheckpointError("parent directory must exist")
		}
		return path, nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cache, "mail-app-cli", "unsubscribe")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return "", err
	}
	return filepath.Abs(filepath.Join(dir, "scan-"+hex.EncodeToString(random[:])+".json"))
}

func checkUnsubscribeStateFile(path string, missingOK bool) error {
	if path == "" || !filepath.IsAbs(path) {
		return unsubscribeCheckpointError("path must be nonempty and absolute")
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) && missingOK {
		return nil
	}
	if err != nil {
		return unsubscribeCheckpointError("inspect state: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxScanCheckpointBytes {
		return unsubscribeCheckpointError("state must be a private regular file (0600), at most 64 MiB")
	}
	return nil
}

func lockUnsubscribeCheckpoint(path string) (func(), error) {
	lockPath := path + ".lock"
	if err := checkUnsubscribeStateFile(lockPath, true); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, unsubscribeCheckpointError("checkpoint is already in use")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func unsubscribeStateChecksum(s unsubscribeCheckpoint) string {
	s.Checksum = ""
	data, _ := json.Marshal(s)
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func loadUnsubscribeCheckpoint(path string, req UnsubscribeScanRequest) (unsubscribeCheckpoint, bool, error) {
	if err := checkUnsubscribeStateFile(path, true); err != nil {
		return unsubscribeCheckpoint{}, false, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if os.IsNotExist(err) {
		return unsubscribeCheckpoint{}, false, nil
	}
	if err != nil {
		return unsubscribeCheckpoint{}, false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxScanCheckpointBytes+1))
	if err != nil || len(data) > maxScanCheckpointBytes {
		return unsubscribeCheckpoint{}, false, unsubscribeCheckpointError("read failed or file exceeds size limit")
	}
	var s unsubscribeCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&s); err != nil {
		return s, false, unsubscribeCheckpointError("corrupt state: %v", err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return s, false, unsubscribeCheckpointError("trailing data")
	}
	if s.Kind != "unsubscribe-discovery" || s.Version != 1 || s.Checksum != unsubscribeStateChecksum(s) {
		return s, false, unsubscribeCheckpointError("unrecognized or damaged discovery state")
	}
	if err = validateUnsubscribeState(s); err != nil {
		return s, false, err
	}
	if !req.Restart {
		want, _ := json.Marshal(req.Scopes)
		got, _ := json.Marshal(s.Scopes)
		if !bytes.Equal(want, got) || s.Since != req.Since {
			return s, false, unsubscribeCheckpointError("different accounts, mailboxes, or since filter; repeat original scope or use --restart-checkpoint")
		}
	}
	return s, true, nil
}

func validateUnsubscribeState(s unsubscribeCheckpoint) error {
	if len(s.Scopes) == 0 || len(s.Coverage) != len(s.Scopes) || s.Pages < 0 {
		return unsubscribeCheckpointError("inconsistent scope or page count")
	}
	for _, value := range []string{s.StartedAt, s.EndedAt} {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return unsubscribeCheckpointError("invalid observation timestamp")
		}
	}
	if s.TraversalComplete && s.Cursor != "" || !s.TraversalComplete && s.Pages > 0 && s.Cursor == "" {
		return unsubscribeCheckpointError("inconsistent traversal cursor")
	}
	if s.Cursor != "" {
		request := ScanRequest{Scopes: s.Scopes, Since: s.Since}
		if _, err := decodeScanCursor(s.Cursor, request, s.Scopes); err != nil {
			return unsubscribeCheckpointError("cursor does not match scope: %v", err)
		}
	}
	for i, c := range s.Coverage {
		if c.SearchMailbox != s.Scopes[i] || c.Count < 0 || c.Remaining < 0 || s.TraversalComplete && (!c.Exhausted || c.Error != "") {
			return unsubscribeCheckpointError("inconsistent coverage")
		}
	}
	seen := map[string]bool{}
	validate := func(account, id string, boxes []string) error {
		if strings.TrimSpace(id) == "" || len(boxes) == 0 {
			return unsubscribeCheckpointError("missing message identity or membership")
		}
		key := unsubscribeMessageKey(account, id)
		if seen[key] {
			return unsubscribeCheckpointError("duplicate message identity")
		}
		seen[key] = true
		for _, box := range boxes {
			found := false
			for _, scope := range s.Scopes {
				found = found || scope.Account == account && scope.Mailbox == box
			}
			if !found {
				return unsubscribeCheckpointError("message is outside requested scope")
			}
		}
		return nil
	}
	for _, o := range s.Observations {
		if err := validate(o.Account, o.ID, o.Mailboxes); err != nil {
			return err
		}
	}
	// Retry work intentionally refers to an already observed incomplete message.
	seen = map[string]bool{}
	for _, m := range s.Pending {
		if err := validate(m.Account, m.ID, m.Mailboxes); err != nil {
			return err
		}
	}
	return nil
}

func saveUnsubscribeCheckpoint(path string, s *unsubscribeCheckpoint) error {
	if err := validateUnsubscribeState(*s); err != nil {
		return err
	}
	s.Checksum = unsubscribeStateChecksum(*s)
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if len(data) > maxScanCheckpointBytes {
		return unsubscribeCheckpointError("discovery state exceeds 64 MiB; narrow the requested scope")
	}
	if err = checkUnsubscribeStateFile(path, true); err != nil {
		return err
	}
	// A known destination exists before the atomic replacement. Existing files
	// have already been validated as this command's private checkpoint.
	if _, err = os.Lstat(path); os.IsNotExist(err) {
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(data)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			return e
		}
		return nil
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".unsubscribe-state-*")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer func() {
		if filepath.IsAbs(temp) && filepath.Dir(temp) == filepath.Dir(path) && strings.HasPrefix(filepath.Base(temp), ".unsubscribe-state-") {
			if info, e := os.Lstat(temp); e == nil && info.Mode().IsRegular() {
				_ = os.Remove(temp)
			}
		}
	}()
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = checkUnsubscribeStateFile(temp, false); err != nil {
		return err
	}
	if err = checkUnsubscribeStateFile(path, false); err != nil {
		return err
	}
	if err = os.Rename(temp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
