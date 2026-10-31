package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// Account-level statuses. authAmbiguous: more than one address has stored
// credentials, so operational commands need --email. authAccountUnsupported:
// the Mail.app account is disabled or not IMAP, so no credentials can help.
const (
	authAmbiguous          gmail.AuthStatus = "ambiguous_credentials"
	authAccountUnsupported gmail.AuthStatus = "account_unsupported"
)

const (
	authAmbiguousHint          = "choose one with --email"
	authAccountUnsupportedHint = "Gmail labels need an enabled IMAP account in Mail; select the Gmail IMAP account"
)

// gmailAuthReport is the Gmail authorization state of one Mail.app account.
type gmailAuthReport struct {
	Account       string                `json:"account"`
	Status        gmail.AuthStatus      `json:"status"`
	ProviderEmail string                `json:"providerEmail,omitempty"`
	Detail        string                `json:"detail,omitempty"`
	Hint          string                `json:"hint,omitempty"`
	Addresses     []gmail.AddressStatus `json:"addresses"`
	// failure is set when the check could not run. Only doctor reads it.
	failure *clierr.Error
}

func (a *application) newLabelsAuthStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use: "status", Short: "Check stored Gmail credentials, refreshing and saving the access token as label commands do", Args: cobra.NoArgs,
		Long:        "Check the Gmail credentials this CLI stores in macOS Keychain for each actual address of the selected Mail.app account. Mail.app's own Google sign-in does not authorize this CLI. Each check exchanges the stored refresh token for a new access token, saves the result to Keychain the same way a label command does, and reads the Gmail profile. A Keychain write failure is reported as keychain_access. A disabled or non-IMAP account reports account_unsupported without reading Keychain.",
		Annotations: map[string]string{annotationAgentNotes: "Not a mutation, but it saves the refreshed access token to Keychain like any label command. The account status follows operational credential selection: connected only when exactly one address has stored credentials and they work for the account; the Gmail profile may match any actual account address. A Keychain, invalid-credential or scope failure on any address fails the account; two or more stored credential sets are ambiguous_credentials (pass --email). account_unsupported means the Mail.app account is disabled or not IMAP. Each address reports its own status, detail and hint. Exits 0 only when connected."},
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

// lookupGmailAccountEmails applies the label workflow's account checks
// before listing the account's actual addresses.
func (a *application) lookupGmailAccountEmails(ctx context.Context, account string) ([]string, error) {
	if a.gmailAccountEmails != nil {
		return a.gmailAccountEmails(ctx, account)
	}
	if err := mail.GmailAccountSupport(ctx, a.mailClient, account); err != nil {
		return nil, err
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
// A disabled or non-IMAP account is a report, with no address probed.
func (a *application) gmailAuthStatus(ctx context.Context, account string) (gmailAuthReport, error) {
	accountEmails, err := a.lookupGmailAccountEmails(ctx, account)
	if errors.Is(err, mail.ErrGmailAccountUnsupported) {
		return gmailAuthReport{Account: account, Status: authAccountUnsupported, Detail: err.Error(), Hint: authAccountUnsupportedHint, Addresses: []gmail.AddressStatus{}}, nil
	}
	if err != nil {
		return gmailAuthReport{}, gmailLabelCommandError(err)
	}
	emails := accountEmails
	if a.labelsEmail != "" {
		email, err := selectGmailLabelEmail(emails, a.labelsEmail, nil)
		if err != nil {
			return gmailAuthReport{}, err
		}
		emails = []string{email}
	}
	report := gmailAuthReport{Account: account, Addresses: make([]gmail.AddressStatus, 0, len(emails))}
	for _, email := range emails {
		report.Addresses = append(report.Addresses, checkGmailAddress(a.probeGmailAddress(ctx, email), email, accountEmails))
	}
	summarizeGmailAuth(&report)
	return report, nil
}

// checkGmailAddress applies the account binding: working credentials for a
// different Google account do not authorize this Mail account. The Keychain
// lookup address can be an alias; bind to any actual account address, as the
// label workflow does, even when --email selects one credential entry.
func checkGmailAddress(status gmail.AddressStatus, email string, accountEmails []string) gmail.AddressStatus {
	status.Email = email
	if status.Status == "" {
		status.Status = gmail.AuthUnknown
	}
	if status.Status == gmail.AuthConnected {
		status.Detail, status.Hint = "", ""
		matchesAccount := false
		for _, address := range accountEmails {
			if strings.EqualFold(strings.TrimSpace(status.ProviderEmail), address) {
				matchesAccount = true
				break
			}
		}
		if !matchesAccount {
			status.Status = gmail.AuthAccountMismatch
			status.Detail = fmt.Sprintf("stored credentials authorize Gmail account %q, which is not an actual address of the selected Mail account", status.ProviderEmail)
			status.Hint = gmail.AuthAccountMismatch.Hint()
		}
	} else if status.Hint == "" {
		status.Hint = status.Status.Hint()
	}
	return status
}

// summarizeGmailAuth sets the account status with the rule operational
// commands use to select credentials (selectGmailLabelEmail):
//   - a credential read failure on any address fails the account;
//   - two or more addresses with stored credentials are ambiguous;
//   - one address with stored credentials gives its status;
//   - otherwise credentials are missing.
func summarizeGmailAuth(report *gmailAuthReport) {
	var stored []gmail.AddressStatus
	for _, address := range report.Addresses {
		if address.Status.StopsSelection() {
			report.Status, report.Hint = address.Status, address.Status.Hint()
			return
		}
		if address.Status.Stored() {
			stored = append(stored, address)
		}
	}
	switch len(stored) {
	case 0:
		report.Status = gmail.AuthCredentialsMissing
	case 1:
		report.Status = stored[0].Status
		if report.Status == gmail.AuthConnected {
			report.ProviderEmail = stored[0].ProviderEmail
			return
		}
	default:
		report.Status, report.Hint = authAmbiguous, authAmbiguousHint
		return
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
		return clierr.Usage(fmt.Sprintf("several Gmail credential sets are stored for Mail.app account %q", report.Account)).WithHint(report.Hint)
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
