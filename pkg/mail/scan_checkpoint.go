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
	"sort"
	"time"
)

var ErrInvalidScanCheckpoint = errors.New("invalid scan checkpoint")

const maxScanCheckpointBytes = 64 << 20

type ScanCheckpointProvenance struct {
	Path                 string `json:"path"`
	Resumed              bool   `json:"resumed"`
	Pages                int    `json:"pages"`
	ObservationStartedAt string `json:"observationStartedAt"`
	ObservationEndedAt   string `json:"observationEndedAt"`
	PageStartedAt        string `json:"pageStartedAt"`
	PageEndedAt          string `json:"pageEndedAt"`
}

type checkpointRequest struct {
	Scopes []SearchMailbox `json:"scopes"`
	Query  string          `json:"query"`
	Since  string          `json:"since"`
	Unread bool            `json:"unread"`
}
type scanCheckpoint struct {
	Checksum    string            `json:"checksum"`
	Version     int               `json:"version"`
	Request     checkpointRequest `json:"request"`
	Fingerprint string            `json:"fingerprint"`
	Result      ScanResult        `json:"result"`
}

func checkpointIdentity(req ScanRequest) checkpointRequest {
	return checkpointRequest{req.Scopes, req.Query, req.Since, req.Unread}
}
func checkpointFingerprint(req checkpointRequest) string {
	data, _ := json.Marshal(req)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func checkpointError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidScanCheckpoint, fmt.Sprintf(format, args...))
}

