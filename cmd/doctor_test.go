package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

type fakeDoctorClient struct {
	bridgeErr     error
	automationErr error
	accountCount  int
	accountErr    error
	indexErr      error
	liveProbeErr  error
	processIDs    []int
	processErr    error
}

func (c fakeDoctorClient) RunningMailProcessIDs(time.Duration) ([]int, error) {
	return c.processIDs, c.processErr
}

func TestDiagnoseDistinguishesRunningMailFromUnreachableBridge(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mail-app-cli")
	if err := os.WriteFile(binary, []byte("binary"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		pids        []int
		processErr  error
		wantContext bool
	}{
		{name: "running but unreachable", pids: []int{42}, wantContext: true},
		{name: "stopped"},
		{name: "unknown", processErr: errors.New("process probe timed out")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := diagnose(fakeDoctorClient{processIDs: tc.pids, processErr: tc.processErr,
				accountErr:   errors.New("Application isn't running. (-600)"),
				liveProbeErr: errors.New("Application isn't running. (-600)")}, "test", func() (string, error) { return binary, nil })
			if r.Healthy || r.AccountAccessAvailable || r.LiveProbeAvailable {
				t.Fatalf("unreachable bridge was marked healthy: %+v", r)
			}
			if tc.processErr != nil {
				if r.MailRunning != nil || r.MailProcessError != tc.processErr.Error() {
					t.Fatalf("failed process probe must remain unknown: %+v", r)
				}
			} else if r.MailRunning == nil || *r.MailRunning != (len(tc.pids) > 0) {
				t.Fatalf("incorrect process observation: %+v", r)
			}
			failure := doctorFailure(r)
			if failure == nil || strings.Contains(failure.Error(), "observed running") != tc.wantContext {
				t.Fatalf("failure = %v, contextual = %t", failure, tc.wantContext)
			}
		})
	}
}

func (c fakeDoctorClient) CheckMailBridge() error                    { return c.bridgeErr }
func (c fakeDoctorClient) CheckAutomationAccess(time.Duration) error { return c.automationErr }
func (c fakeDoctorClient) CheckAccountAccess(time.Duration) (int, error) {
	return c.accountCount, c.accountErr
}
func (c fakeDoctorClient) CheckEnvelopeIndex() error        { return c.indexErr }
func (c fakeDoctorClient) RunLiveProbe(time.Duration) error { return c.liveProbeErr }

