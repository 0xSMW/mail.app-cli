package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