func validateCheckpointOptions(req ScanRequest) error {
	if req.RestartCheckpoint && req.Checkpoint == "" {
		return checkpointError("--restart-checkpoint requires --checkpoint")
	}
	if req.Checkpoint == "" {
		return nil
	}
	if req.Cursor != "" {
		return checkpointError("--cursor cannot be combined with --checkpoint")
	}
	path, err := filepath.Abs(req.Checkpoint)
	if err != nil {
		return checkpointError("resolve path: %v", err)
	}
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return checkpointError("file must be a regular private file (0600)")
		}
		if info.Size() > maxScanCheckpointBytes {
			return checkpointError("file exceeds size limit")
		}
	} else if !os.IsNotExist(err) {
		return checkpointError("inspect file: %v", err)
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil || !parent.IsDir() {
		return checkpointError("parent directory must exist")
	}
	return nil
}
func loadScanCheckpoint(path string, req ScanRequest) (scanCheckpoint, bool, error) {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return scanCheckpoint{}, false, nil
	}
	if err != nil {
		return scanCheckpoint{}, false, checkpointError("read file: %v", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxScanCheckpointBytes+1))
	if err != nil || len(data) > maxScanCheckpointBytes {
		return scanCheckpoint{}, false, checkpointError("read failed or size limit exceeded")
	}
	var state scanCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return state, false, checkpointError("corrupt file: %v", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return state, false, checkpointError("trailing data")
	}
	if state.Version != 1 || state.Fingerprint != checkpointFingerprint(state.Request) || state.Fingerprint != checkpointFingerprint(checkpointIdentity(req)) {
		return state, false, checkpointError("different scopes or filters, or unsupported/corrupt state; use original flags or --restart-checkpoint")
	}
	if state.Checksum != checkpointChecksum(state) {
		return state, false, checkpointError("checkpoint integrity check failed")
	}
	if err := validateCheckpointState(state, req); err != nil {
		return state, false, err
	}
	if state.Result.Complete {
		return state, false, checkpointError("traversal already completed; use --restart-checkpoint for a fresh scan")
	}
	return state, true, nil
}
func checkpointChecksum(state scanCheckpoint) string {
	state.Checksum = ""
	data, _ := json.Marshal(state)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateCheckpointState(state scanCheckpoint, req ScanRequest) error {
	r := state.Result
	scopes := uniqueScanScopes(req.Scopes)
	if r.Checkpoint == nil || r.Checkpoint.Pages < 1 || len(r.Coverage) != len(scopes) || r.StartedAt != r.Checkpoint.ObservationStartedAt || r.EndedAt != r.Checkpoint.ObservationEndedAt {
		return checkpointError("missing or inconsistent traversal state")
	}
	for _, stamp := range []string{r.StartedAt, r.EndedAt, r.Checkpoint.PageStartedAt, r.Checkpoint.PageEndedAt} {
		if _, err := time.Parse(time.RFC3339Nano, stamp); err != nil {
			return checkpointError("invalid observation timestamp")
		}
	}
	var cursor scanCursor
	if !r.Complete {
		if r.NextCursor == "" {
			return checkpointError("unfinished traversal has no cursor")
		}
		var err error
		cursor, err = decodeScanCursor(r.NextCursor, req, scopes)
		if err != nil {
			return checkpointError("cursor mismatch: %v", err)
		}
	} else if r.NextCursor != "" {
		return checkpointError("completed traversal has a cursor")
	}
	allowed := map[string]map[string]bool{}
	for i, scope := range scopes {
		c := r.Coverage[i]
		if c.SearchMailbox != scope || c.Count < 0 || c.Remaining < 0 || c.Exhausted && (c.Remaining != 0 || c.Error != "" || c.Truncated) {
			return checkpointError("invalid coverage")
		}
		if !r.Complete {
			s := cursor.Scopes[i]
			if s.Done != c.Exhausted || s.Done && s.After != nil || !s.Done && c.Truncated && s.After == nil || s.After != nil && s.After.ID <= 0 {
				return checkpointError("cursor and coverage disagree")
			}
		} else if !c.Exhausted {
			return checkpointError("completed traversal has unfinished coverage")
		}
		if allowed[scope.Account] == nil {
			allowed[scope.Account] = map[string]bool{}
		}
		allowed[scope.Account][scope.Mailbox] = true
	}
	seen := map[string]bool{}
	for _, m := range r.Messages {
		key := m.Account + "\x00" + m.ID
		if m.ID == "" || seen[key] || len(m.Mailboxes) == 0 || !allowed[m.Account][m.Mailbox] {
			return checkpointError("invalid message identity")
		}
		seen[key] = true
		memberships := map[string]bool{}
		for _, box := range m.Mailboxes {
			if !allowed[m.Account][box] || memberships[box] {
				return checkpointError("invalid membership")
			}
			memberships[box] = true
		}
	}
	return nil
}
func saveScanCheckpoint(path string, state scanCheckpoint) error {
	state.Checksum = checkpointChecksum(state)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(data) > maxScanCheckpointBytes {
		return checkpointError("cumulative state exceeds size limit")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".mail-scan-checkpoint-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
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
func scanCheckpointMessages(req ScanRequest, read func(SearchMailbox, int, *ScanPosition) (scanPage, error)) (ScanResult, error) {
	path, _ := filepath.Abs(req.Checkpoint)
	state := scanCheckpoint{Version: 1, Request: checkpointIdentity(req), Fingerprint: checkpointFingerprint(checkpointIdentity(req))}
	resumed := false
	if !req.RestartCheckpoint {
		var err error
		state, resumed, err = loadScanCheckpoint(path, req)
		if err != nil {
			return ScanResult{}, err
		}
		if !resumed {
			state = scanCheckpoint{Version: 1, Request: checkpointIdentity(req), Fingerprint: checkpointFingerprint(checkpointIdentity(req))}
		}
	}
	pageReq := req
	pageReq.Checkpoint = ""
	pageReq.RestartCheckpoint = false
	if resumed {
		pageReq.Cursor = state.Result.NextCursor
	}
	page, err := scanPageMessages(pageReq, read)
	if err != nil {
		return ScanResult{}, err
	}
	result := page
	for i := range result.Coverage {
		if result.Coverage[i].Error != "" {
			result.Coverage[i].Errors = []string{result.Coverage[i].Error}
		}
	}
	result.Checkpoint = &ScanCheckpointProvenance{Path: path, Resumed: resumed, Pages: 1, ObservationStartedAt: page.StartedAt, ObservationEndedAt: page.EndedAt, PageStartedAt: page.StartedAt, PageEndedAt: page.EndedAt}
	if resumed {
		old := state.Result
		result.StartedAt = old.StartedAt
		result.Checkpoint.ObservationStartedAt = old.StartedAt
		result.Checkpoint.Pages = old.Checkpoint.Pages + 1
		for i := range result.Coverage {
			result.Coverage[i].Count += old.Coverage[i].Count
			result.Coverage[i].Errors = append(append([]string{}, old.Coverage[i].Errors...), result.Coverage[i].Errors...)
		}
		result.Messages = append([]ScanMessage{}, old.Messages...)
		indices := map[string]int{}
		for i, m := range result.Messages {
			indices[m.Account+"\x00"+m.ID] = i
		}
		for _, m := range page.Messages {
			key := m.Account + "\x00" + m.ID
			if i, ok := indices[key]; ok {
				for _, box := range m.Mailboxes {
					found := false
					for _, prior := range result.Messages[i].Mailboxes {
						if prior == box {
							found = true
						}
					}
					if !found {
						result.Messages[i].Mailboxes = append(result.Messages[i].Mailboxes, box)
					}
				}
			} else {
				indices[key] = len(result.Messages)
				result.Messages = append(result.Messages, m)
			}
		}
		sort.SliceStable(result.Messages, func(i, j int) bool { return result.Messages[i].DateReceived > result.Messages[j].DateReceived })
	}
	state.Result = result
	if err := validateCheckpointState(state, req); err != nil {
		return ScanResult{}, err
	}
	if err := saveScanCheckpoint(path, state); err != nil {
		return ScanResult{}, fmt.Errorf("persist scan checkpoint: %w", err)
	}
	return result, nil
}
