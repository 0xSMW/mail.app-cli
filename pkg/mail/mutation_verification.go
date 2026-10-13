package mail

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StableIdentity identifies a logical message across Mail.app local-ID
// changes. RFCMessageID is used only when it is an actual RFC header; the
// fallback is deliberately all-or-nothing to avoid matching a nearby mail.
type StableIdentity struct {
	RFCMessageID string `json:"rfcMessageId,omitempty"`
	Sender       string `json:"sender,omitempty"`
	Subject      string `json:"subject,omitempty"`
	DateSent     string `json:"dateSent,omitempty"`
	MessageSize  int    `json:"messageSize,omitempty"`
}

func StableIdentityFromMessage(message Message) StableIdentity {
	return StableIdentity{
		RFCMessageID: validRFCMessageID(message.RFCMessageID),
		Sender:       strings.TrimSpace(message.Sender),
		Subject:      strings.TrimSpace(message.Subject),
		DateSent:     canonicalIdentityDate(message.DateSent),
		MessageSize:  message.MessageSize,
	}
}

func canonicalIdentityDate(value string) string {
	value = strings.TrimSpace(value)
	if parsed, ok := ParseMessageTime(value); ok {
		return parsed.UTC().Format(time.RFC3339)
	}
	return value
}

func validRFCMessageID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '<' || value[len(value)-1] != '>' || strings.ContainsAny(value[1:len(value)-1], " \t\r\n<>") {
		return ""
	}
	return value
}

func (identity StableIdentity) hasFallback() bool {
	return identity.Sender != "" && identity.Subject != "" && identity.DateSent != "" && identity.MessageSize > 0
}

func (identity StableIdentity) valid() bool {
	return identity.RFCMessageID != "" || identity.hasFallback()
}

func (identity StableIdentity) fallbackKey() string {
	return identity.Sender + "\x00" + identity.Subject + "\x00" + identity.DateSent + "\x00" + strconv.Itoa(identity.MessageSize)
}

func (c *Client) captureStableIdentity(item BatchItem) (StableIdentity, error) {
	identity := item.Identity
	identity.Sender = strings.TrimSpace(identity.Sender)
	identity.Subject = strings.TrimSpace(identity.Subject)
	identity.DateSent = canonicalIdentityDate(identity.DateSent)
	identity.RFCMessageID = validRFCMessageID(identity.RFCMessageID)
	if !identity.valid() {
		identity = StableIdentity{Sender: strings.TrimSpace(item.Sender), Subject: strings.TrimSpace(item.Subject), DateSent: canonicalIdentityDate(item.DateSent), MessageSize: item.MessageSize}
	}
	// Mail's header property is the only source accepted as an RFC Message-ID.
	// The Envelope Index's numeric message_id is a local database value.
	message, err := c.GetMessageDetailsForVerificationJSON(item.Account, item.SourceMailbox, item.ID)
	return completeStableIdentity(identity, message, err)
}

// completeStableIdentity treats the live RFC read as enrichment. A complete
// index-captured fallback is already safe and must not be discarded merely
// because Mail.app did not answer the optional header read in time.
func completeStableIdentity(identity StableIdentity, message *Message, enrichmentErr error) (StableIdentity, error) {
	if enrichmentErr != nil {
		if identity.valid() {
			return identity, nil
		}
		return StableIdentity{}, enrichmentErr
	}
	if message != nil {
		fromMail := StableIdentityFromMessage(*message)
		if fromMail.RFCMessageID != "" {
			identity.RFCMessageID = fromMail.RFCMessageID
		}
		if !identity.hasFallback() {
			identity.Sender, identity.Subject = fromMail.Sender, fromMail.Subject
			identity.DateSent, identity.MessageSize = fromMail.DateSent, fromMail.MessageSize
		}
	}
	if !identity.valid() {
		return StableIdentity{}, fmt.Errorf("message has neither an RFC Message-ID nor a complete sender+subject+sent-date+size identity")
	}
	return identity, nil
}

