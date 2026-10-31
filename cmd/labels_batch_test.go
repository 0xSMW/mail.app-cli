package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func labelBatchApp(t *testing.T, jq string) (*application, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	a := newApplication()
	var stdout, stderr bytes.Buffer
	var err error
	a.writer, err = output.New(output.FormatJSON, &stdout, &stderr, false, jq, "labels remove", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	return a, &stdout, &stderr
}

func labelBatchRequests(ids ...string) []mail.GmailLabelRequest {
	requests := make([]mail.GmailLabelRequest, 0, len(ids))
	for _, id := range ids {
		requests = append(requests, mail.GmailLabelRequest{Account: "Example Account", Mailbox: "INBOX", MessageID: id, Label: "IMPORTANT"})
	}
	return requests
}

type labelBatchEnvelope struct {
	OK       bool                  `json:"ok"`
	Data     gmailLabelBatchResult `json:"data"`
	Summary  string                `json:"summary"`
	Notices  []string              `json:"notices"`
	Code     string                `json:"code"`
	ExitCode int                   `json:"exitCode"`
	Hint     string                `json:"hint"`
	Error    string                `json:"error"`
}

func decodeLabelBatch(t *testing.T, stdout *bytes.Buffer) labelBatchEnvelope {
	t.Helper()
	var envelope labelBatchEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", stdout.String(), err)
	}
	return envelope
}

// fakeLabelApply returns the scripted result for each message ID and records
// the order of calls.
func fakeLabelApply(calls *[]string, results map[string]mail.GmailLabelResult, failures map[string]error) gmailLabelApply {
	return func(_ context.Context, r mail.GmailLabelRequest) (mail.GmailLabelResult, error) {
		*calls = append(*calls, r.MessageID)
		return results[r.MessageID], failures[r.MessageID]
	}
}

func TestLabelsRequestsFollowArgumentOrderAndRequireOneAccount(t *testing.T) {
	a := newApplication()
	refs := []messageRef{
		{ID: "12346", Account: "Example Account", Mailbox: "Archive"},
		{ID: "12345", Account: "Example Account", Mailbox: "INBOX"},
	}
	requests, err := a.gmailLabelRequestsFromRefs([]string{"12345", "12346"}, refs)
	if err != nil || len(requests) != 2 || requests[0].MessageID != "12345" || requests[0].Mailbox != "INBOX" || requests[1].MessageID != "12346" || requests[1].Mailbox != "Archive" {
		t.Fatalf("requests=%+v error=%v", requests, err)
	}

	refs = append(refs, messageRef{ID: "12347", Account: "Other Account", Mailbox: "INBOX"})
	requests, err = a.gmailLabelRequestsFromRefs([]string{"12345", "12346", "12347"}, refs)
	failure := clierr.Classify(err)
	if requests != nil || failure.Code != clierr.CodeUsage || !strings.Contains(failure.Message, `"Example Account"`) || !strings.Contains(failure.Message, `"Other Account"`) || !strings.Contains(failure.Hint, "once per account") {
		t.Fatalf("mixed accounts requests=%+v error=%+v", requests, failure)
	}

	requests, err = a.gmailLabelRequestsFromRefs([]string{"12345", "12348"}, refs)
	if requests != nil || clierr.Classify(err).Code != clierr.CodeNotFound {
		t.Fatalf("unresolved ID requests=%+v error=%v", requests, err)
	}
}

func TestLabelsRequestsApplyExplicitAccountToEveryMessage(t *testing.T) {
	a := newApplication()
	a.resolved.Account = config.Value{Value: "Example Account", Source: config.SourceFlag}
	refs := []messageRef{
		{ID: "12345", Account: "Example Account", Mailbox: "INBOX"},
		{ID: "12346", Account: "example account", Mailbox: "INBOX"},
	}
	requests, err := a.gmailLabelRequestsFromRefs([]string{"12345", "12346"}, refs)
	if requests != nil || clierr.Classify(err).Code != clierr.CodeNotFound || !strings.Contains(err.Error(), "message 12346") {
		t.Fatalf("requests=%+v error=%v", requests, err)
	}
}

