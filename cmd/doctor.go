package cmd

import (
	"os"
	"path/filepath"
	"time"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

type doctorResult struct {
	CLIVersion                string `json:"cliVersion"`
	CurrentBinaryAvailable    bool   `json:"currentBinaryAvailable"`
	CurrentBinary             string `json:"currentBinary,omitempty"`
	CurrentBinaryError        string `json:"currentBinaryError,omitempty"`
	MailBridgeAvailable       bool   `json:"mailBridgeAvailable"`
	MailBridgeError           string `json:"mailBridgeError,omitempty"`
	AutomationAccessAvailable bool   `json:"automationAccessAvailable"`
	AutomationAccessError     string `json:"automationAccessError,omitempty"`
	AccountAccessAvailable    bool   `json:"accountAccessAvailable"`
	AccountAccessError        string `json:"accountAccessError,omitempty"`
	AccountCount              int    `json:"accountCount,omitempty"`
	EnvelopeIndexAvailable    bool   `json:"envelopeIndexAvailable"`
	EnvelopeIndexError        string `json:"envelopeIndexError,omitempty"`
	LiveProbeAvailable        bool   `json:"liveProbeAvailable"`
	LiveProbeError            string `json:"liveProbeError,omitempty"`
	Healthy                   bool   `json:"healthy"`
}

type doctorClient interface {
	CheckMailBridge() error
	CheckAutomationAccess(timeout time.Duration) error
	CheckAccountAccess(timeout time.Duration) (int, error)
	CheckEnvelopeIndex() error
	RunLiveProbe(timeout time.Duration) error
}

func diagnose(client doctorClient, cliVersion string, executable func() (string, error)) doctorResult {
	result := doctorResult{CLIVersion: cliVersion}
	if binaryPath, err := executable(); err != nil {
		result.CurrentBinaryError = err.Error()
	} else if info, err := os.Stat(binaryPath); err != nil {
		result.CurrentBinary = binaryPath
		result.CurrentBinaryError = err.Error()
	} else if info.IsDir() {
		result.CurrentBinary = binaryPath
		result.CurrentBinaryError = "current executable path is a directory"
	} else {
		result.CurrentBinaryAvailable = true
		result.CurrentBinary = filepath.Clean(binaryPath)
	}

	if err := client.CheckMailBridge(); err != nil {
		result.MailBridgeError = err.Error()
	} else {
		result.MailBridgeAvailable = true
	}
	if err := client.CheckAutomationAccess(mail.DoctorProbeTimeout); err != nil {
		result.AutomationAccessError = err.Error()
	} else {
		result.AutomationAccessAvailable = true
	}
	if count, err := client.CheckAccountAccess(mail.DoctorProbeTimeout); err != nil {
		result.AccountAccessError = err.Error()
	} else {
		result.AccountAccessAvailable = true
		result.AccountCount = count
	}
	result.EnvelopeIndexAvailable = true
	if err := client.CheckEnvelopeIndex(); err != nil {
		result.EnvelopeIndexAvailable = false
		result.EnvelopeIndexError = err.Error()
	}
	if err := client.RunLiveProbe(mail.DoctorProbeTimeout); err != nil {
		result.LiveProbeError = err.Error()
	} else {
		result.LiveProbeAvailable = true
	}

	// A usable index or cached data cannot make the CLI healthy when the live
	// Mail.app bridge is unavailable.
	result.Healthy = result.CurrentBinaryAvailable && result.MailBridgeAvailable &&
		result.AutomationAccessAvailable && result.AccountAccessAvailable &&
		result.LiveProbeAvailable
	return result
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check local Mail.app CLI health",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		result := diagnose(client, rootCmd.Version, os.Executable)
		return printJSON(result, "doctor")
	},
}