// verificationBackoff is deliberately small but bounded: Mail and IMAP can
// accept a move before the destination row is visible in the Envelope Index.
var verificationBackoff = []time.Duration{0, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}

type verificationPresence struct {
	Source      bool
	Destination bool
}

type verificationLookup func() (verificationPresence, error)
type verificationPause func(context.Context, time.Duration) error

func verifyRelocationWithLookup(ctx context.Context, item BatchItem, lookup verificationLookup, pause verificationPause) (string, error) {
	for attempt, delay := range verificationBackoff {
		if attempt > 0 {
			if err := pause(ctx, delay); err != nil {
				return "unknown_after_timeout", err
			}
		}
		if err := ctx.Err(); err != nil {
			return "unknown_after_timeout", err
		}
		presence, err := lookup()
		if err != nil {
			if isVerificationTimeout(err) {
				return "unknown_after_timeout", err
			}
			if isRetryableVerificationError(err) && attempt < len(verificationBackoff)-1 {
				continue
			}
			return "applied_destination_unverified", err
		}
		// Always use the final observation: a local source can reappear after
		// an initially successful destination observation.
		if attempt == len(verificationBackoff)-1 {
			if presence.Source {
				return "present_in_source", fmt.Errorf("message still present in %s", item.SourceMailbox)
			}
			if presence.Destination {
				return "confirmed_destination", nil
			}
			return "applied_destination_unverified", fmt.Errorf("message left %s but was not found in %s", item.SourceMailbox, item.TargetMailbox)
		}
	}
	return "applied_destination_unverified", fmt.Errorf("message destination could not be verified")
}

func isRetryableVerificationError(err error) bool {
	if err == nil {
		return false
	}
	if IsNotFound(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "apple event connection is invalid") ||
		strings.Contains(message, "connection invalid") ||
		strings.Contains(message, "connection interrupted") ||
		strings.Contains(message, "(-609)")
}

func isVerificationTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isAutomationTimeout(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "timed out") || strings.Contains(message, "timeout") || strings.Contains(message, "(-1712)")
}