func TestLabelsSingleIDKeepsSingleResultShape(t *testing.T) {
	a, stdout, _ := labelBatchApp(t, "")
	var calls []string
	apply := fakeLabelApply(&calls, map[string]mail.GmailLabelResult{
		"12345": {Status: "changed_verified", ProviderConfirmed: true, MailStatus: "verified", Account: "Example Account", ReceiptPath: "/private/receipt-1.jsonl"},
	}, nil)
	if err := a.runGmailLabelRequests(context.Background(), "remove", "IMPORTANT", false, labelBatchRequests("12345"), []string{"index notice"}, apply); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		OK      bool           `json:"ok"`
		Data    map[string]any `json:"data"`
		Summary string         `json:"summary"`
		Notices []string       `json:"notices"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || envelope.Data["status"] != "changed_verified" || envelope.Data["receiptPath"] != "/private/receipt-1.jsonl" || envelope.Data["items"] != nil {
		t.Fatalf("single-ID data changed shape: %s", stdout.String())
	}
	if envelope.Summary != "Gmail labels: changed_verified (provider confirmed: true; Mail.app: verified)" || len(envelope.Notices) != 1 || strings.Join(calls, ",") != "12345" {
		t.Fatalf("summary=%q notices=%v calls=%v", envelope.Summary, envelope.Notices, calls)
	}
}

func TestLabelsSeveralIDsAggregateSuccess(t *testing.T) {
	a, stdout, stderr := labelBatchApp(t, "")
	var calls []string
	apply := fakeLabelApply(&calls, map[string]mail.GmailLabelResult{
		"12345": {Status: "changed_verified", ProviderConfirmed: true, MailStatus: "verified", ReceiptPath: "/private/receipt-1.jsonl"},
		"12346": {Status: "noop", ProviderConfirmed: true, MailStatus: "pending", ReceiptPath: "/private/receipt-2.jsonl"},
		"12347": {Status: "changed_verified", ProviderConfirmed: true, MailStatus: "pending", ReceiptPath: "/private/receipt-3.jsonl"},
	}, nil)
	err := a.runGmailLabelRequests(context.Background(), "remove", "IMPORTANT", false, labelBatchRequests("12345", "12346", "12347"), []string{"index notice"}, apply)
	if err != nil {
		t.Fatal(err)
	}
	envelope := decodeLabelBatch(t, stdout)
	data := envelope.Data
	if !envelope.OK || data.Action != "remove" || data.Label != "IMPORTANT" || data.DryRun || data.Matched != 3 || data.Attempted != 3 || data.Succeeded != 3 || data.Failed != 0 || data.NotAttempted != 0 || len(data.Items) != 3 {
		t.Fatalf("data=%+v", data)
	}
	for i, id := range []string{"12345", "12346", "12347"} {
		item := data.Items[i]
		if item.MessageID != id || item.ProviderConfirmed == nil || !*item.ProviderConfirmed || item.Error == nil || *item.Error != "" || item.Result == nil || item.Result.ReceiptPath != item.ReceiptPath || item.ReceiptPath == "" {
			t.Fatalf("item %d = %+v", i, item)
		}
	}
	if strings.Join(calls, ",") != "12345,12346,12347" {
		t.Fatalf("calls out of argument order: %v", calls)
	}
	if envelope.Summary != "Gmail labels: 3 messages, 2 changed_verified, 1 noop" || len(envelope.Notices) != 1 || stderr.Len() != 0 {
		t.Fatalf("summary=%q notices=%v stderr=%q", envelope.Summary, envelope.Notices, stderr.String())
	}
}

func TestLabelsSeveralIDsStopAtFirstFailure(t *testing.T) {
	a, stdout, stderr := labelBatchApp(t, "")
	var calls []string
	preflight := &mail.GmailLabelError{Kind: "usage", Hint: "use a canonical label ID", Cause: errors.New("ambiguous label")}
	apply := fakeLabelApply(&calls, map[string]mail.GmailLabelResult{
		"12345": {Status: "changed_verified", ProviderConfirmed: true, MailStatus: "verified", ReceiptPath: "/private/receipt-1.jsonl"},
		"12346": {Status: "failed", MailStatus: "pending"},
	}, map[string]error{"12346": preflight})
	err := a.runGmailLabelRequests(context.Background(), "remove", "IMPORTANT", false, labelBatchRequests("12345", "12346", "12347", "12348"), nil, apply)
	failure := clierr.Classify(err)
	if failure.Code != clierr.CodeUsage || !failure.Reported || !errors.Is(err, preflight) || !strings.Contains(failure.Message, "message 12346") || failure.Hint != "use a canonical label ID" {
		t.Fatalf("error = %+v", failure)
	}
	if strings.Join(calls, ",") != "12345,12346" {
		t.Fatalf("later IDs were touched: %v", calls)
	}
	envelope := decodeLabelBatch(t, stdout)
	data := envelope.Data
	if envelope.OK || envelope.Code != "usage" || envelope.ExitCode != 1 || data.Matched != 4 || data.Attempted != 2 || data.Succeeded != 1 || data.Failed != 1 || data.NotAttempted != 2 {
		t.Fatalf("envelope=%+v", envelope)
	}
	failed := data.Items[1]
	if failed.Status != "failed" || failed.Error == nil || *failed.Error != "ambiguous label" || failed.Result == nil || failed.Result.Error != "ambiguous label" {
		t.Fatalf("failed item = %+v", failed)
	}
	for _, item := range data.Items[2:] {
		if item.Status != "not_attempted" || item.ProviderConfirmed != nil || item.Error != nil || item.Result != nil || item.ReceiptPath != "" {
			t.Fatalf("not attempted item = %+v", item)
		}
	}
	var raw struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if item := raw.Data.Items[2]; len(item) != 2 || item["messageId"] != "12347" || item["status"] != "not_attempted" {
		t.Fatalf("not attempted item carries extra fields: %v", item)
	}
	if envelope.Summary != "Gmail labels: 4 messages, 1 changed_verified, 1 failed, 2 not_attempted" || stderr.Len() != 0 {
		t.Fatalf("summary=%q stderr=%q", envelope.Summary, stderr.String())
	}
}

func TestLabelsSeveralIDsFailureWithReceiptIsMutationFailed(t *testing.T) {
	a, stdout, stderr := labelBatchApp(t, "")
	var calls []string
	apply := fakeLabelApply(&calls, map[string]mail.GmailLabelResult{
		"12345": {Status: "noop", ProviderConfirmed: true, MailStatus: "verified", ReceiptPath: "/private/receipt-1.jsonl"},
		"12346": {Status: "unknown", MailStatus: "pending", ReceiptPath: "/private/receipt-2.jsonl"},
	}, map[string]error{"12346": errors.New("Gmail readback unavailable; reconcile receipt without retrying mutation")})
	err := a.runGmailLabelRequests(context.Background(), "add", "Projects/Current", false, labelBatchRequests("12345", "12346", "12347"), nil, apply)
	failure := clierr.Classify(err)
	if failure.Code != clierr.CodeMutationFailed || !failure.Reported || !strings.Contains(failure.Hint, "labels reconcile") {
		t.Fatalf("error = %+v", failure)
	}
	envelope := decodeLabelBatch(t, stdout)
	if envelope.OK || envelope.Code != "mutation_failed" || envelope.ExitCode != 6 || !strings.Contains(envelope.Hint, "labels reconcile") || !strings.Contains(envelope.Error, "message 12346") {
		t.Fatalf("envelope=%+v", envelope)
	}
	data := envelope.Data
	if data.Action != "add" || data.Label != "Projects/Current" || data.Items[1].ReceiptPath != "/private/receipt-2.jsonl" || *data.Items[1].ProviderConfirmed || data.Items[2].Status != "not_attempted" || strings.Join(calls, ",") != "12345,12346" {
		t.Fatalf("data=%+v calls=%v", data, calls)
	}
	if envelope.Summary != "Gmail labels: 3 messages, 1 noop, 1 failed, 1 not_attempted" || stderr.Len() != 0 {
		t.Fatalf("summary=%q stderr=%q", envelope.Summary, stderr.String())
	}
}

func TestLabelsSeveralIDsUnexpectedStatusFailsClosed(t *testing.T) {
	a, stdout, _ := labelBatchApp(t, "")
	var calls []string
	apply := fakeLabelApply(&calls, map[string]mail.GmailLabelResult{
		"12345": {Status: "dispatched", ReceiptPath: "/private/receipt-1.jsonl"},
	}, nil)
	err := a.runGmailLabelRequests(context.Background(), "remove", "IMPORTANT", false, labelBatchRequests("12345", "12346"), nil, apply)
	if clierr.Classify(err).Code != clierr.CodeMutationFailed || strings.Join(calls, ",") != "12345" {
		t.Fatalf("error=%v calls=%v", err, calls)
	}
	if data := decodeLabelBatch(t, stdout).Data; data.Failed != 1 || data.NotAttempted != 1 || data.Succeeded != 0 {
		t.Fatalf("data=%+v", data)
	}
}

func TestLabelsShowSeveralIDsWithJQ(t *testing.T) {
	a, stdout, stderr := labelBatchApp(t, `.data.items | map(.messageId + "=" + .status) | join(",")`)
	var calls []string
	apply := fakeLabelApply(&calls, map[string]mail.GmailLabelResult{
		"12345": {Status: "observed", ProviderConfirmed: true, MailStatus: "observed"},
		"12346": {Status: "observed", ProviderConfirmed: true, MailStatus: "observed"},
	}, nil)
	if err := a.runGmailLabelRequests(context.Background(), "show", "", false, labelBatchRequests("12345", "12346"), nil, apply); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(stdout.String()); got != "12345=observed,12346=observed" || stderr.Len() != 0 {
		t.Fatalf("jq output=%q stderr=%q", got, stderr.String())
	}
	batch, err := runGmailLabelBatch(context.Background(), "show", "ignored", false, labelBatchRequests("12345", "12346"), apply)
	if err != nil || batch.Label != "" || batch.Action != "show" || batch.Succeeded != 2 {
		t.Fatalf("batch=%+v error=%v", batch, err)
	}
}
