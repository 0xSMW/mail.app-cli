package mail

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type draftRegressionAttachment struct {
	Name   string `json:"name"`
	Base64 string `json:"base64"`
}

type draftRegressionState struct {
	Enumerated          []string                    `json:"enumerated"`
	Exported            []string                    `json:"exported"`
	Deleted             []string                    `json:"deleted"`
	Composed            int                         `json:"composed"`
	Sent                int                         `json:"sent"`
	OriginalRemoved     bool                        `json:"originalRemoved"`
	SentAttachments     []draftRegressionAttachment `json:"sentAttachments"`
	SentBody            string                      `json:"sentBody"`
	PreparedDeleted     []string                    `json:"preparedDeleted"`
	PreparedEnumerated  []string                    `json:"preparedEnumerated"`
	PreparedByID        []string                    `json:"preparedById"`
	OwnedCloseAfterSend int                         `json:"ownedCloseAfterSend"`
}

func installDraftRegressionBridge(t *testing.T, snapshot *draftSnapshot, attachments []draftRegressionAttachment, specialState string) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the production JXA mailbox regression tests")
	}
	mock, err := filepath.Abs("testdata/draft_send_regression_mock.js")
	if err != nil {
		t.Fatal(err)
	}
	config, err := json.Marshal(map[string]any{"snapshot": snapshot, "attachments": attachments, "specialState": specialState})
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(state, []byte(`{"enumerated":[],"exported":[],"deleted":[],"composed":0,"sent":0,"originalRemoved":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRAFT_REGRESSION_CONFIG", string(config))
	t.Setenv("DRAFT_REGRESSION_STATE", state)
	shellQuote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	writeFakeOsaScript(t, "exec "+shellQuote(node)+" "+shellQuote(mock)+" \"$@\"")
	return state
}

func readDraftRegressionState(t *testing.T, path string) draftRegressionState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state draftRegressionState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func draftRegressionSnapshot(t *testing.T, mimeNames []string) *draftSnapshot {
	t.Helper()
	snapshot := simpleDraftSnapshot(t)
	snapshot.Message.Mailbox = "Bozze"
	if len(mimeNames) == 0 {
		return snapshot
	}
	var source strings.Builder
	source.WriteString("From: Person <alias@example.test>\r\nTo: to@example.test\r\nCc: cc@example.test\r\nSubject: Draft\r\nContent-Type: multipart/mixed; boundary=fixture\r\n\r\n--fixture\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody\r\n\r\n")
	for _, name := range mimeNames {
		fmt.Fprintf(&source, "--fixture\r\nContent-Type: application/octet-stream\r\nContent-Disposition: %s\r\nContent-Transfer-Encoding: base64\r\n\r\n%s\r\n", mime.FormatMediaType("attachment", map[string]string{"filename": name}), base64.StdEncoding.EncodeToString([]byte("preserved bytes")))
	}
	source.WriteString("--fixture--\r\n")
	snapshot.Source = source.String()
	return snapshot
}

func TestSendDraftSpecialMailboxRegression(t *testing.T) {
	for _, tc := range []struct {
		name         string
		mimeNames    []string
		exportedName string
	}{
		{name: "no attachments"},
		{name: "ordinary attachment", mimeNames: []string{"report.txt"}, exportedName: "report.txt"},
		{name: "NFC MIME and NFD export", mimeNames: []string{"caf\u00e9.txt"}, exportedName: "cafe\u0301.txt"},
		{name: "NFD MIME and NFC export", mimeNames: []string{"cafe\u0301.txt"}, exportedName: "caf\u00e9.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attachments []draftRegressionAttachment
			if tc.exportedName != "" {
				attachments = []draftRegressionAttachment{{Name: tc.exportedName, Base64: base64.StdEncoding.EncodeToString([]byte("preserved bytes"))}}
			}
			statePath := installDraftRegressionBridge(t, draftRegressionSnapshot(t, tc.mimeNames), attachments, "")
			result, err := NewClient().SendDraftResult("Work", "42")
			if err != nil {
				t.Fatalf("SendDraftResult failed: %v; state=%+v", err, readDraftRegressionState(t, statePath))
			}
			state := readDraftRegressionState(t, statePath)
			if !result.Submitted || result.CleanupPending || state.Sent != 1 || !state.OriginalRemoved || strings.Join(state.Deleted, ",") != "special" || state.SentBody != "body\r\n" {
				t.Fatalf("result=%+v state=%+v", result, state)
			}
			if strings.Join(state.PreparedDeleted, ",") != "700" || state.OwnedCloseAfterSend != 1 || len(state.PreparedByID) == 0 {
				t.Fatalf("same outgoing preparation/cleanup not verified: %+v", state)
			}
			for _, id := range append(append([]string{}, state.PreparedEnumerated...), state.PreparedByID...) {
				if id != "700" {
					t.Fatalf("prepared cleanup accessed unrelated stored ID: %+v", state)
				}
			}
			for _, kind := range append(append([]string{}, state.Enumerated...), state.Exported...) {
				if kind != "special" {
					t.Fatalf("accessed unrelated draft attachments: %+v", state)
				}
			}
			if len(state.Exported) != len(attachments) || len(state.SentAttachments) != len(attachments) {
				t.Fatalf("attachment preservation counts differ: %+v", state)
			}
			for i, attachment := range attachments {
				if state.SentAttachments[i] != attachment {
					t.Fatalf("submission changed attachment: %+v", state)
				}
			}
		})
	}
}

func TestSendDraftUnavailableSpecialMailboxRegression(t *testing.T) {
	for _, specialState := range []string{"missing", "ambiguous"} {
		t.Run(specialState, func(t *testing.T) {
			statePath := installDraftRegressionBridge(t, draftRegressionSnapshot(t, nil), nil, specialState)
			result, err := NewClient().SendDraftResult("Work", "42")
			state := readDraftRegressionState(t, statePath)
			if err == nil || result.Submitted || state.Sent != 0 || state.Composed != 0 || len(state.Deleted) != 0 || len(state.Exported) != 0 || len(state.Enumerated) != 0 {
				t.Fatalf("unavailable special mailbox was not safe: result=%+v err=%v state=%+v", result, err, state)
			}
		})
	}
}

func TestSendDraftChangedOriginalAfterPreparedCleanupRegression(t *testing.T) {
	statePath := installDraftRegressionBridge(t, draftRegressionSnapshot(t, nil), nil, "changed-original-after-send")
	result, err := NewClient().SendDraftResult("Work", "42")
	state := readDraftRegressionState(t, statePath)
	if err == nil || !result.Submitted || !result.CleanupPending || result.Uncertain || state.Sent != 1 || state.OriginalRemoved || len(state.Deleted) != 0 || strings.Join(state.PreparedDeleted, ",") != "700" {
		t.Fatalf("changed original was not retained after exact prepared cleanup: result=%+v err=%v state=%+v", result, err, state)
	}
}

func TestDraftMIMEAttachmentNormalizationRegression(t *testing.T) {
	for _, tc := range []struct {
		name, actualName, expectedName string
		actualBytes, expectedBytes     string
		actualCount, expectedCount     int
		wantError                      bool
	}{
		{"NFC MIME NFD file", "cafe\u0301.txt", "caf\u00e9.txt", "same", "same", 1, 1, false},
		{"NFD MIME NFC file", "caf\u00e9.txt", "cafe\u0301.txt", "same", "same", 1, 1, false},
		{"different bytes", "cafe\u0301.txt", "caf\u00e9.txt", "changed", "same", 1, 1, true},
		{"different names", "other.txt", "caf\u00e9.txt", "same", "same", 1, 1, true},
		{"duplicate count differs", "cafe\u0301.txt", "caf\u00e9.txt", "same", "same", 1, 2, true},
		{"duplicates preserved", "cafe\u0301.txt", "caf\u00e9.txt", "same", "same", 2, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			for i := 0; i < tc.actualCount; i++ {
				path := filepath.Join(t.TempDir(), tc.actualName)
				if err := os.WriteFile(path, []byte(tc.actualBytes), 0600); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, path)
			}
			var expected []draftMIMEAttachment
			for i := 0; i < tc.expectedCount; i++ {
				expected = append(expected, draftMIMEAttachment{tc.expectedName, []byte(tc.expectedBytes)})
			}
			if err := compareDraftMIMEAttachments(paths, expected); (err != nil) != tc.wantError {
				t.Fatalf("error=%v; wantError=%t", err, tc.wantError)
			}
		})
	}
}

func TestDraftMIMEAttachmentMultiplicityRegression(t *testing.T) {
	for _, tc := range []struct {
		name        string
		secondBytes string
		secondName  string
		wantError   bool
	}{
		{"both copies preserved", "first", "caf\u00e9.txt", false},
		{"second copy has different bytes", "second", "caf\u00e9.txt", true},
		{"second copy has different name", "first", "other.txt", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			for i := 0; i < 2; i++ {
				path := filepath.Join(t.TempDir(), "cafe\u0301.txt")
				if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
					t.Fatal(err)
				}
				paths = append(paths, path)
			}
			expected := []draftMIMEAttachment{{"caf\u00e9.txt", []byte("first")}, {tc.secondName, []byte(tc.secondBytes)}}
			if err := compareDraftMIMEAttachments(paths, expected); (err != nil) != tc.wantError {
				t.Fatalf("same-count attachment multiset: error=%v; wantError=%t", err, tc.wantError)
			}
		})
	}
}
