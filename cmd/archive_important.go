package cmd

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// prepareArchiveImportant completes the separately requested provider action
// before archive can relocate any Mail IDs. Returned results must accompany
// the archive receipt even when preparation fails: an earlier label removal
// may already have completed and has its own durable reconciliation receipt.
func (a *application) prepareArchiveImportant(cmd *cobra.Command, opts batchOptions, items []batchItem, enabled bool) ([]mail.GmailLabelResult, error) {
	return prepareArchiveImportant(cmd.Context(), opts, items, enabled,
		a.mailClient.WithContext(cmd.Context()).IsGmailArchiveAccount,
		func(account string) (gmailLabelApply, error) { return a.gmailLabelApplyFor(cmd, account, false) })
}

func prepareArchiveImportant(ctx context.Context, opts batchOptions, items []batchItem, enabled bool,
	isGmail func(string) (bool, error), applyFor func(string) (gmailLabelApply, error),
) ([]mail.GmailLabelResult, error) {
	if !enabled {
		return nil, nil
	}
	if opts.Action != "archive" {
		return nil, clierr.Usage("--remove-important is available only for archive")
	}
	// Check every account before constructing a workflow or changing labels.
	// In particular, never apply a Gmail label request to ordinary IMAP mail.
	accounts := make(map[string]bool)
	for _, item := range items {
		if accounts[item.Account] {
			continue
		}
		gmail, err := isGmail(item.Account)
		if err != nil {
			return nil, gmailLabelCommandError(err)
		}
		if !gmail {
			return nil, clierr.Usagef("--remove-important requires a Gmail account; account %q is not Gmail", item.Account)
		}
		accounts[item.Account] = true
	}
	workflows := make(map[string]gmailLabelApply)
	requests := make([]mail.GmailLabelRequest, 0, len(items))
	results := make([]mail.GmailLabelResult, 0, len(items))
	for _, item := range items {
		apply := workflows[item.Account]
		if apply == nil {
			var err error
			apply, err = applyFor(item.Account)
			if err != nil {
				return results, gmailLabelCommandError(err)
			}
			workflows[item.Account] = apply
		}
		request := mail.GmailLabelRequest{Account: item.Account, Mailbox: item.SourceMailbox, MessageID: item.ID, Label: "IMPORTANT", DryRun: true}
		result, err := apply(ctx, request)
		results = append(results, result)
		if err == nil && (result.Error != "" || result.Status != "dry_run") {
			err = fmt.Errorf("Gmail IMPORTANT preflight for message %s ended with status %q: %s", item.ID, result.Status, result.Error)
		}
		if err != nil {
			return results, gmailLabelCommandError(err)
		}
		requests = append(requests, request)
	}
	if opts.DryRun {
		return results, nil
	}
	for i, request := range requests {
		request.DryRun = false
		result, err := workflows[request.Account](ctx, request)
		results[i] = result
		if err == nil && (result.Error != "" || !result.ProviderConfirmed || result.MailStatus != "verified" || (result.Status != "changed_verified" && result.Status != "noop")) {
			err = fmt.Errorf("Gmail IMPORTANT removal for message %s is not confirmed in both Gmail and Mail (status: %s; provider confirmed: %t; Mail: %s): %s", request.MessageID, result.Status, result.ProviderConfirmed, result.MailStatus, result.Error)
		}
		if err == nil {
			err = refreshArchiveImportantItem(&items[i], result)
		}
		if err != nil {
			failure := gmailLabelCommandError(err)
			if result.ReceiptPath != "" {
				failure = clierr.Wrap(clierr.CodeMutationFailed, err, err.Error()).WithHint("observe the label receipt with 'mail-app-cli labels reconcile <receipt-file>'; inspect the result before retrying archive")
			}
			return results, failure
		}
	}
	return results, nil
}

// The label workflow independently verifies the exact RFC and envelope identity
// when it captures MailAfter. Reuse that current local ID, since removing a
// label can regenerate the ID originally selected from Important. MailBefore
// in the label receipt retains the original local-ID evidence.
func refreshArchiveImportantItem(item *batchItem, result mail.GmailLabelResult) error {
	if result.Account != item.Account || result.MailAfter == nil || len(result.MailAfter.LocalIDs) != 1 ||
		result.Identity.RFCMessageID == "" || result.Identity.Sender == "" || result.Identity.Subject == "" || result.Identity.DateSent == "" || result.Identity.MessageSize <= 0 {
		return fmt.Errorf("Gmail IMPORTANT removal has no unique verified Mail identity for message %s; archive stopped", item.ID)
	}
	id := result.MailAfter.LocalIDs[0]
	if n, err := strconv.ParseUint(id, 10, 53); err != nil || n == 0 {
		return fmt.Errorf("Gmail IMPORTANT removal returned an invalid current Mail ID for message %s; archive stopped", item.ID)
	}
	source := ""
	for _, membership := range result.MailAfter.Memberships {
		if strings.EqualFold(membership, "INBOX") {
			source = "INBOX"
			break
		}
		if membership == "All Mail" || membership == "[Gmail]/All Mail" {
			source = "All Mail"
		}
	}
	if source == "" {
		return fmt.Errorf("Gmail IMPORTANT removal has no verified INBOX or All Mail membership for message %s; archive stopped", item.ID)
	}
	item.ID, item.SourceMailbox, item.Identity = id, source, result.Identity
	return nil
}
