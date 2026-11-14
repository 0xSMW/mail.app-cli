package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func checkpointMessage(id string) *Message {
	return &Message{ID: id, Account: "Work", Mailbox: "INBOX", RFCMessageID: "<" + id + "@fixture.test>", Sender: "Sender <sender@fixture.test>", Subject: "Message " + id, DateSent: "2026-11-12T12:00:00Z", MessageSize: 100, Content: "full extracted body " + id, ContentSource: "disk", Read: false, BodyDiagnostics: &BodyDiagnostics{}, Links: []MessageLink{{URL: "https://fixture.test/" + id, Source: "plain", Selected: true, Kind: "link", Openable: true}}, LinkStatus: &LinkExtractionStatus{Complete: true, Source: "disk"}, Unsubscribe: &MessageUnsubscribe{HeaderStatus: "available"}, InReplyTo: "<thread@fixture.test>"}
}
func checkpointRefs(ids ...string) []MessageRef {
	var out []MessageRef
	for _, id := range ids {
		out = append(out, MessageRef{AccountName: "Work", MailboxName: "INBOX", MessageID: id})
	}
	return out
}

func TestReadCheckpointRetriesOnlyFailedReadsAndRefreshesMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	refs := checkpointRefs("1", "2", "3")
	calls := map[string]int{}
	read := func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
		calls[ref.MessageID]++
		if ref.MessageID == "2" && calls["2"] == 1 {
			return nil, &PhaseTrace{Pending: "content"}, context.DeadlineExceeded
		}
		return checkpointMessage(ref.MessageID), nil, nil
	}
	metadata := func(ref MessageRef) (*Message, error) {
		m := checkpointMessage(ref.MessageID)
		m.Read = true
		m.Flagged = true
		m.Mailbox = "Archive"
		m.Content = "must not use metadata body"
		return m, nil
	}
	first, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, metadata, read)
	if err != nil || first.Complete || calls["2"] != 1 || len(first.Items) != 3 {
		t.Fatalf("first=%+v calls=%v err=%v", first, calls, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("checkpoint permissions: %v %v", info, err)
	}
	refs[0].MailboxName = "Archive"
	refs[2].MailboxName = "Archive"
	var events bytes.Buffer
	next, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path, Events: &events}, false, false, metadata, read)
	if err != nil || !next.Complete || calls["1"] != 1 || calls["2"] != 2 || calls["3"] != 1 {
		t.Fatalf("next=%+v calls=%v err=%v", next, calls, err)
	}
	for _, i := range []int{0, 2} {
		m := next.Items[i].Message
		if !m.Read || !m.Flagged || m.Mailbox != "Archive" || m.Content != fmt.Sprintf("full extracted body %d", i+1) || len(m.Links) != 1 || m.LinkStatus == nil || !m.LinkStatus.Complete || m.Unsubscribe == nil || m.InReplyTo != "<thread@fixture.test>" {
			t.Fatalf("stale metadata/body: %+v", m)
		}
	}
	for _, phase := range []string{"metadata_started", "metadata_completed", "body_started", "body_completed", "item_completed", "completed"} {
		if !strings.Contains(events.String(), `"event":"`+phase+`"`) {
			t.Fatalf("missing %s: %s", phase, events.String())
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(events.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("bad JSONL: %s", line)
		}
	}
}

type interruptedReadEvents struct{ writes int }

func (w *interruptedReadEvents) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == 5 {
		return 0, errors.New("simulated interruption")
	}
	return len(p), nil
}
func TestReadCheckpointDurablyRetainsBodyAcrossInterruptedInvocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	refs := checkpointRefs("1", "2")
	calls := map[string]int{}
	read := func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
		calls[ref.MessageID]++
		return checkpointMessage(ref.MessageID), nil, nil
	}
	metadata := func(ref MessageRef) (*Message, error) { return checkpointMessage(ref.MessageID), nil }
	result, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path, Events: &interruptedReadEvents{}}, false, false, metadata, read)
	if err == nil || result.Complete || calls["1"] != 1 || calls["2"] != 0 {
		t.Fatalf("interruption result=%+v calls=%v err=%v", result, calls, err)
	}
	state, err := loadReadCheckpoint(path)
	if err != nil || state.Result.Complete || len(state.Result.Items) != 1 {
		t.Fatalf("durable state=%+v err=%v", state, err)
	}
	result, err = readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, metadata, read)
	if err != nil || !result.Complete || calls["1"] != 1 || calls["2"] != 1 {
		t.Fatalf("resume=%+v calls=%v err=%v", result, calls, err)
	}
}

func TestReadCheckpointRejectsStaleIdentityAndMissingRegeneratedID(t *testing.T) {
	for _, kind := range []string{"different-rfc", "different-subject", "different-size", "missing-local-id", "metadata-timeout"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "read.json")
			refs := checkpointRefs("1")
			calls := 0
			read := func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
				calls++
				return checkpointMessage(ref.MessageID), nil, nil
			}
			metadata := func(ref MessageRef) (*Message, error) {
				m := checkpointMessage(ref.MessageID)
				switch kind {
				case "different-rfc":
					m.RFCMessageID = "<replacement@fixture.test>"
				case "different-subject":
					m.Subject = "replacement"
				case "different-size":
					m.MessageSize++
				case "missing-local-id":
					return nil, nil
				case "metadata-timeout":
					return nil, context.DeadlineExceeded
				}
				return m, nil
			}
			if _, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, metadata, read); err != nil {
				t.Fatal(err)
			}
			result, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, metadata, read)
			if err != nil || result.Complete || calls != 1 || result.Items[0].Message != nil || !strings.Contains(result.Items[0].Error, "stale read checkpoint") {
				t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
			}
			third, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, metadata, read)
			if err != nil || third.Complete || calls != 1 || third.Items[0].Message != nil || !strings.Contains(third.Items[0].Error, "stale read checkpoint") {
				t.Fatalf("third invocation extracted recycled ID: result=%+v calls=%d err=%v", third, calls, err)
			}

		})
	}
}

