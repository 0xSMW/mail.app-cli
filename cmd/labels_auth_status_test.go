package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

type gmailAuthFakes struct {
	emails    []string
	emailsErr error
	probes    map[string]gmail.AddressStatus
	probed    []string
}

func (f *gmailAuthFakes) install(a *application) {
	a.gmailAccountEmails = func(_ context.Context, account string) ([]string, error) {
		if account != "Example Account" {
			return nil, fmt.Errorf("unexpected account %q", account)
		}
		return f.emails, f.emailsErr
	}
	a.gmailProbe = func(_ context.Context, email string) gmail.AddressStatus {
		f.probed = append(f.probed, email)
		if status, ok := f.probes[email]; ok {
			return status
		}
		return gmail.AddressStatus{Email: email, Status: gmail.AuthCredentialsMissing, Detail: gmail.ErrCredentialsNotFound.Error(), Hint: gmail.AuthCredentialsMissing.Hint()}
	}
}

func runGmailAuth(t *testing.T, fakes *gmailAuthFakes, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
	t.Setenv(config.EnvAccount, "")
	t.Setenv(config.EnvMailbox, "")
	t.Setenv(config.EnvOutput, "")
	a := newApplication()
	fakes.install(a)
	var stdout, stderr bytes.Buffer
	code := a.run(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

type gmailAuthEnvelope struct {
	OK       bool            `json:"ok"`
	Data     gmailAuthReport `json:"data"`
	Summary  string          `json:"summary"`
	Code     string          `json:"code"`
	ExitCode int             `json:"exitCode"`
	Hint     string          `json:"hint"`
}

func decodeGmailAuth(t *testing.T, stdout string) gmailAuthEnvelope {
	t.Helper()
	var envelope gmailAuthEnvelope
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout)
	}
	return envelope
}

func connectedProbe(email, provider string) gmail.AddressStatus {
	return gmail.AddressStatus{Email: email, Status: gmail.AuthConnected, ProviderEmail: provider}
}

func failedProbe(email string, status gmail.AuthStatus, detail string) gmail.AddressStatus {
	return gmail.AddressStatus{Email: email, Status: status, Detail: detail, Hint: status.Hint()}
}

var authStatusArgs = []string{"labels", "auth", "status", "-a", "Example Account", "--json"}

