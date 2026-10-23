package mail

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// All fixtures are synthetic. The callbacks deliberately bypass Client and
// every local Mail, native bridge, and network access path.
func unsubscribeScanFixture(t *testing.T) UnsubscribeScanRequest {
	t.Helper()
	return UnsubscribeScanRequest{Scopes: []SearchMailbox{{Account: "Synthetic", Mailbox: "INBOX"}}, PageSize: 2, Budget: time.Minute, Checkpoint: filepath.Join(t.TempDir(), "discovery.json")}
}

func unsubscribeSyntheticPages(rows map[string][]int, calls *int) func(context.Context, ScanRequest) (ScanResult, error) {
	return func(_ context.Context, req ScanRequest) (ScanResult, error) {
		if calls != nil {
			*calls++
		}
		return scanPageMessages(req, func(scope SearchMailbox, limit int, after *ScanPosition) (scanPage, error) {
			var available []int
			for _, id := range rows[scope.Mailbox] {
				if after == nil || int64(id) < after.ID {
					available = append(available, id)
				}
			}
			p := scanPage{}
			for _, id := range available[:min(limit, len(available))] {
				p.Messages = append(p.Messages, Message{ID: strconv.Itoa(id), Sender: "newsletter@example.invalid", Subject: "Synthetic newsletter", DateReceived: time.Unix(int64(id), 0).UTC().Format(time.RFC3339)})
				p.Last = &ScanPosition{ReceivedUnix: int64(id), ID: int64(id)}
			}
			p.Remaining = len(available) - len(p.Messages)
			return p, nil
		})
	}
}

func unsubscribeSyntheticLocal(_ context.Context, messages []ScanMessage) ([]UnsubscribeObservation, error) {
	result := []UnsubscribeObservation{}
	for _, m := range messages {
		result = append(result, UnsubscribeObservation{ID: m.ID, Account: m.Account, Mailboxes: append([]string(nil), m.Mailboxes...), Sender: m.Sender, Subject: m.Subject, DateReceived: m.DateReceived, ListID: "synthetic.example.invalid", HeaderStatus: "available", Complete: true, Destinations: []UnsubscribeDestination{{URL: "https://example.invalid/unsubscribe", Kind: "https", Openable: true}}})
	}
	return result, nil
}

func unsubscribeReadState(t *testing.T, path string) unsubscribeCheckpoint {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s unsubscribeCheckpoint
	if err = json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if s.Checksum != unsubscribeStateChecksum(s) {
		t.Fatal("checkpoint checksum does not match persisted state")
	}
	return s
}

func TestUnsubscribeScanTraversalAndBoundedResume(t *testing.T) {
	rows := map[string][]int{"INBOX": {7, 6, 5, 4, 3, 2, 1}}
	req := unsubscribeScanFixture(t)
	calls := 0
	full, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(rows, &calls), unsubscribeSyntheticLocal)
	if err != nil {
		t.Fatal(err)
	}
	if !full.Complete || !full.TraversalComplete || full.Pages != 4 || calls != 4 || full.Readability.Inspected != 7 || len(full.Groups) != 1 || full.Groups[0].MessageCount != 7 || full.Coverage[0].Count != 7 {
		t.Fatalf("unexpected complete traversal: %+v; calls=%d", full, calls)
	}
	bounded := unsubscribeScanFixture(t)
	bounded.MaxPages = 1
	calls = 0
	var resumed UnsubscribeScanResult
	for i := 0; i < 4; i++ {
		resumed, err = scanUnsubscribe(context.Background(), bounded, unsubscribeSyntheticPages(rows, &calls), unsubscribeSyntheticLocal)
		if err != nil {
			t.Fatal(err)
		}
		if resumed.Pages != i+1 || resumed.Pending != 0 || resumed.Resumed != (i > 0) || resumed.Complete != (i == 3) {
			t.Fatalf("invocation %d: %+v", i, resumed)
		}
	}
	if calls != 4 || !reflect.DeepEqual(full.Groups, resumed.Groups) || !reflect.DeepEqual(full.Coverage, resumed.Coverage) || full.Readability != resumed.Readability {
		t.Fatalf("resumed differs from uninterrupted: full=%+v resumed=%+v", full, resumed)
	}
}

