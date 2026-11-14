package mail

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadCheckpointRecoveryExplicitAccountRequiresExactName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	refs := checkpointRefs("1")
	read := func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
		return checkpointMessage(ref.MessageID), nil, nil
	}
	if _, err := readSelectedCheckpointFixture(refs, SelectedReadOptions{Checkpoint: path}, false, false, nil, read); err != nil {
		t.Fatal(err)
	}
	forbidden := func() { t.Fatal("explicit case-only account mismatch reached external boundary") }
	result, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path, Account: "work"}, false, false, func(MessageRef) (*Message, error) { forbidden(); return nil, nil }, func(MessageRef, int) (*Message, *PhaseTrace, error) { forbidden(); return nil, nil, nil }, selectedReadHooks{AccountID: func(string) (string, error) { forbidden(); return "", nil }, Recover: func(readCheckpointOrigin) (*Message, error) { forbidden(); return nil, nil }, Revision: func(MessageRef, *StableIdentity) (string, error) { forbidden(); return "", nil }})
	if err != nil || result.Complete || result.Items[0].Message != nil || !strings.Contains(result.Items[0].Error, "explicit account") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestReadCheckpointRecoveryAccountCannotChangeDuringInitialExtraction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	refs := checkpointRefs("1")
	calls := 0
	hooks := selectedReadHooks{AccountID: func(string) (string, error) {
		calls++
		if calls == 1 {
			return "original-account", nil
		}
		return "replacement-account", nil
	}}
	result, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, nil, func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
		return checkpointMessage(ref.MessageID), nil, nil
	}, hooks)
	if err != nil || result.Complete || result.Items[0].Message != nil || !strings.Contains(result.Items[0].Error, "account identity changed") {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	state, err := loadReadCheckpoint(path)
	if err != nil || len(state.Origins) != 0 {
		t.Fatalf("failed extraction invented origin=%+v err=%v", state, err)
	}
}

func TestReadCheckpointRecoveryRejectsRereadIdentityBeforeCheckpointReplacement(t *testing.T) {
	for _, kind := range []string{"rfc", "sender", "account", "post-read-metadata", "account-drift"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "read.json")
			refs := checkpointRefs("1")
			originalRead := func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
				return checkpointMessage(ref.MessageID), nil, nil
			}
			if _, err := readSelectedCheckpointFixture(refs, SelectedReadOptions{Checkpoint: path}, false, false, nil, originalRead); err != nil {
				t.Fatal(err)
			}
			accountCalls := 0
			hooks := selectedReadHooks{
				AccountID: func(string) (string, error) {
					accountCalls++
					if kind == "account-drift" && accountCalls > 1 {
						return "replacement", nil
					}
					return "original", nil
				},
				Recover:  func(origin readCheckpointOrigin) (*Message, error) { return checkpointMessage("1"), nil },
				Revision: func(MessageRef, *StableIdentity) (string, error) { return strings.Repeat("b", 64), nil },
				Confirm: func(MessageRef) (*Message, error) {
					message := checkpointMessage("1")
					if kind == "post-read-metadata" {
						message.RFCMessageID = "<wrong@fixture.test>"
					}
					return message, nil
				},
			}
			read := func(MessageRef, int) (*Message, *PhaseTrace, error) {
				message := checkpointMessage("1")
				message.Content = "new body"
				switch kind {
				case "rfc":
					message.RFCMessageID = "<wrong@fixture.test>"
				case "sender":
					message.Sender = "other@fixture.test"
				case "account":
					message.Account = "Other"
				}
				return message, nil, nil
			}
			result, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, func(MessageRef) (*Message, error) { return checkpointMessage("1"), nil }, read, hooks)
			if err != nil || result.Complete || result.Items[0].Message != nil || result.Items[0].Error == "" {
				t.Fatalf("unchecked replacement=%+v err=%v", result, err)
			}
			state, err := loadReadCheckpoint(path)
			if err != nil || state.Result.Items[0].Message.Content != "full extracted body 1" || state.Origins["1"].Identity.RFCMessageID != "<1@fixture.test>" {
				t.Fatalf("original evidence lost: %+v err=%v", state, err)
			}
		})
	}
}

func TestReadCheckpointRecoveryLegacyWrongLocalSelectionIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	state := readCheckpoint{Version: 1, Selection: []string{"1"}, Result: SelectedReadResult{Complete: true, Items: []MessageReadResult{{ID: "1", Account: "Work", Mailbox: "INBOX", Message: checkpointMessage("2")}}}}
	if err := saveReadCheckpoint(path, state); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReadCheckpoint(path); !errors.Is(err, ErrInvalidReadCheckpoint) {
		t.Fatalf("invalid legacy selection accepted: %v", err)
	}
}

func TestReadCheckpointRecoveryNonCheckpointTimeoutStaysPerMessage(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nwhile IFS= read -r line; do /bin/sleep 2; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	client := NewClient()
	_ = client.SetBodySource(BodySourceMail)
	start := time.Now()
	result, err := client.ReadSelectedMessagesWithOptions(checkpointRefs("1"), 25*time.Millisecond, 3*time.Second, SelectedReadOptions{})
	if err != nil || result.Complete || result.Items[0].Error == "" || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("timeout expanded to total budget: %+v err=%v elapsed=%s", result, err, time.Since(start))
	}
}

func TestReadCheckpointRecoveryPinsMigratedAccountBeforeDiscovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "read.json")
	refs := checkpointRefs("1")
	read := func(ref MessageRef, _ int) (*Message, *PhaseTrace, error) {
		return checkpointMessage(ref.MessageID), nil, nil
	}
	if _, err := readSelectedCheckpointFixture(refs, SelectedReadOptions{Checkpoint: path}, false, false, nil, read); err != nil {
		t.Fatal(err)
	}
	legacy, err := loadReadCheckpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Origins = nil
	legacy.Revisions = nil
	if err := saveReadCheckpoint(path, legacy); err != nil {
		t.Fatal(err)
	}
	discovered := false
	hooks := selectedReadHooks{AccountID: func(string) (string, error) { return "live-account", nil }, Revision: func(MessageRef, *StableIdentity) (string, error) { return strings.Repeat("b", 64), nil }, Recover: func(origin readCheckpointOrigin) (*Message, error) {
		discovered = true
		if origin.AccountID != "live-account" {
			t.Fatalf("migration discovery was not bound to captured live account: %+v", origin)
		}
		return checkpointMessage("1"), nil
	}, Confirm: func(MessageRef) (*Message, error) { return checkpointMessage("1"), nil }}
	result, err := readSelectedWithCheckpoint(refs, SelectedReadOptions{Checkpoint: path}, false, false, func(MessageRef) (*Message, error) { return checkpointMessage("1"), nil }, read, hooks)
	if err != nil || !result.Complete || !discovered {
		t.Fatalf("migrated=%+v discovered=%v err=%v", result, discovered, err)
	}
	saved, err := loadReadCheckpoint(path)
	if err != nil || saved.Origins["1"].AccountID != "live-account" {
		t.Fatalf("migrated origin=%+v err=%v", saved, err)
	}
}
