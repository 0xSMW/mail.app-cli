package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func TestLabelsValidateArgumentsBeforeMailOrCredentials(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"local ID required", []string{"labels", "show", "rfc@example.com"}, "not numeric"},
		{"ID required", []string{"labels", "show"}, "requires at least 1 arg"},
		{"later local ID required", []string{"labels", "remove", "1", "rfc@example.com", "--label", "IMPORTANT"}, "not numeric"},
		{"duplicate ID", []string{"labels", "show", "12345", "12346", "12345"}, "12345 is given more than once"},
		{"duplicate ID after trimming", []string{"labels", "add", "12345", " 12345", "--label", "IMPORTANT"}, "12345 is given more than once"},
		{"label required", []string{"labels", "remove", "1"}, "--label"},
		{"blank label", []string{"labels", "add", "1", "--label", " "}, "--label"},
		{"list arguments", []string{"labels", "list", "unexpected"}, "unknown command"},
		{"import path required", []string{"labels", "auth", "import"}, "--credentials"},
		{"connect path required", []string{"labels", "auth", "connect"}, "--client-secret"},
		{"receipt required", []string{"labels", "reconcile"}, "accepts 1 arg"},
		{"legacy verify unsupported", []string{"labels", "remove", "1", "--label", "IMPORTANT", "--verify=false"}, "unknown flag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := run(t, append(tt.args, "--json")...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, tt.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want usage containing %q", code, stdout, stderr, tt.want)
			}
		})
	}
}

func TestLabelsProtectMutationReceiptsFromJQ(t *testing.T) {
	for _, args := range [][]string{
		{"labels", "add", "1", "--label", "IMPORTANT"},
		{"labels", "remove", "1", "--label", "Projects/Current", "--dry-run"},
		{"labels", "add", "1", "2", "--label", "IMPORTANT"},
		{"labels", "remove", "1", "2", "--label", "IMPORTANT", "--dry-run"},
		{"labels", "auth", "import", "--credentials", "/private/example.json"},
		{"labels", "auth", "connect", "--client-secret", "/private/example.json"},
	} {
		t.Run(strings.Join(args[:3], " "), func(t *testing.T) {
			code, stdout, stderr := run(t, append(args, "--jq", ".")...)
			if code != 1 || stdout != "" || !strings.Contains(stderr, "--jq cannot be combined") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
		})
	}
}

func TestLabelsCommandStateIsInvocationOwned(t *testing.T) {
	a, b := newApplication(), newApplication()
	one, _, err := a.rootCmd.Find([]string{"labels", "add"})
	if err != nil {
		t.Fatal(err)
	}
	two, _, err := b.rootCmd.Find([]string{"labels", "add"})
	if err != nil {
		t.Fatal(err)
	}
	if err := one.Flags().Set("label", "Projects/Current"); err != nil {
		t.Fatal(err)
	}
	if err := one.Flags().Set("dry-run", "true"); err != nil {
		t.Fatal(err)
	}
	if err := one.Args(one, []string{"12"}); err != nil {
		t.Fatal(err)
	}
	if err := two.Args(two, []string{"12"}); err == nil {
		t.Fatal("label flag leaked to another invocation")
	}
	value, _ := two.Flags().GetBool("dry-run")
	if value {
		t.Fatal("dry-run leaked to another invocation")
	}
	list, _, _ := a.rootCmd.Find([]string{"labels", "list"})
	if list.Annotations[annotationIDList] != "true" {
		t.Fatal("canonical label IDs not exposed as an ID list")
	}
	reconcile, _, _ := a.rootCmd.Find([]string{"labels", "reconcile"})
	if reconcile.Annotations[annotationMutation] == "true" {
		t.Fatal("reconciliation must stay read-only")
	}
}

