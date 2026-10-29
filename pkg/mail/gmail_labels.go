package mail

import (
	"context"
	"fmt"
	"net/mail"
	"sort"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
	"golang.org/x/text/unicode/norm"
)

// GmailLabelRequest changes one explicit provider label. Mailbox and MessageID
// identify the Mail message only; they are never interpreted as Gmail IDs.
type GmailLabelRequest struct {
	Account   string        `json:"account"`
	Mailbox   string        `json:"mailbox"`
	MessageID string        `json:"messageId"`
	Label     string        `json:"label"`
	Add       bool          `json:"add"`
	DryRun    bool          `json:"dryRun"`
	Receipt   *BatchJournal `json:"-"`
}

type GmailLabelSnapshot struct {
	LocalIDs    []string `json:"localIds"`
	Memberships []string `json:"memberships"`
	Read        bool     `json:"read"`
}

type GmailLabelError struct {
	Kind  string
	Hint  string
	Cause error
}

func (e *GmailLabelError) Error() string {
	if e.Cause == nil {
		return e.Hint
	}
	return e.Cause.Error()
}
func (e *GmailLabelError) Unwrap() error { return e.Cause }
func gmailLabelError(kind, hint string, err error) error {
	return &GmailLabelError{Kind: kind, Hint: hint, Cause: err}
}

// Provider confirmation and Mail observation are independent. MailStatus never
// asserts that a row disappeared from an open Mail viewer.
type GmailLabelResult struct {
	Status            string              `json:"status"`
	ProviderConfirmed bool                `json:"providerConfirmed"`
	MailStatus        string              `json:"mailStatus"`
	Account           string              `json:"account"`
	ProviderEmail     string              `json:"providerEmail"`
	ProviderMessageID string              `json:"providerMessageId"`
	Identity          StableIdentity      `json:"identity"`
	Label             gmail.Label         `json:"label"`
	Labels            []gmail.Label       `json:"labels,omitempty"`
	Add               bool                `json:"add"`
	BeforeLabelIDs    []string            `json:"beforeLabelIds"`
	AfterLabelIDs     []string            `json:"afterLabelIds"`
	ProviderAllMail   bool                `json:"providerAllMail"`
	MailBefore        *GmailLabelSnapshot `json:"mailBefore,omitempty"`
	MailAfter         *GmailLabelSnapshot `json:"mailAfter,omitempty"`
	ReceiptPath       string              `json:"receiptPath,omitempty"`
	Error             string              `json:"error,omitempty"`
}

type GmailLabelWorkflow struct {
	Mail     *Client
	Provider gmail.API
	account  func(context.Context, string) (*Account, error)
	message  func(context.Context, GmailLabelRequest) (*Message, error)
	snapshot func(context.Context, string, StableIdentity) (GmailLabelSnapshot, error)
}

func NewGmailLabelWorkflow(c *Client, provider gmail.API) *GmailLabelWorkflow {
	w := &GmailLabelWorkflow{Mail: c, Provider: provider}
	w.account = func(ctx context.Context, name string) (*Account, error) {
		return c.WithContext(ctx).accountByName(name)
	}
	w.message = func(ctx context.Context, r GmailLabelRequest) (*Message, error) {
		client := c.WithContext(ctx)
		return client.gmailLabelMessageWithRead(r, client.GetMessageDetailsForVerificationJSON)
	}
	w.snapshot = func(ctx context.Context, account string, id StableIdentity) (GmailLabelSnapshot, error) {
		return c.WithContext(ctx).gmailLabelSnapshot(account, id)
	}
	return w
}

func GmailAccountEmail(ctx context.Context, c *Client, name string) (string, error) {
	addresses, err := GmailAccountEmails(ctx, c, name)
	if err != nil {
		return "", err
	}
	return addresses[0], nil
}

// GmailAccountEmails returns actual Mail account addresses in primary/alias
// order, with normalized mailbox addresses and case-insensitive duplicates removed.
func GmailAccountEmails(ctx context.Context, c *Client, name string) ([]string, error) {
	a, err := c.WithContext(ctx).accountByName(name)
	if err != nil {
		return nil, err
	}
	addresses := gmailAccountAddresses(a)
	if len(addresses) == 0 {
		return nil, gmailLabelError("unavailable", "Configure an actual email address on the Mail account", fmt.Errorf("Mail account %q has no actual email address", name))
	}
	return addresses, nil
}

