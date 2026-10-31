package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// gmailLabelApply runs one label request. It is GmailLabelWorkflow.Show or
// GmailLabelWorkflow.Change in production and a fake in tests.
type gmailLabelApply func(context.Context, mail.GmailLabelRequest) (mail.GmailLabelResult, error)

// gmailLabelBatchResult is the data shape for show, add, and remove with more
// than one message ID. One ID keeps the single GmailLabelResult shape.
type gmailLabelBatchResult struct {
	Action       string                `json:"action"`
	Label        string                `json:"label,omitempty"`
	DryRun       bool                  `json:"dryRun"`
	Matched      int                   `json:"matched"`
	Attempted    int                   `json:"attempted"`
	Succeeded    int                   `json:"succeeded"`
	Failed       int                   `json:"failed"`
	NotAttempted int                   `json:"notAttempted"`
	Items        []gmailLabelBatchItem `json:"items"`
}

// gmailLabelBatchItem reports one message. Items that were not attempted carry
// only messageId and status.
type gmailLabelBatchItem struct {
	MessageID         string                 `json:"messageId"`
	Status            string                 `json:"status"`
	ProviderConfirmed *bool                  `json:"providerConfirmed,omitempty"`
	MailStatus        string                 `json:"mailStatus,omitempty"`
	ReceiptPath       string                 `json:"receiptPath,omitempty"`
	Error             *string                `json:"error,omitempty"`
	Result            *mail.GmailLabelResult `json:"result,omitempty"`
}

const gmailLabelNotAttempted = "not_attempted"

// gmailLabelItemSucceeded counts an item as succeeded only when it has no
// error. A verified change whose final receipt write failed keeps its status
// but is a failure.
func gmailLabelItemSucceeded(item gmailLabelBatchItem) bool {
	return (item.Error == nil || *item.Error == "") && gmailLabelStatusSucceeded(item.Status)
}

func gmailLabelStatusSucceeded(status string) bool {
	switch status {
	case "changed_verified", "noop", "dry_run", "observed":
		return true
	}
	return false
}

// gmailLabelApplyFor builds the workflow once for the account and returns its
// Show or Change method.
func (a *application) gmailLabelApplyFor(cmd *cobra.Command, account string, show bool) (gmailLabelApply, error) {
	if a.gmailLabelApply != nil {
		return a.gmailLabelApply(cmd, account, show)
	}
	workflow, err := a.gmailLabelsWorkflow(cmd, account)
	if err != nil {
		return nil, err
	}
	if show {
		return workflow.Show, nil
	}
	return workflow.Change, nil
}

// gmailLabelRequests resolves every ID before any provider work. Resolution
// fails closed when an ID is missing, and all messages must be in one Mail.app
// account so a single provider binding applies to the whole run.
func (a *application) gmailLabelRequests(ids []string) ([]mail.GmailLabelRequest, []string, error) {
	ids = trimAll(ids)
	// The label workflow requires indexed identity even with explicit scope.
	// Do not use locateMessages' guessed refs for an unavailable index or an
	// explicit mailbox: a later missing ID must stop the whole preflight.
	located, err := a.locateIndexed(ids)
	if err != nil {
		return nil, nil, clierr.Wrap(clierr.CodeUnavailable, err, "cannot resolve Gmail label messages: "+err.Error()).
			WithHint("Gmail labels require Envelope Index access; run 'mail-app-cli doctor' and restore index access before retrying")
	}
	refs, notices, err := a.resolveLocatedMessages(ids, located, nil)
	if err != nil {
		return nil, nil, err
	}
	if a.mailboxExplicit() {
		account, err := a.requireAccount()
		if err != nil {
			return nil, nil, err
		}
		for i := range refs {
			if refs[i].Account != account {
				return nil, nil, clierr.New(clierr.CodeNotFound, fmt.Sprintf("message %s is in account %q, not the selected account %q", refs[i].ID, refs[i].Account, account))
			}
			refs[i].Mailbox = a.mailboxInScope()
		}
	}
	requests, err := a.gmailLabelRequestsFromRefs(ids, refs)
	if err != nil {
		return nil, nil, err
	}
	return requests, notices, nil
}

// gmailLabelRequestsFromRefs orders resolved refs by argument order, applies
// the explicit-account check to each, and requires a single account.
func (a *application) gmailLabelRequestsFromRefs(ids []string, refs []messageRef) ([]mail.GmailLabelRequest, error) {
	byID := make(map[string]messageRef, len(refs))
	for _, ref := range refs {
		byID[ref.ID] = ref
	}
	requests := make([]mail.GmailLabelRequest, 0, len(ids))
	for _, id := range ids {
		ref, ok := byID[id]
		if !ok {
			return nil, clierr.New(clierr.CodeNotFound, fmt.Sprintf("message %s was not resolved to a Mail.app account and mailbox", id))
		}
		request, err := a.gmailLabelRequestFromRef(ref)
		if err != nil {
			return nil, err
		}
		if len(requests) > 0 && request.Account != requests[0].Account {
			return nil, clierr.Usagef("message %s is in account %q and message %s is in account %q; labels commands accept messages from one Mail.app account", requests[0].MessageID, requests[0].Account, id, request.Account).
				WithHint("run the command once per account")
		}
		requests = append(requests, request)
	}
	return requests, nil
}