func TestLabelsFailedReadbackRetainsReceiptInErrorEnvelope(t *testing.T) {
	a := newApplication()
	var stdout, stderr bytes.Buffer
	var err error
	a.writer, err = output.New(output.FormatJSON, &stdout, &stderr, false, "", "labels remove", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	result := mail.GmailLabelResult{Status: "unknown", MailStatus: "pending", Account: "Work", ReceiptPath: "/private/receipt.jsonl", ProviderMessageID: "provider-123"}
	err = a.writeGmailLabelResult(result, []string{"Mail.app has not converged"}, errors.New("fresh Gmail readback unavailable"))
	failure := clierr.Classify(err)
	if failure.Code != clierr.CodeMutationFailed || !failure.Reported {
		t.Fatalf("error = %+v", failure)
	}
	var envelope struct {
		OK       bool                  `json:"ok"`
		Data     mail.GmailLabelResult `json:"data"`
		Code     string                `json:"code"`
		ExitCode int                   `json:"exitCode"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Code != "mutation_failed" || envelope.ExitCode != 6 || envelope.Data.ReceiptPath != result.ReceiptPath || envelope.Data.ProviderConfirmed {
		t.Fatalf("error envelope lost uncertainty or receipt: %s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("receipt failure printed a second error: %s", stderr.String())
	}
}

func TestLabelsSummaryKeepsProviderConfirmationSeparateFromMail(t *testing.T) {
	a := newApplication()
	var stdout, stderr bytes.Buffer
	a.writer, _ = output.New(output.FormatJSON, &stdout, &stderr, false, "", "labels remove", mail.SchemaVersion)
	err := a.writeGmailLabelResult(mail.GmailLabelResult{Status: "changed_verified", ProviderConfirmed: true, MailStatus: "pending"}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "provider confirmed: true; Mail.app: pending") {
		t.Fatalf("summary = %s", stdout.String())
	}
}

func TestLabelsPreflightErrorsHaveActionableExitCodes(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		code clierr.Code
		hint string
	}{
		{"ambiguous label", &mail.GmailLabelError{Kind: "usage", Hint: "use a canonical label ID", Cause: errors.New("ambiguous label")}, clierr.CodeUsage, "canonical label ID"},
		{"missing message", &mail.NotFoundError{Kind: "Gmail message", Name: "selected identity"}, clierr.CodeNotFound, ""},
		{"system label", &mail.GmailLabelError{Kind: "unavailable", Hint: "use flag or unflag", Cause: errors.New("STARRED is unavailable")}, clierr.CodeUnavailable, "flag or unflag"},
		{"profile mismatch", &mail.GmailLabelError{Kind: "unavailable", Hint: "connect the correct account", Cause: errors.New("profile mismatch")}, clierr.CodeUnavailable, "correct account"},
		{"provider denied", &gmail.RequestError{Operation: "profile", StatusCode: 403}, clierr.CodeUnavailable, "connect the correct account"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := gmailLabelCommandError(fmt.Errorf("label preflight: %w", tt.err))
			if got.Code != tt.code || !strings.Contains(got.Hint, tt.hint) {
				t.Fatalf("error = %+v", got)
			}
		})
	}
}

func TestLabelsReconcileRejectsDifferentExplicitAccountBeforeProviderAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "receipt.jsonl")
	receipt := mail.GmailLabelResult{
		Account: "Work", ProviderEmail: "work@example.com", ProviderMessageID: "provider-123",
		Identity: mail.StableIdentity{RFCMessageID: "<message@example.com>", Sender: "sender@example.com", Subject: "Subject", DateSent: "2026-10-15T00:00:00Z", MessageSize: 123},
		Label:    gmail.Label{ID: "IMPORTANT", Name: "IMPORTANT", Type: "system"}, BeforeLabelIDs: []string{"IMPORTANT"},
		MailBefore: &mail.GmailLabelSnapshot{LocalIDs: []string{"123"}, Memberships: []string{"Important"}},
	}
	data, err := json.Marshal(map[string]any{"event": "gmail_label_dispatch", "result": receipt})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "labels", "reconcile", path, "--account", "Other", "--json")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "must match the Gmail label receipt account") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unchanged, append(data, '\n')) {
		t.Fatal("read-only reconciliation changed its receipt")
	}
}

func TestLabelsRejectLocatedAccountWithDifferentCaseFromExplicitSelection(t *testing.T) {
	a := newApplication()
	a.resolved.Account = config.Value{Value: "Work", Source: config.SourceFlag}
	for _, account := range []string{"work", "WORK", "Other"} {
		t.Run(account, func(t *testing.T) {
			request, err := a.gmailLabelRequestFromRef(messageRef{ID: "123", Account: account, Mailbox: "INBOX"})
			if err == nil || request.Account != "" || clierr.Classify(err).Code != clierr.CodeNotFound {
				t.Fatalf("request=%+v error=%v", request, err)
			}
		})
	}
	request, err := a.gmailLabelRequestFromRef(messageRef{ID: "123", Account: "Work", Mailbox: "INBOX"})
	if err != nil || request.Account != "Work" || request.MessageID != "123" {
		t.Fatalf("exact account request=%+v error=%v", request, err)
	}
}

func TestLabelsSelectCredentialAcrossAliasFirstAccount(t *testing.T) {
	emails := []string{"alias@example.com", "primary@example.com"}
	var lookedUp []string
	selected, err := selectGmailLabelEmail(emails, "", func(email string) error {
		lookedUp = append(lookedUp, email)
		if email == "primary@example.com" {
			return nil
		}
		return fmt.Errorf("lookup absent: %w", gmail.ErrCredentialsNotFound)
	})
	if err != nil || selected != "primary@example.com" || strings.Join(lookedUp, ",") != strings.Join(emails, ",") {
		t.Fatalf("selected=%q lookups=%v error=%v", selected, lookedUp, err)
	}
	selected, err = selectGmailLabelEmail(emails, "PRIMARY@example.com", func(string) error { t.Fatal("explicit selector probed credentials before validation"); return nil })
	if err != nil || selected != "primary@example.com" {
		t.Fatalf("explicit selected=%q error=%v", selected, err)
	}
	selected, err = selectGmailLabelEmail(emails, "", nil)
	if err != nil || selected != emails[0] {
		t.Fatalf("authorization default selected=%q error=%v", selected, err)
	}
	selected, err = selectGmailLabelEmail(emails, "primary@example.com", nil)
	if err != nil || selected != "primary@example.com" {
		t.Fatalf("authorization selector selected=%q error=%v", selected, err)
	}
}

func TestLabelsRejectInvalidOrUnconfiguredProviderEmailBeforeCredentials(t *testing.T) {
	for _, explicit := range []string{"invalid", "other@example.com", "Person <primary@example.com>", " "} {
		t.Run(explicit, func(t *testing.T) {
			selected, err := selectGmailLabelEmail([]string{"alias@example.com", "primary@example.com"}, explicit, func(string) error { t.Fatal("invalid selector touched credentials"); return nil })
			if selected != "" || err == nil || clierr.Classify(err).Code != clierr.CodeUsage {
				t.Fatalf("selected=%q error=%v", selected, err)
			}
		})
	}
}

func TestLabelsCredentialSelectionFailsClosedOnAmbiguityOrKeychainFailure(t *testing.T) {
	emails := []string{"alias@example.com", "primary@example.com"}
	selected, err := selectGmailLabelEmail(emails, "", func(string) error { return nil })
	if selected != "" || err == nil || clierr.Classify(err).Code != clierr.CodeUsage || !strings.Contains(err.Error(), "--email") {
		t.Fatalf("ambiguous selected=%q error=%v", selected, err)
	}
	denied := errors.New("macOS Keychain access denied")
	selected, err = selectGmailLabelEmail(emails, "", func(email string) error {
		if email == emails[0] {
			return nil
		}
		return denied
	})
	if selected != "" || !errors.Is(err, denied) || clierr.Classify(err).Code != clierr.CodeUnavailable {
		t.Fatalf("Keychain failure selected=%q error=%v", selected, err)
	}
	selected, err = selectGmailLabelEmail(emails, "", func(string) error { return gmail.ErrCredentialsNotFound })
	if selected != "" || !errors.Is(err, gmail.ErrCredentialsNotFound) || clierr.Classify(err).Code != clierr.CodeUnavailable || !strings.Contains(clierr.Classify(err).Hint, "labels auth connect") {
		t.Fatalf("missing credentials selected=%q error=%v", selected, err)
	}
}

func TestLabelsProviderSelectionLoadsEachCredentialOnce(t *testing.T) {
	emails := []string{"alias@example.com", "primary@example.com"}
	for _, explicit := range []string{"", "PRIMARY@example.com"} {
		t.Run(explicit, func(t *testing.T) {
			calls := map[string]int{}
			want := gmail.NewClient(nil, nil)
			provider, err := selectGmailLabelProvider(emails, explicit, func(email string) (*gmail.Client, error) {
				calls[email]++
				if email == emails[0] {
					return nil, gmail.ErrCredentialsNotFound
				}
				return want, nil
			})
			if err != nil || provider != want || calls[emails[1]] != 1 {
				t.Fatalf("provider=%p want=%p calls=%v error=%v", provider, want, calls, err)
			}
			wantAliasCalls := 1
			if explicit != "" {
				wantAliasCalls = 0
			}
			if calls[emails[0]] != wantAliasCalls {
				t.Fatalf("credential calls=%v", calls)
			}
		})
	}
}

func TestLabelsProviderSelectionDoesNotChooseAmbiguousOrUnreadableCredentials(t *testing.T) {
	emails := []string{"alias@example.com", "primary@example.com"}
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			calls := map[string]int{}
			denial := errors.New("Keychain interaction unavailable")
			provider, err := selectGmailLabelProvider(emails, "", func(email string) (*gmail.Client, error) {
				calls[email]++
				if denied && email == emails[1] {
					return nil, denial
				}
				return gmail.NewClient(nil, nil), nil
			})
			wantCode := clierr.CodeUsage
			if denied {
				wantCode = clierr.CodeUnavailable
			}
			if provider != nil || err == nil || clierr.Classify(err).Code != wantCode || calls[emails[0]] != 1 || calls[emails[1]] != 1 {
				t.Fatalf("provider=%p calls=%v error=%v", provider, calls, err)
			}
			if denied && !errors.Is(err, denial) {
				t.Fatalf("Keychain error lost: %v", err)
			}
		})
	}
}