func TestLabelsAuthStatusReportsEachAddress(t *testing.T) {
	fakes := &gmailAuthFakes{
		emails: []string{"primary@example.com", "alias@example.com"},
		probes: map[string]gmail.AddressStatus{"primary@example.com": connectedProbe("primary@example.com", "primary@example.com")},
	}
	code, stdout, stderr := runGmailAuth(t, fakes, authStatusArgs...)
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q stdout=%s", code, stderr, stdout)
	}
	envelope := decodeGmailAuth(t, stdout)
	report := envelope.Data
	if !envelope.OK || envelope.Summary != "Gmail auth: connected (primary@example.com)" || report.Account != "Example Account" || report.Status != gmail.AuthConnected || report.ProviderEmail != "primary@example.com" || report.Hint != "" {
		t.Fatalf("envelope = %+v", envelope)
	}
	if len(report.Addresses) != 2 || report.Addresses[0].Status != gmail.AuthConnected || report.Addresses[1].Email != "alias@example.com" || report.Addresses[1].Status != gmail.AuthCredentialsMissing || report.Addresses[1].Hint == "" {
		t.Fatalf("addresses = %+v", report.Addresses)
	}
	if strings.Join(fakes.probed, ",") != "primary@example.com,alias@example.com" {
		t.Fatalf("probed = %v", fakes.probed)
	}
	var raw struct {
		Data struct {
			Addresses []map[string]any `json:"addresses"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw.Data.Addresses[0]["detail"]; ok {
		t.Fatalf("connected address carried detail: %s", stdout)
	}
}

func TestLabelsAuthStatusFailuresKeepData(t *testing.T) {
	for _, tt := range []struct {
		name     string
		emails   []string
		probes   map[string]gmail.AddressStatus
		status   gmail.AuthStatus
		code     string
		exit     int
		hint     string
		provider string
	}{
		{
			name:   "real failure preferred over missing alias",
			emails: []string{"alias@example.com", "primary@example.com"},
			probes: map[string]gmail.AddressStatus{"primary@example.com": failedProbe("primary@example.com", gmail.AuthTokenRejected, "Gmail OAuth token exchange failed (HTTP 400, invalid_grant)")},
			status: gmail.AuthTokenRejected, code: "unavailable", exit: 3, hint: gmail.AuthTokenRejected.Hint(),
		},
		{
			name:   "all missing",
			emails: []string{"primary@example.com", "alias@example.com"},
			status: gmail.AuthCredentialsMissing, code: "unavailable", exit: 3, hint: gmail.AuthCredentialsMissing.Hint(),
		},
		{
			name:   "several connected",
			emails: []string{"primary@example.com", "alias@example.com"},
			probes: map[string]gmail.AddressStatus{
				"primary@example.com": connectedProbe("primary@example.com", "primary@example.com"),
				"alias@example.com":   connectedProbe("alias@example.com", "alias@example.com"),
			},
			status: authAmbiguous, code: "usage", exit: 1, hint: "choose one with --email",
		},
		{
			name:   "different Google account",
			emails: []string{"primary@example.com"},
			probes: map[string]gmail.AddressStatus{"primary@example.com": connectedProbe("primary@example.com", "other@example.com")},
			status: gmail.AuthAccountMismatch, code: "unavailable", exit: 3, hint: gmail.AuthAccountMismatch.Hint(), provider: "",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			code, stdout, stderr := runGmailAuth(t, &gmailAuthFakes{emails: tt.emails, probes: tt.probes}, authStatusArgs...)
			if code != tt.exit || stderr != "" {
				t.Fatalf("exit=%d stderr=%q stdout=%s", code, stderr, stdout)
			}
			envelope := decodeGmailAuth(t, stdout)
			if envelope.OK || envelope.Code != tt.code || envelope.ExitCode != tt.exit || envelope.Hint != tt.hint || envelope.Data.Status != tt.status || envelope.Data.Hint != tt.hint || envelope.Data.ProviderEmail != tt.provider {
				t.Fatalf("envelope = %+v", envelope)
			}
			if len(envelope.Data.Addresses) != len(tt.emails) || envelope.Summary != "Gmail auth: "+string(tt.status) {
				t.Fatalf("envelope = %+v", envelope)
			}
		})
	}
}

func TestLabelsAuthStatusAccountMismatchKeepsProviderEmail(t *testing.T) {
	fakes := &gmailAuthFakes{emails: []string{"primary@example.com"}, probes: map[string]gmail.AddressStatus{"primary@example.com": connectedProbe("primary@example.com", "other@example.com")}}
	_, stdout, _ := runGmailAuth(t, fakes, authStatusArgs...)
	address := decodeGmailAuth(t, stdout).Data.Addresses[0]
	if address.Status != gmail.AuthAccountMismatch || address.ProviderEmail != "other@example.com" || !strings.Contains(address.Detail, "other@example.com") || address.Hint != gmail.AuthAccountMismatch.Hint() {
		t.Fatalf("address = %+v", address)
	}
}

func TestLabelsAuthStatusAcceptsSoleStoredAlias(t *testing.T) {
	for _, selector := range []string{"", "old@example.com"} {
		t.Run("selector="+selector, func(t *testing.T) {
			fakes := &gmailAuthFakes{
				emails: []string{"old@example.com", "current@example.com"},
				probes: map[string]gmail.AddressStatus{
					"old@example.com": connectedProbe("old@example.com", "CURRENT@example.com"),
				},
			}
			args := append([]string(nil), authStatusArgs...)
			if selector != "" {
				args = append(args, "--email", selector)
			}
			code, stdout, stderr := runGmailAuth(t, fakes, args...)
			report := decodeGmailAuth(t, stdout).Data
			if code != 0 || stderr != "" || report.Status != gmail.AuthConnected || report.ProviderEmail != "CURRENT@example.com" || report.Addresses[0].Status != gmail.AuthConnected {
				t.Fatalf("usable account credentials rejected: exit=%d report=%+v stderr=%s", code, report, stderr)
			}
		})
	}
}

func TestLabelsAuthStatusEmailSelectsOneAddress(t *testing.T) {
	fakes := &gmailAuthFakes{
		emails: []string{"alias@example.com", "primary@example.com"},
		probes: map[string]gmail.AddressStatus{
			"primary@example.com": connectedProbe("primary@example.com", "primary@example.com"),
			"alias@example.com":   connectedProbe("alias@example.com", "alias@example.com"),
		},
	}
	code, stdout, stderr := runGmailAuth(t, fakes, append(authStatusArgs, "--email", "PRIMARY@example.com")...)
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	report := decodeGmailAuth(t, stdout).Data
	if len(report.Addresses) != 1 || report.Addresses[0].Email != "primary@example.com" || strings.Join(fakes.probed, ",") != "primary@example.com" {
		t.Fatalf("report = %+v probed = %v", report, fakes.probed)
	}

	fakes.probed = nil
	code, stdout, stderr = runGmailAuth(t, fakes, append(authStatusArgs, "--email", "other@example.com")...)
	if code != 1 || stdout != "" || !strings.Contains(stderr, "--email must match") || len(fakes.probed) != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q probed=%v", code, stdout, stderr, fakes.probed)
	}
}

func TestLabelsAuthStatusReturnsMailErrorsUnchanged(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		exit int
		want string
	}{
		{"no address", &mail.GmailLabelError{Kind: "unavailable", Hint: "Configure an actual email address on the Mail account", Cause: errors.New(`Mail account "Example Account" has no actual email address`)}, 3, "Configure an actual email address"},
		{"missing account", &mail.NotFoundError{Kind: "account", Name: "Example Account"}, 2, "not_found"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakes := &gmailAuthFakes{emailsErr: tt.err}
			code, stdout, stderr := runGmailAuth(t, fakes, authStatusArgs...)
			if code != tt.exit || stdout != "" || !strings.Contains(stderr, tt.want) || len(fakes.probed) != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q probed=%v", code, stdout, stderr, fakes.probed)
			}
		})
	}
}

func TestLabelsAuthStatusIsReadOnlyAndAcceptsJQ(t *testing.T) {
	a := newApplication()
	status, _, err := a.rootCmd.Find([]string{"labels", "auth", "status"})
	if err != nil || status.Name() != "status" {
		t.Fatalf("status command: %v", err)
	}
	if status.Annotations[annotationMutation] == "true" {
		t.Fatal("labels auth status must stay read-only")
	}
	fakes := &gmailAuthFakes{emails: []string{"primary@example.com"}, probes: map[string]gmail.AddressStatus{"primary@example.com": connectedProbe("primary@example.com", "primary@example.com")}}
	code, stdout, stderr := runGmailAuth(t, fakes, "labels", "auth", "status", "-a", "Example Account", "--jq", ".data.addresses[0].status")
	if code != 0 || stdout != "connected\n" || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

func TestLabelsAuthStatusPlainOutput(t *testing.T) {
	fakes := &gmailAuthFakes{
		emails: []string{"primary@example.com", "alias@example.com"},
		probes: map[string]gmail.AddressStatus{"primary@example.com": failedProbe("primary@example.com", gmail.AuthKeychainAccess, "Gmail Keychain read failed (Security status -50)")},
	}
	code, stdout, stderr := runGmailAuth(t, fakes, "labels", "auth", "status", "-a", "Example Account", "--plain", "--no-color")
	if code != 3 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, want := range []string{"EMAIL", "STATUS", "DETAIL", "primary@example.com", "keychain_access", "Security status -50", "alias@example.com", "credentials_missing", "Gmail auth: keychain_access"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("plain stdout omits %q:\n%s", want, stdout)
		}
	}
	if !strings.Contains(stderr, "unlock the login Keychain") {
		t.Fatalf("plain stderr omits the hint: %q", stderr)
	}
}

func TestGmailErrorHintsFollowAuthStatus(t *testing.T) {
	const details = "run 'mail-app-cli labels auth status -a <account>' for details"
	for _, tt := range []struct {
		name string
		err  error
		code clierr.Code
		hint string
	}{
		{"token rejected", fmt.Errorf("profile: %w", &gmail.TokenError{StatusCode: 400, Code: "invalid_grant"}), clierr.CodeUnavailable, gmail.AuthTokenRejected.Hint() + "; " + details},
		{"client rejected", &gmail.TokenError{StatusCode: 401, Code: "invalid_client"}, clierr.CodeUnavailable, gmail.AuthClientRejected.Hint() + "; " + details},
		{"keychain", &gmail.KeychainError{Operation: "read", Status: -25308}, clierr.CodeUnavailable, gmail.AuthKeychainAccess.Hint() + "; " + details},
		{"scope", gmail.ErrScopeInsufficient, clierr.CodeUnavailable, gmail.AuthScopeInsufficient.Hint() + "; " + details},
		{"provider denied", &gmail.RequestError{Operation: "profile", StatusCode: 403}, clierr.CodeUnavailable, gmail.AuthAccessDenied.Hint() + "; " + details},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, got := range []*clierr.Error{gmailLabelCommandError(tt.err), gmailCredentialsError(tt.err)} {
				if got.Code != tt.code || got.Hint != tt.hint || !errors.Is(got, tt.err) {
					t.Fatalf("error = %+v", got)
				}
			}
		})
	}
	unknown := errors.New("Keychain interaction unavailable")
	if got := gmailCredentialsError(unknown); !strings.Contains(got.Hint, "resolve any macOS Keychain access error") {
		t.Fatalf("unknown credential error hint = %q", got.Hint)
	}
	deadline := fmt.Errorf("Mail.app read: %w", context.DeadlineExceeded)
	if got := gmailLabelCommandError(deadline); got.Code != clierr.CodeTimeout || strings.Contains(got.Hint, "labels auth") {
		t.Fatalf("deadline error = %+v", got)
	}
}

// The account status must agree with selectGmailLabelEmail, which operational
// commands use: a second stored credential set makes the account ambiguous
// even when it no longer works, and a credential read failure on any address
// fails the account.
func TestLabelsAuthStatusMatchesOperationalSelection(t *testing.T) {
	primary := connectedProbe("primary@example.com", "primary@example.com")
	for _, tt := range []struct {
		name   string
		alias  gmail.AddressStatus
		status gmail.AuthStatus
		exit   int
		hint   string
	}{
		{"alias with revoked refresh token", failedProbe("alias@example.com", gmail.AuthTokenRejected, "Gmail OAuth token exchange failed (HTTP 400, invalid_grant)"), authAmbiguous, 1, authAmbiguousHint},
		{"alias with read-only scope", failedProbe("alias@example.com", gmail.AuthScopeInsufficient, gmail.ErrScopeInsufficient.Error()), gmail.AuthScopeInsufficient, 3, gmail.AuthScopeInsufficient.Hint()},
		{"alias Keychain refused", failedProbe("alias@example.com", gmail.AuthKeychainAccess, "Gmail Keychain read failed (Security status -50)"), gmail.AuthKeychainAccess, 3, gmail.AuthKeychainAccess.Hint()},
		{"alias connected to the same Google account", connectedProbe("alias@example.com", "primary@example.com"), authAmbiguous, 1, authAmbiguousHint},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fakes := &gmailAuthFakes{emails: []string{"primary@example.com", "alias@example.com"}, probes: map[string]gmail.AddressStatus{"primary@example.com": primary, "alias@example.com": tt.alias}}
			code, stdout, _ := runGmailAuth(t, fakes, authStatusArgs...)
			envelope := decodeGmailAuth(t, stdout)
			if code != tt.exit || envelope.OK || envelope.Data.Status != tt.status || envelope.Hint != tt.hint || envelope.Data.ProviderEmail != "" {
				t.Fatalf("exit=%d envelope=%+v", code, envelope)
			}
		})
	}

	// The selector applies the same rule to the same two addresses.
	emails := []string{"primary@example.com", "alias@example.com"}
	_, err := selectGmailLabelEmail(emails, "", func(string) error { return nil })
	if clierr.Classify(err).Code != clierr.CodeUsage {
		t.Fatalf("selector with two stored credential sets: %v", err)
	}
	_, err = selectGmailLabelEmail(emails, "", func(email string) error {
		if email == "alias@example.com" {
			return gmail.ErrScopeInsufficient
		}
		return nil
	})
	if !errors.Is(err, gmail.ErrScopeInsufficient) || clierr.Classify(err).Code != clierr.CodeUnavailable {
		t.Fatalf("selector with read-only alias: %v", err)
	}
}

func TestGmailPreflightErrorUsesTypedCauseHint(t *testing.T) {
	// GmailLabelWorkflow.bind wraps a failed profile read this way.
	bind := &mail.GmailLabelError{Kind: "unavailable", Hint: "Connect or import Gmail credentials for this Mail account", Cause: &gmail.TokenError{StatusCode: 400, Code: "invalid_grant"}}
	got := gmailLabelCommandError(fmt.Errorf("label preflight: %w", bind))
	if got.Code != clierr.CodeUnavailable || got.Hint != gmail.AuthTokenRejected.Hint()+"; run 'mail-app-cli labels auth status -a <account>' for details" {
		t.Fatalf("error = %+v", got)
	}
	plain := &mail.GmailLabelError{Kind: "unavailable", Hint: "Connect Gmail credentials whose profile matches an actual address of this Mail account", Cause: errors.New("Gmail profile email does not match")}
	if got := gmailLabelCommandError(plain); got.Hint != plain.Hint {
		t.Fatalf("untyped preflight hint replaced: %+v", got)
	}
}

func TestGmailRequestErrorsAboutTheTargetHaveNoAuthHint(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
	}{
		{404, "Gmail did not find the message or label"},
		{410, "Gmail did not find the message or label"},
		{400, "Gmail rejected the request"},
		{429, gmail.AuthProviderError.Hint()},
		{401, gmail.AuthTokenRejected.Hint()},
	} {
		t.Run(fmt.Sprint(tt.status), func(t *testing.T) {
			got := gmailLabelCommandError(&gmail.RequestError{Operation: "get message", StatusCode: tt.status})
			if got.Code != clierr.CodeUnavailable || !strings.HasPrefix(got.Hint, tt.want) {
				t.Fatalf("error = %+v", got)
			}
			if tt.status == 404 || tt.status == 410 || tt.status == 400 {
				if strings.Contains(got.Hint, "labels auth") {
					t.Fatalf("target error got an authorization hint: %q", got.Hint)
				}
			}
		})
	}
}

func TestLabelsAuthStatusRejectsUnsupportedAccountsWithoutProbing(t *testing.T) {
	for _, tt := range []struct {
		name    string
		account mail.Account
		detail  string
	}{
		{"disabled", mail.Account{Name: "Example Account", AccountType: "imap", Enabled: false, EmailAddress: "primary@example.com"}, "is disabled"},
		{"not IMAP", mail.Account{Name: "Example Account", AccountType: "iCloud", Enabled: true, EmailAddress: "one@example.com", EmailAddresses: []string{"two@example.com", "three@example.com"}}, "must be an IMAP account"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := newApplication()
			a.mailClient = mail.NewClient()
			a.mailClient.PrimeAccounts([]mail.Account{tt.account})
			var probed []string
			a.gmailProbe = func(_ context.Context, email string) gmail.AddressStatus {
				probed = append(probed, email)
				return connectedProbe(email, email)
			}
			report, err := a.gmailAuthStatus(context.Background(), "Example Account")
			if err != nil || report.Status != authAccountUnsupported || report.Hint != authAccountUnsupportedHint || !strings.Contains(report.Detail, tt.detail) || len(report.Addresses) != 0 || len(probed) != 0 {
				t.Fatalf("report=%+v err=%v probed=%v", report, err, probed)
			}
			failure := gmailAuthFailure(report)
			if failure == nil || failure.Code != clierr.CodeUnavailable || failure.Hint != authAccountUnsupportedHint {
				t.Fatalf("failure = %+v", failure)
			}
		})
	}
	a := newApplication()
	a.mailClient = mail.NewClient()
	a.mailClient.PrimeAccounts([]mail.Account{{Name: "Example Account", AccountType: "imap", Enabled: true, EmailAddress: "primary@example.com"}})
	a.gmailProbe = func(_ context.Context, email string) gmail.AddressStatus { return connectedProbe(email, email) }
	if report, err := a.gmailAuthStatus(context.Background(), "Example Account"); err != nil || report.Status != gmail.AuthConnected {
		t.Fatalf("supported account report=%+v err=%v", report, err)
	}
}