func TestUnsubscribeScanPersistsBeforeAndAfterExtraction(t *testing.T) {
	req := unsubscribeScanFixture(t)
	batches := 0
	local := func(ctx context.Context, m []ScanMessage) ([]UnsubscribeObservation, error) {
		state := unsubscribeReadState(t, req.Checkpoint)
		if len(state.Pending) != len(m) || len(state.Observations) != batches*2 {
			t.Fatalf("page not durable before extraction: %+v", state)
		}
		batches++
		return unsubscribeSyntheticLocal(ctx, m)
	}
	result, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(map[string][]int{"INBOX": {3, 2, 1}}, nil), local)
	if err != nil {
		t.Fatal(err)
	}
	state := unsubscribeReadState(t, req.Checkpoint)
	if !result.Complete || len(state.Observations) != 3 || len(state.Pending) != 0 || !state.TraversalComplete {
		t.Fatalf("results not durable: %+v", state)
	}
	info, err := os.Stat(req.Checkpoint)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("checkpoint mode: %v, %v", info, err)
	}
}

func TestUnsubscribeScanCancellationRetainsSuccessAndPending(t *testing.T) {
	req := unsubscribeScanFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := func(ctx context.Context, m []ScanMessage) ([]UnsubscribeObservation, error) {
		result, _ := unsubscribeSyntheticLocal(ctx, m[:1])
		cancel()
		return result, context.Canceled
	}
	first, err := scanUnsubscribe(ctx, req, unsubscribeSyntheticPages(map[string][]int{"INBOX": {3, 2, 1}}, nil), local)
	if err != nil {
		t.Fatal(err)
	}
	state := unsubscribeReadState(t, req.Checkpoint)
	if first.Complete || first.Pending != 1 || first.Readability.Inspected != 1 || len(state.Pending) != 1 || state.Pending[0].ID != "2" || state.Observations[0].ID != "3" {
		t.Fatalf("canceled state: %+v, %+v", first, state)
	}
	seen := map[string]int{}
	resumed, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(map[string][]int{"INBOX": {3, 2, 1}}, nil), func(ctx context.Context, m []ScanMessage) ([]UnsubscribeObservation, error) {
		for _, x := range m {
			seen[x.ID]++
		}
		return unsubscribeSyntheticLocal(ctx, m)
	})
	if err != nil || !resumed.Complete || resumed.Readability.Inspected != 3 || seen["3"] != 0 || seen["2"] != 1 || seen["1"] != 1 {
		t.Fatalf("resume=%+v err=%v seen=%v", resumed, err, seen)
	}
}