func gmailAccountAddresses(a *Account) []string {
	addresses := []string{}
	seen := map[string]bool{}
	for _, value := range append([]string{a.EmailAddress}, a.EmailAddresses...) {
		email := gmailEmail(value)
		if email != "" && !seen[email] {
			seen[email] = true
			addresses = append(addresses, email)
		}
	}
	return addresses
}

func gmailEmail(value string) string {
	a, err := mail.ParseAddress(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return strings.ToLower(a.Address)
}

// Mail's public message-id property may omit RFC header angle brackets. Only
// this explicitly sourced property (or an actual Message-ID header) may use
// this normalization; numeric Mail IDs and metadata fallbacks never qualify.
func gmailRFCMessageID(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "<") && strings.HasSuffix(value, ">") {
		value = value[1 : len(value)-1]
	}
	if strings.Count(value, "@") != 1 || strings.ContainsAny(value, " \t\r\n<>\"\\") {
		return ""
	}
	for _, r := range value {
		if r < 33 || r > 126 {
			return ""
		}
	}
	address, err := mail.ParseAddress(value)
	if err != nil || address.Name != "" || address.Address != value {
		return ""
	}
	return "<" + value + ">"
}

func gmailLabelIdentity(message Message) StableIdentity {
	identity := StableIdentityFromMessage(message)
	identity.RFCMessageID = gmailRFCMessageID(message.RFCMessageID)
	return identity
}

func gmailLabelMetadataMatches(a, b StableIdentity) bool {
	return gmailEmail(a.Sender) != "" && gmailEmail(a.Sender) == gmailEmail(b.Sender) && a.Subject == b.Subject && a.DateSent == b.DateSent && a.MessageSize == b.MessageSize
}

func (w *GmailLabelWorkflow) bind(ctx context.Context, account string) (string, error) {
	if w.Provider == nil {
		return "", fmt.Errorf("Gmail provider is required")
	}
	a, err := w.account(ctx, account)
	if err != nil {
		return "", err
	}
	if !a.Enabled {
		return "", gmailLabelError("unavailable", "Enable the selected Mail account", fmt.Errorf("Mail account %q is disabled", account))
	}
	if !strings.EqualFold(a.AccountType, "imap") {
		return "", gmailLabelError("unavailable", "Select the Gmail IMAP account in Mail", fmt.Errorf("Mail account %q must be an IMAP account for Gmail labels", account))
	}
	p, err := w.Provider.Profile(ctx)
	if err != nil {
		return "", gmailLabelError("unavailable", "Connect or import Gmail credentials for this Mail account", err)
	}
	email := gmailEmail(p.EmailAddress)
	if email == "" {
		return "", gmailLabelError("unavailable", "Connect Gmail credentials with a valid profile email", fmt.Errorf("Gmail profile has no valid email address"))
	}
	for _, address := range gmailAccountAddresses(a) {
		if address == email {
			return email, nil
		}
	}
	return "", gmailLabelError("unavailable", "Connect Gmail credentials whose profile matches an actual address of this Mail account", fmt.Errorf("Gmail profile email does not match an actual address of Mail account %q", account))
}

func (w *GmailLabelWorkflow) List(ctx context.Context, account string) ([]gmail.Label, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if _, err := w.bind(ctx, account); err != nil {
		return nil, err
	}
	return w.Provider.ListLabels(ctx)
}

func gmailHeader(m gmail.Message, name string) (string, error) {
	var values []string
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			values = append(values, strings.TrimSpace(h.Value))
		}
	}
	if len(values) != 1 {
		return "", fmt.Errorf("Gmail identity requires exactly one %s header", name)
	}
	return values[0], nil
}