func TestDiagnoseReportsPartialDiagnosticsAndFailsClosedOnBridge(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mail-app-cli")
	if err := os.WriteFile(binary, []byte("binary"), 0755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	bridgeErr := errors.New("Application can't be found. (-2700)")
	result := diagnose(fakeDoctorClient{
		bridgeErr:    bridgeErr,
		accountCount: 2,
		indexErr:     errors.New("authorization denied"),
	}, "test-version", func() (string, error) { return binary, nil })

	if result.Healthy {
		t.Fatal("Healthy = true, want false when live Mail bridge access fails")
	}
	if result.MailBridgeAvailable || result.MailBridgeError != bridgeErr.Error() {
		t.Fatalf("bridge diagnostic = %+v", result)
	}
	if !result.AutomationAccessAvailable || !result.AccountAccessAvailable || result.AccountCount != 2 {
		t.Fatalf("independent diagnostics were not retained: %+v", result)
	}
	if result.EnvelopeIndexAvailable || result.EnvelopeIndexError == "" {
		t.Fatalf("index diagnostic = %+v", result)
	}
}

func TestDiagnoseMarksMissingCurrentBinaryUnhealthy(t *testing.T) {
	result := diagnose(fakeDoctorClient{accountCount: 1}, "test-version", func() (string, error) {
		return "/definitely/missing/mail-app-cli", nil
	})
	if result.CurrentBinaryAvailable || result.CurrentBinaryError == "" {
		t.Fatalf("current binary diagnostic = %+v", result)
	}
	if result.Healthy {
		t.Fatal("Healthy = true, want false with an unavailable current binary")
	}
}

func TestDiagnoseKeepsBridgeAvailableWhenAutomationIsDenied(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "mail-app-cli")
	if err := os.WriteFile(binary, []byte("binary"), 0755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	result := diagnose(fakeDoctorClient{
		automationErr: errors.New("Not authorized to send Apple events to Mail."),
		accountCount:  1,
	}, "test-version", func() (string, error) { return binary, nil })

	if !result.MailBridgeAvailable {
		t.Fatalf("MailBridgeAvailable = false, want true: %+v", result)
	}
	if result.AutomationAccessAvailable || result.AutomationAccessError == "" {
		t.Fatalf("automation diagnostic = %+v", result)
	}
	if result.Healthy {
		t.Fatal("Healthy = true, want false when automation permission is denied")
	}
}

func doctorApp(t *testing.T, format output.Format, fakes *gmailAuthFakes) (*application, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	a := newApplication()
	a.resolved.Account = config.Value{Value: "Example Account", Source: config.SourceFlag}
	fakes.install(a)
	var stdout, stderr bytes.Buffer
	var err error
	a.writer, err = output.New(format, &stdout, &stderr, false, "", "doctor", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	return a, &stdout, &stderr
}

type doctorEnvelope struct {
	OK      bool            `json:"ok"`
	Summary string          `json:"summary"`
	Code    string          `json:"code"`
	Error   string          `json:"error"`
	Hint    string          `json:"hint"`
	Data    json.RawMessage `json:"data"`
}

func decodeDoctor(t *testing.T, stdout *bytes.Buffer) (doctorEnvelope, doctorResult, map[string]any) {
	t.Helper()
	var envelope doctorEnvelope
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("doctor stdout: %v\n%s", err, stdout)
	}
	var result doctorResult
	var raw map[string]any
	if err := json.Unmarshal(envelope.Data, &result); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Data, &raw); err != nil {
		t.Fatal(err)
	}
	return envelope, result, raw
}

func TestDoctorGmailFailsWhenBridgeIsHealthy(t *testing.T) {
	fakes := &gmailAuthFakes{
		emails: []string{"primary@example.com"},
		probes: map[string]gmail.AddressStatus{"primary@example.com": failedProbe("primary@example.com", gmail.AuthTokenRejected, "Gmail OAuth token exchange failed (HTTP 400, invalid_grant)")},
	}
	a, stdout, _ := doctorApp(t, output.FormatJSON, fakes)
	err := a.runDoctor(context.Background(), fakeDoctorClient{accountCount: 1, processIDs: []int{42}}, true)
	failure := clierr.Classify(err)
	if failure == nil || failure.Code != clierr.CodeUnavailable || !strings.Contains(failure.Message, `"Example Account"`) || !strings.Contains(failure.Message, "token_rejected") || failure.Hint != gmail.AuthTokenRejected.Hint() {
		t.Fatalf("failure = %+v", failure)
	}
	envelope, result, _ := decodeDoctor(t, stdout)
	if envelope.OK || envelope.Summary != "not healthy" || !result.Healthy || result.GmailHealthy == nil || *result.GmailHealthy {
		t.Fatalf("envelope = %+v result = %+v", envelope, result)
	}
	if result.Gmail == nil || result.Gmail.Status != gmail.AuthTokenRejected || len(result.Gmail.Addresses) != 1 {
		t.Fatalf("gmail = %+v", result.Gmail)
	}
}

func TestDoctorGmailConnected(t *testing.T) {
	fakes := &gmailAuthFakes{emails: []string{"primary@example.com"}, probes: map[string]gmail.AddressStatus{"primary@example.com": connectedProbe("primary@example.com", "primary@example.com")}}
	a, stdout, _ := doctorApp(t, output.FormatJSON, fakes)
	if err := a.runDoctor(context.Background(), fakeDoctorClient{accountCount: 1, processIDs: []int{42}}, true); err != nil {
		t.Fatal(err)
	}
	envelope, result, _ := decodeDoctor(t, stdout)
	if !envelope.OK || envelope.Summary != "healthy" || result.GmailHealthy == nil || !*result.GmailHealthy || result.Gmail.ProviderEmail != "primary@example.com" {
		t.Fatalf("envelope = %+v result = %+v", envelope, result)
	}
}