func waitForVerificationBackoff(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isAutomationTimeout(err error) bool {
	var timeout *AutomationTimeoutError
	return errors.As(err, &timeout)
}

func isValidGmailInboxTransition(item BatchItem) bool {
	return item.GmailInboxSource && strings.EqualFold(item.SourceMailbox, "INBOX") && IsArchiveAlias(item.TargetMailbox)
}

const identityEnrichmentTimeout = 5 * time.Second
const DefaultVerifyBudget = 30 * time.Second

// ValidateBatchOptions runs before source resolution or any mailbox changes.
func ValidateBatchOptions(opts BatchOptions) error {
	if opts.MarkReadOnSkip && (!opts.MarkReadBefore || opts.Action != "archive") {
		return fmt.Errorf("--mark-read-on-skip requires --mark-read and archive")
	}
	if opts.Settle < 0 || opts.Settle > 5*time.Minute {
		return fmt.Errorf("--settle must be between 0 and 5m")
	}
	if opts.VerifyBudget < 0 || opts.VerifyBudget > 10*time.Minute {
		return fmt.Errorf("--verify-budget must be between 0 and 10m")
	}
	if opts.Settle >= effectiveVerifyBudget(opts) {
		return fmt.Errorf("--verify-budget must exceed --settle")
	}
	return nil
}

func effectiveVerifyBudget(opts BatchOptions) time.Duration {
	if opts.VerifyBudget > 0 {
		return opts.VerifyBudget
	}
	return DefaultVerifyBudget
}

// DefaultGmailArchiveSettle observes local memberships for ten seconds after
// Mail accepts an explicit sync request. It is not a server acknowledgement.
const DefaultGmailArchiveSettle = 10 * time.Second

func relocationSettle(opts BatchOptions, items []BatchItem) time.Duration {
	if opts.Settle != 0 || opts.readOnlyVerification || opts.Action != "archive" {
		return opts.Settle
	}
	for _, item := range items {
		if item.GmailInboxSource && strings.EqualFold(item.SourceMailbox, "INBOX") && item.Status != "failed" {
			return DefaultGmailArchiveSettle
		}
	}
	return opts.Settle
}

// verificationSchedule preserves the existing six observations by default.
// Longer windows continue observing every two seconds, including identities
// whose destination was already confirmed in an earlier observation.
func verificationSchedule(settle time.Duration) []time.Duration {
	schedule := append([]time.Duration(nil), verificationBackoff...)
	elapsed := time.Duration(0)
	for _, delay := range schedule {
		elapsed += delay
	}
	for elapsed < settle {
		delay := min(2*time.Second, settle-elapsed)
		schedule = append(schedule, delay)
		elapsed += delay
	}
	return schedule
}

// canRetainRelocationSource proves the provider membership exception from the
// actual system mailbox URL. An item flag or a generic All Mail name alone
// never permits retaining a source. Spam and INBOX must always be removed.
func (c *Client) canRetainRelocationSource(action string, item BatchItem) bool {
	if action != "move" || !strings.EqualFold(item.TargetMailbox, "INBOX") || !IsArchiveAlias(item.SourceMailbox) {
		return false
	}
	mbox, ok, err := c.resolveIndexMailbox(item.Account, item.SourceMailbox)
	if err != nil || !ok {
		return false
	}
	url := strings.ToLower(mbox.URL)
	return strings.Contains(url, "/%5bgmail%5d/all%20mail") || strings.Contains(url, "/[gmail]/all%20mail")
}

func captureBatchIdentities(client *Client, opts BatchOptions, items []BatchItem) error {
	ctx, cancel := context.WithTimeout(client.Context(), effectiveVerifyBudget(opts))
	defer cancel()
	reader := client.WithContext(ctx)
	var session *jxaSession
	defer func() {
		if session != nil {
			session.Close()
		}
	}()
	for i := range items {
		item := &items[i]
		if item.Status == "failed" || skipReason(client, opts, *item) != "" {
			continue
		}
		if err := opts.Receipt.Record("identity_capture_started", journalItem(*item)); err != nil {
			return err
		}
		started := time.Now()
		identity := item.Identity
		identity.RFCMessageID = validRFCMessageID(identity.RFCMessageID)
		identity.Sender, identity.Subject, identity.DateSent = strings.TrimSpace(identity.Sender), strings.TrimSpace(identity.Subject), canonicalIdentityDate(identity.DateSent)
		if !identity.valid() {
			identity = StableIdentityFromMessage(Message{Sender: item.Sender, Subject: item.Subject, DateSent: item.DateSent, MessageSize: item.MessageSize})
		}
		var message *Message
		err := ctx.Err()
		if err == nil {
			// Close after ten requests or an interrupted exchange. The next ID
			// receives a fresh serial session; the failed read is never retried.
			if session != nil && (i%10 == 0 || session.closed) {
				session.Close()
				session = nil
				reader.session = nil
			}
			if opts.ReuseBridge && len(items) > 1 && session == nil {
				session, err = newJXASession(ctx)
				reader.session = session
			}
			if err == nil {
				message, err = reader.GetMessageDetailsForVerificationJSON(item.Account, item.SourceMailbox, item.ID)
			}
		}
		if err != nil {
			item.IdentityEnrichmentError = err.Error()
			if session != nil {
				session.Close()
				session = nil
				reader.session = nil
			}
		}
		identity, captureErr := completeStableIdentity(identity, message, err)
		item.IdentityCaptureMillis = time.Since(started).Milliseconds()
		if captureErr != nil {
			item.Status, item.Error = "failed", "capture stable identity: "+captureErr.Error()
		} else {
			item.Identity = identity
		}
		if err := opts.Receipt.Record("identity_capture_result", journalItem(*item)); err != nil {
			return err
		}
	}
	return nil
}

// ReconcileMutations observes a saved receipt again without replaying any
// mutation. Unknown relocations can be reconciled when a pre-mutation stable
// identity survived; their original application status remains unchanged.
func ReconcileMutations(client *Client, opts BatchOptions, receipt BatchResult) (BatchResult, error) {
	if receipt.Action != "archive" && receipt.Action != "move" {
		return receipt, fmt.Errorf("reconciliation requires an archive or move receipt")
	}
	if receipt.DryRun {
		return receipt, fmt.Errorf("cannot reconcile a dry-run receipt")
	}
	if opts.Action == "" {
		opts.Action = receipt.Action
	}
	if opts.Action != receipt.Action {
		return receipt, fmt.Errorf("reconciliation action %q does not match receipt action %q", opts.Action, receipt.Action)
	}
	eligible := 0
	for _, item := range receipt.Items {
		if item.Status == "skipped" && item.MarkReadOnSkip && item.MarkedRead {
			if strings.TrimSpace(item.Account) == "" || strings.TrimSpace(item.SourceMailbox) == "" || strings.TrimSpace(item.ID) == "" {
				return receipt, fmt.Errorf("skipped read item lacks account, mailbox or ID")
			}
			eligible++
		}
		if item.Status != "succeeded" && item.Status != "unknown" {
			continue
		}
		if strings.TrimSpace(item.Account) == "" || strings.TrimSpace(item.SourceMailbox) == "" || strings.TrimSpace(item.TargetMailbox) == "" || !item.Identity.valid() {
			return receipt, fmt.Errorf("receipt item %q lacks account, source, destination, or pre-mutation stable identity", item.ID)
		}
		eligible++
	}
	if eligible == 0 {
		return receipt, fmt.Errorf("receipt contains no applied or uncertain identities to reconcile")
	}
	opts.readOnlyVerification = true
	opts.MarkReadBefore = opts.MarkReadBefore || receipt.MarkReadRequested
	if err := ValidateBatchOptions(opts); err != nil {
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(client.Context(), effectiveVerifyBudget(opts))
	defer cancel()
	client = client.WithContext(ctx)
	items := append([]BatchItem(nil), receipt.Items...)
	for i := range items {
		if items[i].Status == "unknown" && (opts.Action == "move" || opts.Action == "archive") && items[i].Identity.valid() {
			items[i].Status = "succeeded"
		}
		items[i].VerifyStatus, items[i].VerifyError, items[i].Observed = "", "", nil
	}
	verified := VerifyMutations(client, opts, items)
	var skippedMarks []BatchItem
	var skippedIndices []int
	for i, item := range items {
		if item.Status == "skipped" && item.MarkReadOnSkip && item.MarkedRead {
			item.Status = "succeeded"
			skippedMarks = append(skippedMarks, item)
			skippedIndices = append(skippedIndices, i)
		}
	}
	if len(skippedMarks) > 0 {
		checks := VerifyMutations(client, BatchOptions{Action: "mark", Read: true, VerifyBudget: opts.VerifyBudget, readOnlyVerification: true}, skippedMarks)
		for i, check := range checks {
			verified[skippedIndices[i]].VerifyStatus, verified[skippedIndices[i]].VerifyError = check.VerifyStatus, check.VerifyError
		}
	}
	receipt.Unverified = 0
	for i := range verified {
		if receipt.Items[i].Status == "unknown" && verified[i].VerifyStatus == "" {
			verified[i].VerifyStatus, verified[i].VerifyError = "applied_destination_unverified", "unknown mutation has no stable identity for reconciliation"
		}
		verified[i].Status = receipt.Items[i].Status
		if verified[i].VerifyError != "" {
			receipt.Unverified++
		}
	}
	receipt.Items = verified
	receipt.EndedAt = time.Now().Format(time.RFC3339Nano)
	if receipt.Unverified > 0 {
		return receipt, &BatchFailedError{Action: opts.Action, Unverified: receipt.Unverified, Attempted: receipt.Attempted}
	}
	return receipt, nil
}
