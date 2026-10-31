package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// authAmbiguous is an account-level status: more than one address has
// working credentials, so operational commands need --email.
const authAmbiguous gmail.AuthStatus = "ambiguous_credentials"

const authAmbiguousHint = "choose one with --email"

// gmailAuthReport is the Gmail authorization state of one Mail.app account.
type gmailAuthReport struct {
	Account       string                `json:"account"`
	Status        gmail.AuthStatus      `json:"status"`
	ProviderEmail string                `json:"providerEmail,omitempty"`
	Detail        string                `json:"detail,omitempty"`
	Hint          string                `json:"hint,omitempty"`
	Addresses     []gmail.AddressStatus `json:"addresses"`
}

func (a *application) newLabelsAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Check stored Gmail credentials for each address of a Mail.app account", Args: cobra.NoArgs,
		Long:        "Check the Gmail credentials this CLI stores in macOS Keychain for each actual address of the selected Mail.app account. Each check exchanges the stored refresh token and reads the Gmail profile. Mail.app's own Google sign-in does not authorize this CLI.",
		Annotations: map[string]string{annotationAgentNotes: "Read-only. status is connected only when exactly one address has working credentials whose Gmail profile matches that address. ambiguous_credentials means several do; pass --email. Each address reports its own status, detail and hint. Exits 0 only when connected."},
		RunE: func(cmd *cobra.Command, args []string) error {
			account, err := a.requireAccount()
			if err != nil {
				return err
			}
			report, err := a.gmailAuthStatus(cmd.Context(), account)
			if err != nil {
				return err
			}
			return a.writer.Write(output.Result{Data: report, Summary: gmailAuthSummary(report), Plain: renderGmailAuth(report), Err: gmailAuthFailure(report)})
		},
	}
}

func (a *application) lookupGmailAccountEmails(ctx context.Context, account string) ([]string, error) {
	if a.gmailAccountEmails != nil {
		return a.gmailAccountEmails(ctx, account)
	}
	return mail.GmailAccountEmails(ctx, a.mailClient, account)
}

func (a *application) probeGmailAddress(ctx context.Context, email string) gmail.AddressStatus {
	if a.gmailProbe != nil {
		return a.gmailProbe(ctx, email)
	}
	return gmail.ProbeStoredCredentials(ctx, email, nil)
}

// gmailAuthStatus probes the selected address, or every actual address of the
// account. A Mail.app failure is returned as a command error with no report.
func (a *application) gmailAuthStatus(ctx context.Context, account string) (gmailAuthReport, error) {
	emails, err := a.lookupGmailAccountEmails(ctx, account)
	if err != nil {
		return gmailAuthReport{}, gmailLabelCommandError(err)
	}
	if a.labelsEmail != "" {
		email, err := selectGmailLabelEmail(emails, a.labelsEmail, nil)
		if err != nil {
			return gmailAuthReport{}, err
		}
		emails = []string{email}
	}
	report := gmailAuthReport{Account: account, Addresses: make([]gmail.AddressStatus, 0, len(emails))}
	for _, email := range emails {
		report.Addresses = append(report.Addresses, checkGmailAddress(a.probeGmailAddress(ctx, email), email))
	}
	summarizeGmailAuth(&report)
	return report, nil
}

// checkGmailAddress applies the account binding: working credentials for a
// different Google account do not authorize this address.
func checkGmailAddress(status gmail.AddressStatus, email string) gmail.AddressStatus {
	status.Email = email
	if status.Status == "" {
		status.Status = gmail.AuthUnknown
	}
	if status.Status == gmail.AuthConnected {
		status.Detail, status.Hint = "", ""
		if !strings.EqualFold(strings.TrimSpace(status.ProviderEmail), email) {
			status.Status = gmail.AuthAccountMismatch
			status.Detail = fmt.Sprintf("stored credentials authorize Gmail account %q, not %q", status.ProviderEmail, email)
			status.Hint = gmail.AuthAccountMismatch.Hint()
		}
	} else if status.Hint == "" {
		status.Hint = status.Status.Hint()
	}
	return status
}

// summarizeGmailAuth sets the account status. A real failure is reported in
// preference to a missing credential for another address, such as an alias.
func summarizeGmailAuth(report *gmailAuthReport) {
	var connected []gmail.AddressStatus
	for _, address := range report.Addresses {
		if address.Status == gmail.AuthConnected {
			connected = append(connected, address)
		}
	}
	switch len(connected) {
	case 1:
		report.Status, report.ProviderEmail = gmail.AuthConnected, connected[0].ProviderEmail
		return
	case 0:
	default:
		report.Status, report.Hint = authAmbiguous, authAmbiguousHint
		return
	}
	report.Status = gmail.AuthCredentialsMissing
	for _, address := range report.Addresses {
		if address.Status != gmail.AuthCredentialsMissing {
			report.Status = address.Status
			break
		}
	}
	report.Hint = report.Status.Hint()
}

func gmailAuthSummary(report gmailAuthReport) string {
	if report.Status == gmail.AuthConnected && report.ProviderEmail != "" {
		return fmt.Sprintf("Gmail auth: %s (%s)", report.Status, report.ProviderEmail)
	}
	return "Gmail auth: " + string(report.Status)
}

func gmailAuthFailure(report gmailAuthReport) *clierr.Error {
	switch report.Status {
	case gmail.AuthConnected:
		return nil
	case authAmbiguous:
		return clierr.Usage(fmt.Sprintf("several Gmail credential sets work for Mail.app account %q", report.Account)).WithHint(report.Hint)
	}
	return clierr.New(clierr.CodeUnavailable, fmt.Sprintf("Gmail authorization for Mail.app account %q: %s", report.Account, report.Status)).WithHint(report.Hint)
}

// renderGmailAuth shows one row per address. The account hint is printed with
// the error on stderr when the status is not connected.
func renderGmailAuth(report gmailAuthReport) func(*output.Printer) {
	return func(p *output.Printer) {
		rows := make([][]string, 0, len(report.Addresses))
		for _, address := range report.Addresses {
			state := p.Green(string(address.Status))
			if address.Status != gmail.AuthConnected {
				state = p.Red(string(address.Status))
			}
			detail := address.Detail
			if address.Status == gmail.AuthConnected {
				detail = "Gmail profile " + address.ProviderEmail
			}
			rows = append(rows, []string{address.Email, state, output.Truncate(detail, 80)})
		}
		p.Table([]string{"EMAIL", "STATUS", "DETAIL"}, rows)
		p.Blank()
		p.Line("%s", gmailAuthSummary(report))
	}
}
