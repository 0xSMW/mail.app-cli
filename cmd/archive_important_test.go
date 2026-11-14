package cmd

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func importantArchiveItems() []batchItem {
	return []batchItem{{ID: "1", Account: "Gmail", SourceMailbox: "INBOX"}, {ID: "2", Account: "Gmail", SourceMailbox: "All Mail"}}
}

func verifiedImportantResult(r mail.GmailLabelRequest) mail.GmailLabelResult {
	result := mail.GmailLabelResult{Status: "changed_verified", Account: r.Account, ProviderConfirmed: true, MailStatus: "verified", ReceiptPath: "/receipts/" + r.MessageID,
		Identity:   mail.StableIdentity{RFCMessageID: "message@example.com", Sender: "sender@example.com", Subject: "Example", DateSent: "2026-01-01T00:00:00Z", MessageSize: 42},
		MailBefore: &mail.GmailLabelSnapshot{LocalIDs: []string{r.MessageID}, Memberships: []string{"INBOX", "Important", "All Mail"}},
		MailAfter:  &mail.GmailLabelSnapshot{LocalIDs: []string{r.MessageID}, Memberships: []string{"INBOX", "All Mail"}}}
	if r.Mailbox == "All Mail" {
		result.MailBefore.Memberships = []string{"Important", "All Mail"}
		result.MailAfter.Memberships = []string{"All Mail"}
	}
	return result
}

func TestArchiveImportantRequiresExplicitOptIn(t *testing.T) {
	results, err := prepareArchiveImportant(context.Background(), batchOptions{Action: "archive"}, importantArchiveItems(), false,
		func(string) (bool, error) { t.Fatal("implicit Gmail probe"); return false, nil },
		func(string) (gmailLabelApply, error) { t.Fatal("implicit provider access"); return nil, nil })
	if err != nil || results != nil {
		t.Fatalf("results=%+v err=%v", results, err)
	}
}

func TestArchiveImportantPreflightsAllBeforeRemovalIncludingArchived(t *testing.T) {
	var calls []string
	apply := func(_ context.Context, request mail.GmailLabelRequest) (mail.GmailLabelResult, error) {
		if request.Label != "IMPORTANT" || request.Add {
			t.Fatalf("unexpected label request: %+v", request)
		}
		prefix := "change:"
		result := verifiedImportantResult(request)
		if request.DryRun {
			prefix = "preflight:"
			result = mail.GmailLabelResult{Status: "dry_run"}
		}
		calls = append(calls, prefix+request.MessageID+":"+request.Mailbox)
		return result, nil
	}
	results, err := prepareArchiveImportant(context.Background(), batchOptions{Action: "archive"}, importantArchiveItems(), true,
		func(string) (bool, error) { return true, nil }, func(string) (gmailLabelApply, error) { return apply, nil })
	want := []string{"preflight:1:INBOX", "preflight:2:All Mail", "change:1:INBOX", "change:2:All Mail"}
	if err != nil || !reflect.DeepEqual(calls, want) || len(results) != 2 || results[1].ReceiptPath != "/receipts/2" {
		t.Fatalf("calls=%v results=%+v error=%v", calls, results, err)
	}
}

func TestArchiveImportantFailedPreflightNeverChangesLabels(t *testing.T) {
	var calls []string
	apply := func(_ context.Context, r mail.GmailLabelRequest) (mail.GmailLabelResult, error) {
		if !r.DryRun {
			t.Fatal("label mutation after failed preflight")
		}
		calls = append(calls, r.MessageID)
		if r.MessageID == "2" {
			return mail.GmailLabelResult{Status: "failed"}, &mail.GmailLabelError{Kind: "unavailable", Hint: "connect the matching Gmail account", Cause: errors.New("profile mismatch")}
		}
		return mail.GmailLabelResult{Status: "dry_run"}, nil
	}
	results, err := prepareArchiveImportant(context.Background(), batchOptions{Action: "archive"}, importantArchiveItems(), true,
		func(string) (bool, error) { return true, nil }, func(string) (gmailLabelApply, error) { return apply, nil })
	failure := clierr.Classify(err)
	if len(calls) != 2 || len(results) != 2 || failure.Code != clierr.CodeUnavailable || failure.Hint != "connect the matching Gmail account" {
		t.Fatalf("calls=%v results=%+v error=%+v", calls, results, failure)
	}
}

func TestArchiveImportantRejectsNonGmailBeforeAnyProviderWork(t *testing.T) {
	items := append(importantArchiveItems(), batchItem{ID: "3", Account: "Ordinary IMAP"})
	_, err := prepareArchiveImportant(context.Background(), batchOptions{Action: "archive"}, items, true,
		func(account string) (bool, error) { return account == "Gmail", nil },
		func(string) (gmailLabelApply, error) {
			t.Fatal("provider accessed before all account checks")
			return nil, nil
		})
	if err == nil || !strings.Contains(err.Error(), "not Gmail") {
		t.Fatalf("error=%v", err)
	}
}

