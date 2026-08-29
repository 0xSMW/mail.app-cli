package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// BatchJournal is an append-only, durable mutation receipt. Each line is a
// complete JSON object so a caller can recover the work completed before an
// interrupted process lost its normal stdout receipt.
type BatchJournal struct {
	path string
	file *os.File
	mu   sync.Mutex
}

var userConfigDir = os.UserConfigDir

// CreateBatchJournal validates and creates path before any mailbox mutation.
// The caller owns Close.
func CreateBatchJournal(path string) (*BatchJournal, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create batch receipt %q: %w", path, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure batch receipt %q: %w", path, err)
	}
	return &BatchJournal{path: path, file: file}, nil
}

// CreateDefaultBatchJournal creates a private, persistent receipt outside the
// working tree. It is the safety net for library and UI callers that do not
// explicitly configure a receipt path.
func CreateDefaultBatchJournal() (*BatchJournal, error) {
	base, err := userConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate receipt directory: %w", err)
	}
	dir := filepath.Join(base, "mail-app-cli", "receipts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create receipt directory %q: %w", dir, err)
	}
	file, err := os.CreateTemp(dir, "batch-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("create batch receipt in %q: %w", dir, err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure batch receipt %q: %w", file.Name(), err)
	}
	return &BatchJournal{path: file.Name(), file: file}, nil
}

func (j *BatchJournal) Path() string { return j.path }

func (j *BatchJournal) Close() error {
	if j == nil || j.file == nil {
		return nil
	}
	return j.file.Close()
}

// Record appends and synchronizes one event. Syncing every event exceeds the
// per-chunk durability guarantee and deliberately favors recoverability over
// the small overhead of a bulk mutation receipt.
func (j *BatchJournal) Record(event string, fields map[string]any) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	record := make(map[string]any, len(fields)+2)
	for key, value := range fields {
		record[key] = value
	}
	record["event"] = event
	record["at"] = time.Now().Format(time.RFC3339Nano)
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode batch receipt event: %w", err)
	}
	if _, err := j.file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("append batch receipt %q: %w", j.path, err)
	}
	if err := j.file.Sync(); err != nil {
		return fmt.Errorf("sync batch receipt %q: %w", j.path, err)
	}
	return nil
}

func journalItem(item BatchItem) map[string]any {
	return map[string]any{"item": item}
}