func verifyGmailIdentity(m gmail.Message, id StableIdentity) error {
	rfc, err := gmailHeader(m, "Message-ID")
	if err != nil {
		return err
	}
	if gmailRFCMessageID(rfc) == "" || gmailRFCMessageID(rfc) != id.RFCMessageID {
		return fmt.Errorf("Gmail RFC Message-ID differs from Mail identity")
	}
	from, err := gmailHeader(m, "From")
	if err != nil {
		return err
	}
	if gmailEmail(from) == "" || gmailEmail(from) != gmailEmail(id.Sender) {
		return fmt.Errorf("Gmail From differs from Mail identity")
	}
	subject, err := gmailHeader(m, "Subject")
	if err != nil {
		return err
	}
	subject, err = headerWordDecoder().DecodeHeader(subject)
	if err != nil {
		return fmt.Errorf("Gmail Subject encoding is invalid")
	}
	if subject != id.Subject {
		return fmt.Errorf("Gmail Subject differs from Mail identity")
	}
	date, err := gmailHeader(m, "Date")
	if err != nil {
		return err
	}
	parsed, err := mail.ParseDate(date)
	if err != nil {
		return fmt.Errorf("Gmail Date is invalid")
	}
	if parsed.UTC().Format(time.RFC3339) != id.DateSent {
		return fmt.Errorf("Gmail Date differs from Mail identity")
	}
	return nil
}