func TestUnsubscribeScanRetryIncompleteAndReplay(t *testing.T) {
	req := unsubscribeScanFixture(t)
	pages := 0
	local := func(ctx context.Context, m []ScanMessage) ([]UnsubscribeObservation, error) {
		o, _ := unsubscribeSyntheticLocal(ctx, m)
		o[0].Complete = false
		o[0].Error = "synthetic partial file"
		return o, nil
	}
	first, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(map[string][]int{"INBOX": {1}}, &pages), local)
	if err != nil || first.Complete || !first.TraversalComplete || first.Readability.Partial != 1 || len(first.Exceptions) != 1 || first.Readability.WithOpportunities != 1 || len(first.Groups[0].Destinations) != 1 {
		t.Fatalf("partial=%+v err=%v", first, err)
	}
	noPage := func(context.Context, ScanRequest) (ScanResult, error) {
		t.Fatal("replayed completed traversal enumerated again")
		return ScanResult{}, nil
	}
	noLocal := func(context.Context, []ScanMessage) ([]UnsubscribeObservation, error) {
		t.Fatal("replay extracted again without explicit retry")
		return nil, nil
	}
	replay, err := scanUnsubscribe(context.Background(), req, noPage, noLocal)
	if err != nil || !replay.Resumed || replay.Complete || !reflect.DeepEqual(replay.Groups, first.Groups) {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	req.RetryIncomplete = true
	retried := 0
	fixed, err := scanUnsubscribe(context.Background(), req, noPage, func(ctx context.Context, m []ScanMessage) ([]UnsubscribeObservation, error) {
		retried += len(m)
		return unsubscribeSyntheticLocal(ctx, m)
	})
	if err != nil || !fixed.Complete || retried != 1 || fixed.Readability.Inspected != 1 || fixed.Readability.Enumerated != 1 || fixed.Groups[0].MessageCount != 1 {
		t.Fatalf("retry=%+v err=%v calls=%d", fixed, err, retried)
	}
	req.RetryIncomplete = false
	if _, err = scanUnsubscribe(context.Background(), req, noPage, noLocal); err != nil {
		t.Fatal(err)
	}
	req.Restart = true
	restarted, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(map[string][]int{"INBOX": {2, 1}}, &pages), unsubscribeSyntheticLocal)
	if err != nil || !restarted.Complete || restarted.Resumed || restarted.Readability.Inspected != 2 || pages != 2 {
		t.Fatalf("restart=%+v err=%v pages=%d", restarted, err, pages)
	}
}

func TestUnsubscribeScanNoSourceExceptionPreservesTraversal(t *testing.T) {
	req := unsubscribeScanFixture(t)
	result, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(map[string][]int{"INBOX": {1}}, nil), func(ctx context.Context, m []ScanMessage) ([]UnsubscribeObservation, error) {
		o, _ := unsubscribeSyntheticLocal(ctx, m)
		o[0].HeaderStatus = "unavailable"
		o[0].Complete = false
		o[0].Destinations = nil
		o[0].Error = "no local source"
		return o, nil
	})
	if err != nil || result.Complete || !result.TraversalComplete || result.Readability.Unavailable != 1 || result.Readability.Partial != 0 || len(result.Exceptions) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestUnsubscribeScanDuplicateAcrossPagesRetainsMembership(t *testing.T) {
	req := unsubscribeScanFixture(t)
	req.PageSize = 1
	req.Scopes = append(req.Scopes, SearchMailbox{Account: "Synthetic", Mailbox: "Archive"})
	seen := map[string]int{}
	result, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(map[string][]int{"INBOX": {3, 2}, "Archive": {4, 3}}, nil), func(ctx context.Context, m []ScanMessage) ([]UnsubscribeObservation, error) {
		for _, x := range m {
			seen[x.ID]++
		}
		return unsubscribeSyntheticLocal(ctx, m)
	})
	if err != nil || !result.Complete || result.Readability.Inspected != 3 || seen["3"] != 1 {
		t.Fatalf("result=%+v err=%v seen=%v", result, err, seen)
	}
	state := unsubscribeReadState(t, req.Checkpoint)
	for _, o := range state.Observations {
		if o.ID == "3" {
			if !reflect.DeepEqual(o.Mailboxes, []string{"Archive", "INBOX"}) {
				t.Fatalf("memberships=%v", o.Mailboxes)
			}
			return
		}
	}
	t.Fatal("duplicate message missing")
}