func TestReadCheckpointRefusesMalformedInsecureOrDifferentRequestBeforeReads(t *testing.T) {
	for _, kind := range []string{"json", "checksum", "private", "selection", "source", "alternatives"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "read.json")
			refs := checkpointRefs("1")
			read := func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
				return checkpointMessage(ref.MessageID), nil, nil
			}
			metadata := func(MessageRef) (*Message, error) {
				t.Fatal("metadata called for rejected checkpoint")
				return nil, nil
			}
			if _, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, metadata, read); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "json":
				os.WriteFile(path, []byte("{"), 0600)
			case "checksum":
				data, _ := os.ReadFile(path)
				data = bytes.Replace(data, []byte("full extracted body"), []byte("edited body"), 1)
				os.WriteFile(path, data, 0600)
			case "private":
				os.Chmod(path, 0644)
			case "selection":
				refs = checkpointRefs("2")
			}
			_, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, kind == "source", kind == "alternatives", metadata, func(MessageRef, int) (*Message, *PhaseTrace, error) {
				t.Fatal("body called for rejected checkpoint")
				return nil, nil, nil
			})
			if !errors.Is(err, ErrInvalidReadCheckpoint) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestReadCheckpointPreservesAllStaleEvidenceAcrossLaterSuccessfulItems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	refs := checkpointRefs("1", "2")
	calls := map[string]int{}
	read := func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
		calls[ref.MessageID]++
		return checkpointMessage(ref.MessageID), nil, nil
	}
	changed := false
	metadata := func(ref MessageRef) (*Message, error) {
		m := checkpointMessage(ref.MessageID)
		if changed && ref.MessageID == "1" {
			m.RFCMessageID = "<replacement@fixture.test>"
		}
		return m, nil
	}
	if _, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, metadata, read); err != nil {
		t.Fatal(err)
	}
	changed = true
	for invocation := 2; invocation <= 3; invocation++ {
		result, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, metadata, read)
		if err != nil || result.Complete || result.Items[0].Message != nil || result.Items[0].Error == "" || result.Items[1].Message == nil || calls["1"] != 1 || calls["2"] != 1 {
			t.Fatalf("invocation %d result=%+v calls=%v err=%v", invocation, result, calls, err)
		}
	}
}

func TestReadCheckpointResumeMetadataHonorsPerMessageTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	refs := checkpointRefs("1")
	if _, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, nil, func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
		return checkpointMessage(ref.MessageID), nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nexec /bin/sleep 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "automation.lock"))
	started := time.Now()
	result, err := NewClient().ReadSelectedMessagesWithOptions(refs, 25*time.Millisecond, 3*time.Second, SelectedReadOptions{Checkpoint: path})
	elapsed := time.Since(started)
	if err != nil || result.Complete || len(result.Items) != 1 || !strings.Contains(result.Items[0].Error, "stale read checkpoint") || elapsed > 500*time.Millisecond {
		t.Fatalf("result=%+v err=%v elapsed=%s", result, err, elapsed)
	}
}

func TestReadCheckpointRetainsFullSelectionAcrossMissingLocatedIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	opts := SelectedReadOptions{Checkpoint: path, SelectionIDs: []string{"1", "2"}}
	calls := map[string]int{}
	read := func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
		calls[ref.MessageID]++
		return checkpointMessage(ref.MessageID), nil, nil
	}
	metadata := func(ref MessageRef) (*Message, error) { return checkpointMessage(ref.MessageID), nil }
	first, err := readSelectedWithCheckpoint(checkpointRefs("1"), opts, false, false, metadata, read)
	if err != nil || first.Complete || calls["1"] != 1 || calls["2"] != 0 {
		t.Fatalf("first=%+v calls=%v err=%v", first, calls, err)
	}
	second, err := readSelectedWithCheckpoint(checkpointRefs("1", "2"), opts, false, false, metadata, read)
	if err != nil || !second.Complete || calls["1"] != 1 || calls["2"] != 1 {
		t.Fatalf("second=%+v calls=%v err=%v", second, calls, err)
	}
	// Losing the cached local ID in a later locator result must preserve its
	// original success as evidence, so a later account change cannot bypass it.
	third, err := readSelectedWithCheckpoint(checkpointRefs("2"), opts, false, false, metadata, read)
	if err != nil || third.Complete {
		t.Fatalf("third=%+v err=%v", third, err)
	}
	moved := checkpointRefs("1", "2")
	moved[0].AccountName = "Other"
	for invocation := 0; invocation < 2; invocation++ {
		result, err := readSelectedWithCheckpoint(moved, opts, false, false, metadata, read)
		if err != nil || result.Complete || result.Items[0].Message != nil || !strings.Contains(result.Items[0].Error, "checkpoint account") || calls["1"] != 1 || calls["2"] != 1 {
			t.Fatalf("account-changed=%+v calls=%v err=%v", result, calls, err)
		}
	}
}
