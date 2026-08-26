package cmd

import (
	"os"
	"path/filepath"
	"time"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
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

func renderDoctor(r doctorResult) func(*output.Printer) {
	return func(p *output.Printer) {
		check := func(label string, ok bool, detail string) []string {
			state := p.Green("ok")
			if !ok {
				state = p.Red("fail")
			}
			return []string{label, state, output.Truncate(detail, 80)}
		}
		rows := [][]string{
			check("binary", r.CurrentBinaryAvailable, firstNonEmpty(r.CurrentBinaryError, r.CurrentBinary)),
			check("Mail.app bundle", r.MailBridgeAvailable, r.MailBridgeError),
			check("automation access", r.AutomationAccessAvailable, r.AutomationAccessError),
			check("account access", r.AccountAccessAvailable, firstNonEmpty(r.AccountAccessError, plural(r.AccountCount, "account"))),
			check("Envelope Index", r.EnvelopeIndexAvailable, firstNonEmpty(r.EnvelopeIndexError, "fast reads available")),
			check("live probe", r.LiveProbeAvailable, r.LiveProbeError),
		}
		p.Table([]string{"CHECK", "STATUS", "DETAIL"}, rows)
		p.Blank()
		if r.Healthy {
			p.Line("%s", p.Green("healthy"))
		} else {
			p.Line("%s", p.Red("not healthy"))
		}
		if !r.EnvelopeIndexAvailable {
			p.Line("%s", p.Dim("Grant Full Disk Access to the app running mail-app-cli for fast index-backed reads and cross-mailbox search."))
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check Mail.app access, permissions, and index availability",
	Annotations: map[string]string{
		annotationAgentNotes: "Run this first when any command exits 3. healthy=false with envelopeIndexAvailable=false means slow automation-only reads, not a broken install.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		result := diagnose(mailClient, version, os.Executable)
		summary := "healthy"
		if !result.Healthy {
			summary = "not healthy"
		}
		if err := writer.Write(output.Result{Data: result, Summary: summary, Plain: renderDoctor(result)}); err != nil {
			return err
		}
		if !result.Healthy {
			return clierr.New(clierr.CodeUnavailable, "Mail.app access is not fully available").WithHint("see the failed checks above")
		}
		return nil
	},
}