func TestUnsubscribeScanCheckpointScopeMismatch(t *testing.T) {
	for _, change := range []string{"account", "mailbox", "since", "order"} {
		t.Run(change, func(t *testing.T) {
			req := unsubscribeScanFixture(t)
			req.Scopes = append(req.Scopes, SearchMailbox{Account: "Synthetic", Mailbox: "Archive"})
			if _, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(nil, nil), unsubscribeSyntheticLocal); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "account":
				req.Scopes[0].Account = "Other"
			case "mailbox":
				req.Scopes[0].Mailbox = "Other"
			case "since":
				req.Since = "2026-01-01"
			case "order":
				req.Scopes[0], req.Scopes[1] = req.Scopes[1], req.Scopes[0]
			}
			if _, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(nil, nil), unsubscribeSyntheticLocal); !errors.Is(err, ErrInvalidUnsubscribeCheckpoint) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestUnsubscribeScanRejectsUnsafeCheckpointAndConcurrentLock(t *testing.T) {
	for _, kind := range []string{"corrupt", "mode", "symlink", "lock-mode", "lock-symlink", "locked"} {
		t.Run(kind, func(t *testing.T) {
			req := unsubscribeScanFixture(t)
			switch kind {
			case "corrupt":
				if err := os.WriteFile(req.Checkpoint, []byte("broken JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := os.WriteFile(req.Checkpoint, []byte("{}"), 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(req.Checkpoint, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink", "lock-symlink":
				target := filepath.Join(filepath.Dir(req.Checkpoint), "target.json")
				if err := os.WriteFile(target, []byte("untouched"), 0600); err != nil {
					t.Fatal(err)
				}
				link := req.Checkpoint
				if kind == "lock-symlink" {
					link += ".lock"
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			case "lock-mode":
				if err := os.WriteFile(req.Checkpoint+".lock", nil, 0644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(req.Checkpoint+".lock", 0644); err != nil {
					t.Fatal(err)
				}
			case "locked":
				release, err := lockUnsubscribeCheckpoint(req.Checkpoint)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			}
			page := func(context.Context, ScanRequest) (ScanResult, error) {
				t.Fatal("unsafe checkpoint reached enumeration")
				return ScanResult{}, nil
			}
			_, err := scanUnsubscribe(context.Background(), req, page, unsubscribeSyntheticLocal)
			if !errors.Is(err, ErrInvalidUnsubscribeCheckpoint) {
				t.Fatalf("err=%v", err)
			}
			if kind == "symlink" || kind == "lock-symlink" {
				data, err := os.ReadFile(filepath.Join(filepath.Dir(req.Checkpoint), "target.json"))
				if err != nil || string(data) != "untouched" {
					t.Fatalf("symlink target modified: %q %v", data, err)
				}
			}
		})
	}
}

func TestUnsubscribeScanCanceledRetryCountsUniqueMessages(t *testing.T) {
	req := unsubscribeScanFixture(t)
	partial := func(ctx context.Context, m []ScanMessage) ([]UnsubscribeObservation, error) {
		o, _ := unsubscribeSyntheticLocal(ctx, m)
		for i := range o {
			o[i].Complete = false
		}
		return o, nil
	}
	if _, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(map[string][]int{"INBOX": {2, 1}}, nil), partial); err != nil {
		t.Fatal(err)
	}
	req.RetryIncomplete = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := scanUnsubscribe(ctx, req, unsubscribeSyntheticPages(nil, nil), func(ctx context.Context, m []ScanMessage) ([]UnsubscribeObservation, error) {
		o, _ := unsubscribeSyntheticLocal(ctx, m[:1])
		cancel()
		return o, context.Canceled
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Pending != 1 || result.Readability.Inspected != 2 || result.Readability.Enumerated != 2 {
		t.Fatalf("retry must count unique message identities: %+v", result)
	}
}

func TestUnsubscribeScanFailedMailboxResumesWithoutRepeatingReadableScope(t *testing.T) {
	req := unsubscribeScanFixture(t)
	req.Scopes = append(req.Scopes, SearchMailbox{Account: "Synthetic", Mailbox: "Archive"})
	fail := true
	calls := map[string]int{}
	page := func(_ context.Context, r ScanRequest) (ScanResult, error) {
		return scanPageMessages(r, func(scope SearchMailbox, _ int, _ *ScanPosition) (scanPage, error) {
			calls[scope.Mailbox]++
			if scope.Mailbox == "Archive" && fail {
				return scanPage{}, errors.New("synthetic inaccessible mailbox")
			}
			id := "1"
			if scope.Mailbox == "Archive" {
				id = "2"
			}
			return scanPage{Messages: []Message{{ID: id, Sender: "newsletter@example.invalid"}}}, nil
		})
	}
	first, err := scanUnsubscribe(context.Background(), req, page, unsubscribeSyntheticLocal)
	if err != nil || first.TraversalComplete || first.Complete || first.Readability.Inspected != 1 || first.Coverage[1].Error == "" || calls["Archive"] != 1 {
		t.Fatalf("first=%+v err=%v calls=%v", first, err, calls)
	}
	fail = false
	resumed, err := scanUnsubscribe(context.Background(), req, page, unsubscribeSyntheticLocal)
	if err != nil || !resumed.Complete || resumed.Readability.Inspected != 2 || calls["INBOX"] != 1 || calls["Archive"] != 2 {
		t.Fatalf("resumed=%+v err=%v calls=%v", resumed, err, calls)
	}
}

func TestUnsubscribeScanUnavailableRetryRetainsRecoveredControlsAndListIdentity(t *testing.T) {
	req := unsubscribeScanFixture(t)
	evidence := UnsubscribeEvidence{MessageID: "1", Source: "mime-body", MIMEPart: "1.1", Text: "Unsubscribe", Kind: "unsubscribe", Selected: true}
	initial := func(ctx context.Context, messages []ScanMessage) ([]UnsubscribeObservation, error) {
		observations, _ := unsubscribeSyntheticLocal(ctx, messages)
		observations[0].Complete = false
		observations[0].Error = "synthetic partial source"
		observations[0].Destinations[0].Evidence = []UnsubscribeEvidence{evidence}
		return observations, nil
	}
	first, err := scanUnsubscribe(context.Background(), req, unsubscribeSyntheticPages(map[string][]int{"INBOX": {1}}, nil), initial)
	if err != nil || first.Complete || !first.TraversalComplete || len(first.Groups) != 1 {
		t.Fatalf("initial=%+v err=%v", first, err)
	}
	originalKey := first.Groups[0].Key
	req.RetryIncomplete = true
	noPage := func(context.Context, ScanRequest) (ScanResult, error) {
		t.Fatal("completed traversal enumerated during retry")
		return ScanResult{}, nil
	}
	retry := func(_ context.Context, messages []ScanMessage) ([]UnsubscribeObservation, error) {
		if len(messages) != 1 || messages[0].ID != "1" {
			t.Fatalf("retry messages=%+v", messages)
		}
		return []UnsubscribeObservation{{ID: "1", Account: "Synthetic", Mailboxes: []string{"INBOX"}, HeaderStatus: "unavailable", Error: "synthetic source disappeared", Complete: false}}, nil
	}
	result, err := scanUnsubscribe(context.Background(), req, noPage, retry)
	if err != nil {
		t.Fatal(err)
	}
	state := unsubscribeReadState(t, req.Checkpoint)
	if len(state.Observations) != 1 {
		t.Fatalf("observations=%+v", state.Observations)
	}
	observation := state.Observations[0]
	if observation.HeaderStatus != "unavailable" || observation.Error != "synthetic source disappeared" || observation.Complete || observation.ListID != "synthetic.example.invalid" {
		t.Fatalf("retry must preserve current failure and known list identity: %+v", observation)
	}
	if len(observation.Destinations) != 1 || observation.Destinations[0].URL != "https://example.invalid/unsubscribe" || !reflect.DeepEqual(observation.Destinations[0].Evidence, []UnsubscribeEvidence{evidence}) {
		t.Fatalf("recovered destination or evidence lost: %+v", observation.Destinations)
	}
	if result.Complete || !result.TraversalComplete || result.Readability.Enumerated != 1 || result.Readability.Inspected != 1 || result.Readability.Unavailable != 1 || len(result.Groups) != 1 || result.Groups[0].Key != originalKey || result.Groups[0].ListID != "synthetic.example.invalid" || result.Groups[0].MessageCount != 1 || !reflect.DeepEqual(result.Groups[0].Destinations, observation.Destinations) {
		t.Fatalf("retry group shifted or controls lost: %+v", result)
	}
}