func labelSet(ids []string) []string {
	result := append([]string{}, ids...)
	sort.Strings(result)
	out := result[:0]
	for _, id := range result {
		if len(out) == 0 || out[len(out)-1] != id {
			out = append(out, id)
		}
	}
	return out
}
func hasLabel(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func sameLabels(a, b []string) bool {
	return strings.Join(labelSet(a), "\x00") == strings.Join(labelSet(b), "\x00")
}
func desiredLabels(before []string, label string, add bool) []string {
	out := []string{}
	for _, id := range before {
		if id != label {
			out = append(out, id)
		}
	}
	if add {
		out = append(out, label)
	}
	return labelSet(out)
}

func resolveGmailLabel(labels []gmail.Label, value string) (gmail.Label, error) {
	if strings.EqualFold(value, "All Mail") {
		return gmail.Label{}, gmailLabelError("unavailable", "Use archive for Inbox removal", fmt.Errorf("Gmail All Mail is a provider projection, not a removable label; use archive for Inbox removal"))
	}
	var matches []gmail.Label
	for _, label := range labels {
		if label.ID == value || label.Name == value || (strings.EqualFold(value, "important") && label.ID == "IMPORTANT") {
			matches = append(matches, label)
		}
	}
	if len(matches) != 1 {
		return gmail.Label{}, gmailLabelError("usage", "Use the exact existing Gmail label ID or full label name", fmt.Errorf("Gmail label %q must identify exactly one existing label", value))
	}
	l := matches[0]
	if l.ID == "IMPORTANT" && strings.EqualFold(l.Type, "system") {
		return l, nil
	}
	if strings.EqualFold(l.Type, "user") {
		return l, nil
	}
	guidance := map[string]string{"INBOX": "use archive to remove Inbox membership or move to INBOX to restore it", "UNREAD": "use mark read or mark unread", "STARRED": "use flag or unflag", "TRASH": "use delete or move", "SPAM": "use the provider's spam controls", "SENT": "Sent membership is provider managed", "DRAFT": "use draft commands", "DRAFTS": "use draft commands", "ALL": "use archive for Inbox removal", "ALL MAIL": "use archive for Inbox removal"}
	hint := guidance[strings.ToUpper(l.ID)]
	if hint == "" {
		hint = "only IMPORTANT and existing user labels support explicit label changes"
	}
	return gmail.Label{}, gmailLabelError("unavailable", hint, fmt.Errorf("Gmail label %q does not support explicit add/remove: %s", l.Name, hint))
}

func (w *GmailLabelWorkflow) prepare(ctx context.Context, r GmailLabelRequest, change bool) (GmailLabelResult, error) {
	out := GmailLabelResult{Status: "failed", MailStatus: "pending", Account: r.Account, Add: r.Add}
	email, err := w.bind(ctx, r.Account)
	if err != nil {
		return out, err
	}
	out.ProviderEmail = email
	labels, err := w.Provider.ListLabels(ctx)
	if err != nil {
		return out, err
	}
	out.Labels = labels
	if change {
		out.Label, err = resolveGmailLabel(labels, r.Label)
		if err != nil {
			return out, err
		}
	}
	m, err := w.message(ctx, r)
	if err != nil {
		return out, err
	}
	if m == nil {
		return out, notFound("Mail message", r.MessageID)
	}
	id := gmailLabelIdentity(*m)
	out.Identity = id
	if id.RFCMessageID == "" || !strings.Contains(id.RFCMessageID, "@") {
		return out, gmailLabelError("usage", "Select a Mail message with a true RFC Message-ID header", fmt.Errorf("Gmail resolution requires Mail's true RFC Message-ID; numeric local IDs and metadata fallback are insufficient"))
	}
	if !id.hasFallback() || gmailEmail(id.Sender) == "" {
		return out, gmailLabelError("usage", "Select a Mail message with complete indexed identity metadata", fmt.Errorf("Mail identity requires sender, subject, sent date and local size for independent verification"))
	}
	before, err := w.snapshot(ctx, r.Account, id)
	if err != nil {
		return out, err
	}
	out.MailBefore = &before
	ids, err := w.Provider.FindMessages(ctx, id.RFCMessageID)
	if err != nil {
		return out, err
	}
	if len(ids) != 1 {
		if len(ids) == 0 {
			return out, notFound("Gmail message for RFC Message-ID", id.RFCMessageID)
		}
		return out, gmailLabelError("usage", "Choose a Mail message with a unique RFC Message-ID", fmt.Errorf("RFC Message-ID must resolve to exactly one Gmail message; found %d", len(ids)))
	}
	provider, err := w.Provider.GetMessage(ctx, ids[0])
	if err != nil {
		return out, err
	}
	if provider.ID != ids[0] {
		return out, gmailLabelError("unavailable", "Repeat the read-only lookup after checking provider availability", fmt.Errorf("Gmail returned a different message ID"))
	}
	if err := verifyGmailIdentity(provider, id); err != nil {
		return out, gmailLabelError("unavailable", "Choose a message whose independent Mail and Gmail headers agree", err)
	}
	out.ProviderMessageID = provider.ID
	out.BeforeLabelIDs = labelSet(provider.LabelIDs)
	out.AfterLabelIDs = labelSet(provider.LabelIDs)
	out.ProviderAllMail = !hasLabel(provider.LabelIDs, "TRASH") && !hasLabel(provider.LabelIDs, "SPAM")
	return out, nil
}

func gmailResultError(out GmailLabelResult, err error) (GmailLabelResult, error) {
	if err != nil {
		out.Error = err.Error()
	}
	return out, err
}

func (w *GmailLabelWorkflow) Show(ctx context.Context, r GmailLabelRequest) (GmailLabelResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := w.prepare(ctx, r, false)
	if err != nil {
		return gmailResultError(out, err)
	}
	out.Status = "observed"
	out.ProviderConfirmed = true
	out.MailStatus = "observed"
	out.MailAfter = out.MailBefore
	return out, nil
}

func (w *GmailLabelWorkflow) Change(ctx context.Context, r GmailLabelRequest) (GmailLabelResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := w.prepare(ctx, r, true)
	if err != nil {
		return gmailResultError(out, err)
	}
	if r.DryRun {
		out.Status = "dry_run"
		out.MailStatus = "observed"
		return out, nil
	}
	journal := r.Receipt
	if journal == nil {
		journal, err = CreateDefaultBatchJournal()
		if err != nil {
			return gmailResultError(out, err)
		}
		defer journal.Close()
	}
	out.ReceiptPath = journal.Path()
	if hasLabel(out.BeforeLabelIDs, out.Label.ID) == r.Add {
		out.Status = "noop"
		out.ProviderConfirmed = true
		out.MailStatus = "pending"
		out.MailAfter = out.MailBefore
		if gmailMailLabelNamesUnambiguous(out.Labels) && gmailMailStateMatches(*out.MailBefore, *out.MailBefore, out.Label, out.Add) {
			out.MailStatus = "verified"
		}
		if err := journal.Record("gmail_label_noop", map[string]any{"result": out}); err != nil {
			return gmailResultError(out, err)
		}
		return out, nil
	}
	out.Status = "dispatch_pending"
	if err := journal.Record("gmail_label_dispatch", map[string]any{"result": out}); err != nil {
		out.Status = "failed"
		return gmailResultError(out, err)
	}
	var add, remove []string
	if r.Add {
		add = []string{out.Label.ID}
	} else {
		remove = []string{out.Label.ID}
	}
	dispatchErr := w.Provider.ModifyLabels(ctx, out.ProviderMessageID, add, remove)
	out.Status = "dispatched"
	// A response is acceptance only. Always make a fresh independent read,
	// including after transport errors, and never retry the mutation.
	out, err = w.observe(ctx, out)
	if dispatchErr != nil && out.Status != "changed_verified" {
		out.Status = "unknown"
		err = fmt.Errorf("Gmail dispatch outcome is uncertain: %w", dispatchErr)
	}
	out.Error = ""
	if err != nil {
		out.Error = err.Error()
	}
	if recordErr := journal.Record("gmail_label_result", map[string]any{"result": out}); recordErr != nil {
		if err == nil {
			err = recordErr
		}
		out.Error = err.Error()
	}
	return out, err
}

func (w *GmailLabelWorkflow) observe(ctx context.Context, out GmailLabelResult) (GmailLabelResult, error) {
	wasNoop := out.Status == "noop"
	out.ProviderConfirmed = false
	out.MailStatus = "pending"
	out.MailAfter = nil
	// Read Mail independently after dispatch even when provider readback fails.
	if after, snapshotErr := w.snapshot(ctx, out.Account, out.Identity); snapshotErr == nil {
		out.MailAfter = &after
		if out.MailBefore != nil && gmailMailLabelNamesUnambiguous(out.Labels) && gmailMailStateMatches(*out.MailBefore, after, out.Label, out.Add) {
			out.MailStatus = "verified"
		}
	}
	provider, err := w.Provider.GetMessage(ctx, out.ProviderMessageID)
	if err != nil {
		out.Status = "unknown"
		return out, fmt.Errorf("Gmail readback unavailable; reconcile receipt without retrying mutation: %w", err)
	}
	if provider.ID != out.ProviderMessageID {
		out.Status = "unknown"
		return out, fmt.Errorf("Gmail readback returned a different message ID")
	}
	if err := verifyGmailIdentity(provider, out.Identity); err != nil {
		out.Status = "unknown"
		return out, err
	}
	out.AfterLabelIDs = labelSet(provider.LabelIDs)
	out.ProviderAllMail = !hasLabel(provider.LabelIDs, "TRASH") && !hasLabel(provider.LabelIDs, "SPAM")
	if !sameLabels(out.AfterLabelIDs, desiredLabels(out.BeforeLabelIDs, out.Label.ID, out.Add)) {
		out.Status = "failed"
		return out, fmt.Errorf("fresh Gmail state does not match the exact requested label delta; requested label or unrelated labels differ")
	}
	out.Status = "changed_verified"
	if wasNoop {
		out.Status = "noop"
	}
	out.ProviderConfirmed = true
	return out, nil
}

// Mail exposes membership names, not provider IDs. Distinct provider labels
// sharing a canonical name cannot be independently verified from those names.
func gmailMailLabelNamesUnambiguous(labels []gmail.Label) bool {
	ids := make(map[string]string, len(labels))
	for _, label := range labels {
		name := norm.NFC.String(label.Name)
		if id, exists := ids[name]; exists && id != label.ID {
			return false
		}
		ids[name] = label.ID
	}
	return true
}

func gmailMailStateMatches(before, after GmailLabelSnapshot, label gmail.Label, add bool) bool {
	// These values are mailbox names, unlike the literal provider IDs used
	// by sameLabels elsewhere. Compare names canonically without rewriting
	// snapshot evidence or weakening case and compatibility distinctions.
	canonicalNames := func(names []string) ([]string, bool) {
		out := make([]string, len(names))
		spellings := make(map[string]string, len(names))
		for i, name := range names {
			out[i] = norm.NFC.String(name)
			if previous, exists := spellings[out[i]]; exists && previous != name {
				// Distinct physical memberships must not disappear in set deduplication.
				return nil, false
			}
			spellings[out[i]] = name
		}
		return out, true
	}
	beforeNames, beforeUnique := canonicalNames(before.Memberships)
	afterNames, afterUnique := canonicalNames(after.Memberships)
	if !beforeUnique || !afterUnique {
		return false
	}
	name := norm.NFC.String(label.Name)
	if label.ID == "IMPORTANT" {
		name = "Important"
	}
	return before.Read == after.Read && sameLabels(afterNames, desiredLabels(beforeNames, name, add))
}
