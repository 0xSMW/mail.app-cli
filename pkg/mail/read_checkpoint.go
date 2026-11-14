package mail

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const maxReadCheckpointBytes = 64 << 20

var ErrInvalidReadCheckpoint = errors.New("invalid read checkpoint")

type readCheckpoint struct {
	Version      int                `json:"version"`
	Checksum     string             `json:"checksum"`
	Selection    []string           `json:"selection"`
	MailBody     bool               `json:"mailBody"`
	Alternatives bool               `json:"alternatives"`
	Result       SelectedReadResult `json:"result"`
}

func readCheckpointError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidReadCheckpoint, fmt.Sprintf(format, args...))
}

// ValidateReadCheckpoint validates the local file without accessing Mail.
func ValidateReadCheckpoint(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > maxReadCheckpointBytes {
			return readCheckpointError("file must be regular, private (0600), and at most 64 MiB")
		}
	} else if !os.IsNotExist(err) {
		return readCheckpointError("inspect file: %v", err)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() {
		return readCheckpointError("parent directory must exist")
	}
	if err == nil && info != nil {
		_, err = loadReadCheckpoint(path)
		return err
	}
	return nil
}

func readCheckpointChecksum(state readCheckpoint) string {
	state.Checksum = ""
	data, _ := json.Marshal(state)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func loadReadCheckpoint(path string) (readCheckpoint, error) {
	var state readCheckpoint
	f, err := os.Open(path)
	if err != nil {
		return state, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxReadCheckpointBytes+1))
	if err != nil || len(data) > maxReadCheckpointBytes {
		return state, readCheckpointError("read failed or size limit exceeded")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, readCheckpointError("decode: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return state, readCheckpointError("trailing data")
	}
	if state.Version != 1 || state.Checksum != readCheckpointChecksum(state) || len(state.Selection) == 0 {
		return state, readCheckpointError("unsupported or corrupt state")
	}
	seen := map[string]bool{}
	for _, key := range state.Selection {
		if key == "" || seen[key] {
			return state, readCheckpointError("invalid selection")
		}
		seen[key] = true
	}
	items := map[string]bool{}
	for _, item := range state.Result.Items {
		key := item.ID
		if !seen[key] || items[key] || item.ID == "" {
			return state, readCheckpointError("invalid result identity")
		}
		items[key] = true
		if item.Error == "" && (item.Message == nil || item.Message.ContentError != "" || !cacheIdentityUsable(*item.Message) || item.Message.ID != item.ID || item.Message.Account != item.Account) {
			return state, readCheckpointError("invalid successful body")
		}
	}
	if state.Result.Complete && len(items) != len(seen) {
		return state, readCheckpointError("incomplete successful selection")
	}
	if state.Result.Complete {
		for _, item := range state.Result.Items {
			if item.Error != "" {
				return state, readCheckpointError("completed result contains errors")
			}
		}
	}
	return state, nil
}

func saveReadCheckpoint(path string, state readCheckpoint) error {
	state.Checksum = readCheckpointChecksum(state)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > maxReadCheckpointBytes {
		return readCheckpointError("body results exceed 64 MiB")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".mail-read-checkpoint-*")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer os.Remove(temporary) // Reproducible temporary file.
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
	if err = os.Rename(temporary, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func cacheIdentityUsable(message Message) bool {
	identity := StableIdentityFromMessage(message)
	return identity.hasFallback() || identity.RFCMessageID != "" && identity.rfcEnvelopeKey() != "" && identity.Subject != "" && identity.DateSent != ""
}

// cachedBodyWithFreshMetadata copies only extracted body fields. Every mutable
// message property (including local ID, read state, and location) comes from
// the new metadata observation, never from the saved checkpoint.
func cachedBodyWithFreshMetadata(cached, fresh Message) (*Message, error) {
	if !cacheIdentityUsable(cached) || !StableIdentityFromMessage(cached).matchesEnvelope(fresh) || cached.MessageSize != fresh.MessageSize {
		return nil, fmt.Errorf("stale read checkpoint: stable identity changed; use a new checkpoint for a fresh read")
	}
	fresh.Content, fresh.ContentSource = cached.Content, cached.ContentSource
	fresh.BodyDiagnostics = cached.BodyDiagnostics
	fresh.Links, fresh.LinkStatus, fresh.Unsubscribe = cached.Links, cached.LinkStatus, cached.Unsubscribe
	fresh.InReplyTo, fresh.References = cached.InReplyTo, cached.References
	// Verification metadata deliberately omits immutable recipient headers.
	fresh.ToRecipients = append([]string(nil), cached.ToRecipients...)
	fresh.CcRecipients = append([]string(nil), cached.CcRecipients...)
	fresh.BccRecipients = append([]string(nil), cached.BccRecipients...)
	return &fresh, nil
}

func readSelectedWithCheckpoint(refs []MessageRef, opts SelectedReadOptions, mailBody, alternatives bool, metadata func(MessageRef) (*Message, error), read func(MessageRef, int) (*Message, *PhaseTrace, error)) (SelectedReadResult, error) {
	result := SelectedReadResult{Items: []MessageReadResult{}, Complete: true}
	state := readCheckpoint{Version: 1, MailBody: mailBody, Alternatives: alternatives, Result: SelectedReadResult{Items: []MessageReadResult{}}}
	state.Selection = append([]string{}, opts.SelectionIDs...)
	if len(state.Selection) == 0 {
		for _, ref := range refs {
			state.Selection = append(state.Selection, ref.MessageID)
		}
	}
	slices.Sort(state.Selection)
	if opts.Checkpoint != "" {
		for i, id := range state.Selection {
			if id == "" || i > 0 && state.Selection[i-1] == id {
				return result, readCheckpointError("selection must contain unique nonempty IDs")
			}
		}
		if len(state.Selection) == 0 {
			return result, readCheckpointError("selection must not be empty")
		}
	}
	selected := map[string]bool{}
	for _, id := range state.Selection {
		selected[id] = true
	}
	seenRefs := map[string]bool{}
	for _, ref := range refs {
		if opts.Checkpoint != "" && (!selected[ref.MessageID] || seenRefs[ref.MessageID]) {
			return result, readCheckpointError("resolved IDs do not match selection")
		}
		seenRefs[ref.MessageID] = true
	}
	result.Complete = len(refs) == len(state.Selection)
	cached := map[string]MessageReadResult{}
	if opts.Checkpoint != "" {
		if err := ValidateReadCheckpoint(opts.Checkpoint); err != nil {
			return result, err
		}
		previous, err := loadReadCheckpoint(opts.Checkpoint)
		if err == nil {
			if !slices.Equal(previous.Selection, state.Selection) || previous.MailBody != mailBody || previous.Alternatives != alternatives {
				return result, readCheckpointError("selection or body options differ; use original selection/options or a new checkpoint")
			}
			for _, item := range previous.Result.Items {
				if item.Error == "" {
					cached[item.ID] = item
				}
			}
		} else if !os.IsNotExist(err) {
			return result, err
		}
		// Establish a durable incomplete invocation before extraction starts.
		state.Result.Items = append(state.Result.Items, successfulReadItems(previous.Result.Items)...)
		if err := saveReadCheckpoint(opts.Checkpoint, state); err != nil {
			return result, fmt.Errorf("persist read checkpoint: %w", err)
		}
	}
	emit := func(event string, item *MessageReadResult, completed int) error {
		if opts.Events == nil {
			return nil
		}
		record := map[string]any{"event": event, "at": time.Now().Format(time.RFC3339Nano), "action": "read", "completed": completed, "matched": len(state.Selection)}
		if item != nil {
			record["id"] = item.ID
			record["account"] = item.Account
			if item.Error != "" {
				record["error"] = item.Error
			}
		}
		return json.NewEncoder(opts.Events).Encode(record)
	}
	if err := emit("started", nil, 0); err != nil {
		return result, err
	}
	processed := map[string]bool{}
	for i, ref := range refs {
		item := MessageReadResult{ID: ref.MessageID, Account: ref.AccountName, Mailbox: ref.MailboxName}
		saved, resume := cached[ref.MessageID]
		phase := "body_started"
		if resume {
			phase = "metadata_started"
		}
		if err := emit(phase, &item, i); err != nil {
			result.Complete = false
			return result, err
		}
		var err error
		if resume {
			var fresh *Message
			if saved.Account != ref.AccountName {
				err = fmt.Errorf("checkpoint account %q differs from current account %q", saved.Account, ref.AccountName)
			} else {
				fresh, err = metadata(ref)
			}
			if err == nil && fresh == nil {
				err = fmt.Errorf("message absent")
			}
			if err == nil && (fresh.ID != ref.MessageID || fresh.Account != ref.AccountName) {
				err = fmt.Errorf("current metadata does not match requested account and local ID")
			}
			if err == nil {
				item.Message, err = cachedBodyWithFreshMetadata(*saved.Message, *fresh)
			}
			if err != nil {
				err = fmt.Errorf("stale read checkpoint: cannot validate current local ID %s: %w; locate the message again and use a new checkpoint", ref.MessageID, err)
			}
		} else {
			item.Message, item.Diagnostics, err = read(ref, i)
			if err == nil && item.Message == nil {
				err = fmt.Errorf("message not found")
			}
			if err == nil && item.Message.ContentError != "" {
				err = fmt.Errorf("body unavailable: %s", item.Message.ContentError)
			}
			if err == nil && opts.Checkpoint != "" && (item.Message.ID != ref.MessageID || item.Message.Account != ref.AccountName || !cacheIdentityUsable(*item.Message)) {
				err = fmt.Errorf("cannot checkpoint body without complete stable identity")
			}
		}
		if err != nil {
			item.Error = err.Error()
			result.Complete = false
		}
		result.Items = append(result.Items, item)
		processed[ref.MessageID] = true
		if opts.Checkpoint != "" {
			// Keep unvisited successful bodies if an invocation is interrupted again.
			state.Result = result
			// Preserve immutable original success evidence for every refused
			// cached item, including earlier items when later results are saved.
			state.Result.Items = append([]MessageReadResult{}, result.Items...)
			for n, completed := range result.Items {
				if completed.Error != "" {
					if original, ok := cached[completed.ID]; ok {
						state.Result.Items[n] = original
					}
				}
			}
			state.Result.Complete = result.Complete && len(processed) == len(state.Selection)
			for _, id := range state.Selection {
				if !processed[id] {
					if saved, ok := cached[id]; ok {
						state.Result.Items = append(state.Result.Items, saved)
					}
				}
			}
			if err := saveReadCheckpoint(opts.Checkpoint, state); err != nil {
				result.Complete = false
				return result, fmt.Errorf("persist read checkpoint: %w", err)
			}
		}
		completedPhase := "body_completed"
		if resume {
			completedPhase = "metadata_completed"
		}
		if err := emit(completedPhase, &item, i+1); err != nil {
			result.Complete = false
			return result, err
		}
		if err := emit("item_completed", &item, i+1); err != nil {
			result.Complete = false
			return result, err
		}
	}
	if err := emit("completed", nil, len(result.Items)); err != nil {
		result.Complete = false
		return result, err
	}
	return result, nil
}

func successfulReadItems(items []MessageReadResult) []MessageReadResult {
	var out []MessageReadResult
	for _, item := range items {
		if item.Error == "" {
			out = append(out, item)
		}
	}
	return out
}