// runGmailLabelBatch applies requests in order and stops at the first failure.
// Later requests are reported as not_attempted and never reach apply. The
// returned error is the first failure, or nil when every request succeeded.
func runGmailLabelBatch(ctx context.Context, action, label string, dryRun bool, requests []mail.GmailLabelRequest, apply gmailLabelApply) (gmailLabelBatchResult, error) {
	batch := gmailLabelBatchResult{Action: action, DryRun: dryRun, Matched: len(requests), Items: make([]gmailLabelBatchItem, 0, len(requests))}
	if action != "show" {
		batch.Label = label
	}
	var failure error
	for _, request := range requests {
		if failure != nil {
			batch.Items = append(batch.Items, gmailLabelBatchItem{MessageID: request.MessageID, Status: gmailLabelNotAttempted})
			batch.NotAttempted++
			continue
		}
		result, err := apply(ctx, request)
		batch.Attempted++
		if err == nil && result.Error != "" {
			err = errors.New(result.Error)
		}
		if err == nil && !gmailLabelStatusSucceeded(result.Status) {
			err = fmt.Errorf("Gmail label request ended with status %q", result.Status)
		}
		if err != nil {
			// Status and receiptPath stay as returned: a verified change whose
			// receipt write failed still applied, and its receipt stays visible.
			result.Error = err.Error()
			failure = err
		}
		confirmed, message := result.ProviderConfirmed, result.Error
		item := gmailLabelBatchItem{
			MessageID: request.MessageID, Status: result.Status, ProviderConfirmed: &confirmed,
			MailStatus: result.MailStatus, ReceiptPath: result.ReceiptPath, Error: &message, Result: &result,
		}
		if gmailLabelItemSucceeded(item) {
			batch.Succeeded++
		} else {
			batch.Failed++
		}
		batch.Items = append(batch.Items, item)
	}
	return batch, failure
}

// runGmailLabelRequests keeps the single-result shape for one ID and writes
// the aggregated shape for several.
func (a *application) runGmailLabelRequests(ctx context.Context, action, label string, dryRun bool, requests []mail.GmailLabelRequest, notices []string, apply gmailLabelApply) error {
	if len(requests) == 1 {
		result, err := apply(ctx, requests[0])
		return a.writeGmailLabelResult(result, notices, err)
	}
	batch, err := runGmailLabelBatch(ctx, action, label, dryRun, requests, apply)
	return a.writeGmailLabelBatch(batch, notices, err)
}

// writeGmailLabelBatch always writes the batch data. A failure keeps the
// classified error of the failed message, upgraded to mutation_failed when
// that message has a receipt, as writeGmailLabelResult does for one ID.
func (a *application) writeGmailLabelBatch(batch gmailLabelBatchResult, notices []string, err error) error {
	var failure *clierr.Error
	if err != nil && batch.Attempted > 0 {
		// The run stops at the first failure, so it is the last attempted item.
		failed := batch.Items[batch.Attempted-1]
		named := fmt.Errorf("message %s: %w", failed.MessageID, err)
		failure = gmailLabelCommandError(named)
		if failed.ReceiptPath != "" {
			failure = clierr.Wrap(clierr.CodeMutationFailed, named, named.Error()).WithHint("observe the receipt with 'mail-app-cli labels reconcile <receipt-file>'; inspect the result before retrying")
		}
	} else if err != nil {
		failure = gmailLabelCommandError(err)
	}
	return a.writer.Write(output.Result{Data: batch, Notices: notices, Err: failure, Summary: gmailLabelBatchSummary(batch)})
}

func gmailLabelBatchSummary(batch gmailLabelBatchResult) string {
	var order []string
	counts := map[string]int{}
	for _, item := range batch.Items {
		if !gmailLabelItemSucceeded(item) {
			continue
		}
		if counts[item.Status] == 0 {
			order = append(order, item.Status)
		}
		counts[item.Status]++
	}
	parts := []string{plural(batch.Matched, "message")}
	for _, status := range order {
		parts = append(parts, fmt.Sprintf("%d %s", counts[status], status))
	}
	if batch.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", batch.Failed))
	}
	if batch.NotAttempted > 0 {
		parts = append(parts, fmt.Sprintf("%d not_attempted", batch.NotAttempted))
	}
	return "Gmail labels: " + strings.Join(parts, ", ")
}
