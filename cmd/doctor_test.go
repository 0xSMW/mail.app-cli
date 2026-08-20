package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeDoctorClient struct {
	bridgeErr     error
	automationErr error
	accountCount  int
	accountErr    error
	indexErr      error
	liveProbeErr  error
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