func TestDoctorGmailWithBrokenBridgeReportsMailError(t *testing.T) {
	fakes := &gmailAuthFakes{emailsErr: errors.New("Application isn't running. (-600)")}
	a, stdout, _ := doctorApp(t, output.FormatJSON, fakes)
	err := a.runDoctor(context.Background(), fakeDoctorClient{bridgeErr: errors.New("Application can't be found. (-2700)")}, true)
	failure := clierr.Classify(err)
	if failure == nil || failure.Code != clierr.CodeUnavailable || !strings.Contains(failure.Message, "Mail.app access") {
		t.Fatalf("failure = %+v", failure)
	}
	envelope, result, _ := decodeDoctor(t, stdout)
	if envelope.OK || result.Healthy || result.GmailHealthy == nil || *result.GmailHealthy || result.Gmail == nil {
		t.Fatalf("result = %+v", result)
	}
	if result.Gmail.Status != gmail.AuthUnknown || !strings.Contains(result.Gmail.Detail, "(-600)") || result.Gmail.Hint == "" || result.Gmail.Account != "Example Account" || len(fakes.probed) != 0 {
		t.Fatalf("gmail = %+v probed = %v", result.Gmail, fakes.probed)
	}
}

func TestDoctorWithoutGmailOmitsGmailFields(t *testing.T) {
	fakes := &gmailAuthFakes{}
	a, stdout, _ := doctorApp(t, output.FormatJSON, fakes)
	if err := a.runDoctor(context.Background(), fakeDoctorClient{accountCount: 1, processIDs: []int{42}}, false); err != nil {
		t.Fatal(err)
	}
	_, _, raw := decodeDoctor(t, stdout)
	for _, key := range []string{"gmail", "gmailHealthy"} {
		if _, ok := raw[key]; ok {
			t.Fatalf("doctor without --gmail emitted %q: %s", key, stdout)
		}
	}
	if len(fakes.probed) != 0 {
		t.Fatalf("doctor without --gmail probed %v", fakes.probed)
	}
}

func TestDoctorGmailPlainRow(t *testing.T) {
	fakes := &gmailAuthFakes{emails: []string{"primary@example.com"}, probes: map[string]gmail.AddressStatus{"primary@example.com": connectedProbe("primary@example.com", "primary@example.com")}}
	a, stdout, _ := doctorApp(t, output.FormatPlain, fakes)
	if err := a.runDoctor(context.Background(), fakeDoctorClient{accountCount: 1, processIDs: []int{42}}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Gmail auth") || !strings.Contains(stdout.String(), "connected (primary@example.com)") || !strings.Contains(stdout.String(), "healthy") {
		t.Fatalf("plain doctor = %s", stdout)
	}
}

func TestDoctorGmailAccountLookupFailureKeepsItsCode(t *testing.T) {
	fakes := &gmailAuthFakes{emailsErr: &mail.NotFoundError{Kind: "account", Name: "Typo"}}
	a, stdout, _ := doctorApp(t, output.FormatJSON, fakes)
	err := a.runDoctor(context.Background(), fakeDoctorClient{accountCount: 1, processIDs: []int{42}}, true)
	failure := clierr.Classify(err)
	if failure == nil || failure.Code != clierr.CodeNotFound || clierr.ExitCode(failure.Code) != 2 || !strings.Contains(failure.Message, "Gmail authorization was not checked") {
		t.Fatalf("failure = %+v", failure)
	}
	if strings.Contains(failure.Hint, "failed Mail.app checks") || failure.Hint == "" {
		t.Fatalf("healthy bridge got a Mail.app hint: %q", failure.Hint)
	}
	envelope, result, _ := decodeDoctor(t, stdout)
	if envelope.OK || envelope.Code != "not_found" || !result.Healthy || result.Gmail == nil || result.Gmail.Status != gmail.AuthUnknown || result.Gmail.Detail == "" {
		t.Fatalf("envelope = %+v result = %+v", envelope, result)
	}
}