func TestArchiveImportantDryRunOnlyPreflights(t *testing.T) {
	calls := 0
	apply := func(_ context.Context, r mail.GmailLabelRequest) (mail.GmailLabelResult, error) {
		calls++
		if !r.DryRun {
			t.Fatal("dry-run mutated labels")
		}
		return mail.GmailLabelResult{Status: "dry_run"}, nil
	}
	results, err := prepareArchiveImportant(context.Background(), batchOptions{Action: "archive", DryRun: true}, importantArchiveItems(), true,
		func(string) (bool, error) { return true, nil }, func(string) (gmailLabelApply, error) { return apply, nil })
	if err != nil || calls != 2 || len(results) != 2 {
		t.Fatalf("calls=%d results=%+v error=%v", calls, results, err)
	}
}

func TestArchiveImportantStopsUncertainRemovalAndPreservesReceipts(t *testing.T) {
	for _, outcome := range []mail.GmailLabelResult{
		{Status: "unknown", MailStatus: "pending", ReceiptPath: "/receipts/uncertain"},
		{Status: "changed_verified", ProviderConfirmed: true, MailStatus: "pending", ReceiptPath: "/receipts/uncertain"},
	} {
		changes := 0
		apply := func(_ context.Context, r mail.GmailLabelRequest) (mail.GmailLabelResult, error) {
			if r.DryRun {
				return mail.GmailLabelResult{Status: "dry_run"}, nil
			}
			changes++
			return outcome, nil
		}
		results, err := prepareArchiveImportant(context.Background(), batchOptions{Action: "archive"}, importantArchiveItems(), true,
			func(string) (bool, error) { return true, nil }, func(string) (gmailLabelApply, error) { return apply, nil })
		failure := clierr.Classify(err)
		if changes != 1 || len(results) != 2 || results[0].ReceiptPath != outcome.ReceiptPath || results[1].Status != "dry_run" || failure.Code != clierr.CodeMutationFailed || !strings.Contains(failure.Hint, "labels reconcile") {
			t.Fatalf("changes=%d results=%+v error=%+v", changes, results, failure)
		}
	}
}

func TestArchiveImportantKeepsEarlierCompletedReceiptWhenLaterRemovalFails(t *testing.T) {
	apply := func(_ context.Context, r mail.GmailLabelRequest) (mail.GmailLabelResult, error) {
		if r.DryRun {
			return mail.GmailLabelResult{Status: "dry_run"}, nil
		}
		if r.MessageID == "1" {
			result := verifiedImportantResult(r)
			result.Status, result.ReceiptPath = "noop", "/receipts/first"
			return result, nil
		}
		return mail.GmailLabelResult{Status: "unknown", ReceiptPath: "/receipts/second"}, errors.New("provider connection lost")
	}
	results, err := prepareArchiveImportant(context.Background(), batchOptions{Action: "archive"}, importantArchiveItems(), true,
		func(string) (bool, error) { return true, nil }, func(string) (gmailLabelApply, error) { return apply, nil })
	if err == nil || len(results) != 2 || results[0].Status != "noop" || results[0].ReceiptPath != "/receipts/first" || results[1].ReceiptPath != "/receipts/second" {
		t.Fatalf("results=%+v error=%v", results, err)
	}
}

func TestArchiveImportantRefreshesRegeneratedMailIDFromVerifiedSnapshot(t *testing.T) {
	items := importantArchiveItems()[:1]
	items[0].SourceMailbox = "Important"
	apply := func(_ context.Context, r mail.GmailLabelRequest) (mail.GmailLabelResult, error) {
		if r.DryRun {
			return mail.GmailLabelResult{Status: "dry_run"}, nil
		}
		result := verifiedImportantResult(r)
		result.MailAfter.LocalIDs = []string{"101"}
		return result, nil
	}
	results, err := prepareArchiveImportant(context.Background(), batchOptions{Action: "archive"}, items, true,
		func(string) (bool, error) { return true, nil }, func(string) (gmailLabelApply, error) { return apply, nil })
	if err != nil || items[0].ID != "101" || items[0].SourceMailbox != "INBOX" || items[0].Identity.RFCMessageID != "message@example.com" || results[0].MailBefore.LocalIDs[0] != "1" {
		t.Fatalf("items=%+v results=%+v error=%v", items, results, err)
	}
}

func TestArchiveImportantRefreshRejectsUncertainMailIdentity(t *testing.T) {
	for _, ids := range [][]string{nil, {"1", "2"}, {"invalid"}} {
		item := importantArchiveItems()[0]
		result := verifiedImportantResult(mail.GmailLabelRequest{Account: item.Account, MessageID: item.ID})
		result.MailAfter.LocalIDs = ids
		if err := refreshArchiveImportantItem(&item, result); err == nil || item.ID != "1" {
			t.Fatalf("ids=%v item=%+v error=%v", ids, item, err)
		}
	}
}

func TestArchiveImportantMissingAuthorizationUsesExistingHint(t *testing.T) {
	_, err := prepareArchiveImportant(context.Background(), batchOptions{Action: "archive"}, importantArchiveItems(), true,
		func(string) (bool, error) { return true, nil },
		func(string) (gmailLabelApply, error) { return nil, gmail.ErrCredentialsNotFound })
	failure := clierr.Classify(err)
	if failure.Code != clierr.CodeUnavailable || failure.Hint == "" || !strings.Contains(failure.Hint, "labels auth") {
		t.Fatalf("error=%+v", failure)
	}
}