func TestDoctorGmailWithoutAccountStillWritesDiagnosis(t *testing.T) {
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte("#!/bin/sh\nprintf '%s' '[{\"id\":\"one\",\"name\":\"Example Account\",\"enabled\":true},{\"id\":\"two\",\"name\":\"Other Account\",\"enabled\":true}]'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	fakes := &gmailAuthFakes{}
	a, stdout, _ := doctorApp(t, output.FormatJSON, fakes)
	a.resolved.Account = config.Value{}
	a.flagBackend = "legacy"
	a.mailClient = mail.NewClient().WithContext(context.Background())
	err := a.runDoctor(context.Background(), fakeDoctorClient{accountCount: 2, processIDs: []int{42}}, true)
	failure := clierr.Classify(err)
	if failure == nil || failure.Code != clierr.CodeUsage || !strings.Contains(failure.Message, "choose an account with --account") {
		t.Fatalf("failure = %+v", failure)
	}
	envelope, result, _ := decodeDoctor(t, stdout)
	if envelope.OK || envelope.Code != "usage" || !result.Healthy || result.AccountCount != 2 || result.Gmail == nil || result.Gmail.Status != gmail.AuthUnknown || !strings.Contains(result.Gmail.Detail, "choose an account") || len(fakes.probed) != 0 {
		t.Fatalf("envelope = %+v result = %+v", envelope, result)
	}
}

func TestDoctorGmailAmbiguousIsUsage(t *testing.T) {
	fakes := &gmailAuthFakes{emails: []string{"primary@example.com", "alias@example.com"}, probes: map[string]gmail.AddressStatus{
		"primary@example.com": connectedProbe("primary@example.com", "primary@example.com"),
		"alias@example.com":   connectedProbe("alias@example.com", "alias@example.com"),
	}}
	a, _, _ := doctorApp(t, output.FormatJSON, fakes)
	failure := clierr.Classify(a.runDoctor(context.Background(), fakeDoctorClient{accountCount: 1, processIDs: []int{42}}, true))
	if failure == nil || failure.Code != clierr.CodeUsage || !strings.Contains(failure.Hint, "--email on 'labels' commands") {
		t.Fatalf("failure = %+v", failure)
	}
}

func TestDoctorGmailUnsupportedAccount(t *testing.T) {
	a, stdout, _ := doctorApp(t, output.FormatJSON, &gmailAuthFakes{})
	a.gmailAccountEmails = nil
	a.mailClient = mail.NewClient()
	a.mailClient.PrimeAccounts([]mail.Account{{Name: "Example Account", AccountType: "iCloud", Enabled: true, EmailAddress: "one@example.com"}})
	failure := clierr.Classify(a.runDoctor(context.Background(), fakeDoctorClient{accountCount: 1, processIDs: []int{42}}, true))
	if failure == nil || failure.Code != clierr.CodeUnavailable || failure.Hint != authAccountUnsupportedHint {
		t.Fatalf("failure = %+v", failure)
	}
	_, result, _ := decodeDoctor(t, stdout)
	if result.Gmail == nil || result.Gmail.Status != authAccountUnsupported || len(result.Gmail.Addresses) != 0 {
		t.Fatalf("gmail = %+v", result.Gmail)
	}
}

func TestDoctorGmailPlainShowsFailureDetail(t *testing.T) {
	fakes := &gmailAuthFakes{emailsErr: errors.New("Application isn't running. (-600)")}
	a, stdout, _ := doctorApp(t, output.FormatPlain, fakes)
	_ = a.runDoctor(context.Background(), fakeDoctorClient{bridgeErr: errors.New("Application can't be found. (-2700)")}, true)
	if !strings.Contains(stdout.String(), "unknown: Application isn't running. (-600)") || !strings.Contains(stdout.String(), "not healthy") {
		t.Fatalf("plain doctor = %s", stdout)
	}
}

func TestDoctorGmailFlagIsInvocationOwned(t *testing.T) {
	one, two := newApplication(), newApplication()
	if err := one.doctorCmd.Flags().Set("gmail", "true"); err != nil {
		t.Fatal(err)
	}
	if value, _ := two.doctorCmd.Flags().GetBool("gmail"); value {
		t.Fatal("--gmail leaked to another invocation")
	}
}
