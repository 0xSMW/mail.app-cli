package cmd

import (
	"errors"
	"fmt"
	netmail "net/mail"
	"os/exec"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

func (a *application) newLabelsCmd() *cobra.Command {
	labels := &cobra.Command{
		Use: "labels", Short: "Inspect and change Gmail labels through an explicit provider adapter", GroupID: groupManage,
		Long: "Inspect and change Gmail labels using credentials bound to a Mail.app account. Existing archive and move commands keep their Mail.app behavior. See docs/gmail-labels.md for setup and receipt semantics.",
	}
	labels.PersistentFlags().StringVar(&a.labelsEmail, "email", "", "Gmail provider email among the selected Mail.app account's actual addresses")
	list := &cobra.Command{
		Use: "list", Short: "List canonical Gmail label IDs and full names", Args: cobra.NoArgs,
		Annotations: map[string]string{annotationAgentNotes: "Fresh provider labels for the selected Mail.app account. Canonical IDs and exact full names are accepted by add/remove."},
		RunE: func(cmd *cobra.Command, args []string) error {
			account, err := a.requireAccount()
			if err != nil {
				return err
			}
			workflow, err := a.gmailLabelsWorkflow(cmd, account)
			if err != nil {
				return err
			}
			labels, err := workflow.List(cmd.Context(), account)
			if err != nil {
				return gmailLabelCommandError(err)
			}
			return a.writer.Write(output.Result{Data: labels, Summary: plural(len(labels), "Gmail label"), Plain: func(p *output.Printer) {
				for _, label := range labels {
					p.Line("%s\t%s", label.ID, label.Name)
				}
			}})
		},
	}
	markIDList(list)
	show := &cobra.Command{
		Use: "show <message-id>", Short: "Show current Gmail labels for a Mail.app message", Args: labelMessageArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			request, notices, err := a.gmailLabelRequest(args[0])
			if err != nil {
				return err
			}
			workflow, err := a.gmailLabelsWorkflow(cmd, request.Account)
			if err != nil {
				return err
			}
			result, err := workflow.Show(cmd.Context(), request)
			return a.writeGmailLabelResult(result, notices, err)
		},
	}
	labels.AddCommand(list, show, a.newLabelChangeCmd(true), a.newLabelChangeCmd(false), a.newLabelReconcileCmd(), a.newLabelsAuthCmd())
	return labels
}

func labelMessageArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.ExactArgs(1)(cmd, args); err != nil {
		return err
	}
	if !isNumericID(strings.TrimSpace(args[0])) {
		return clierr.Usagef("message ID %q is not numeric; use a Mail.app message ID from list or search", args[0])
	}
	return nil
}

func (a *application) newLabelChangeCmd(add bool) *cobra.Command {
	verb := "remove"
	if add {
		verb = "add"
	}
	var label string
	var dryRun bool
	cmd := &cobra.Command{
		Use: verb + " <message-id>", Short: strings.ToUpper(verb[:1]) + verb[1:] + " one Gmail label and verify fresh provider state",
		Annotations: map[string]string{annotationAgentNotes: "Resolve one Mail.app message to one exact Gmail identity. --label accepts a canonical ID or exact full name. Always verifies a fresh provider read after a dispatched mutation; receipt distinguishes provider confirmation from Mail.app convergence. Reconcile the receipt after interruption; do not blindly replay."},
		Args: func(cmd *cobra.Command, args []string) error {
			if err := labelMessageArgs(cmd, args); err != nil {
				return err
			}
			if strings.TrimSpace(label) == "" {
				return clierr.Usage("--label must name a canonical Gmail label ID or exact full name")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			request, notices, err := a.gmailLabelRequest(args[0])
			if err != nil {
				return err
			}
			request.Label, request.Add, request.DryRun = label, add, dryRun
			workflow, err := a.gmailLabelsWorkflow(cmd, request.Account)
			if err != nil {
				return err
			}
			result, err := workflow.Change(cmd.Context(), request)
			return a.writeGmailLabelResult(result, notices, err)
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "Canonical Gmail label ID or exact full name (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Resolve identity and label and read memberships without changing labels")
	markMutation(cmd)
	return cmd
}

func (a *application) gmailLabelRequest(id string) (mail.GmailLabelRequest, []string, error) {
	refs, notices, err := a.locateMessages([]string{strings.TrimSpace(id)})
	if err != nil {
		return mail.GmailLabelRequest{}, nil, err
	}
	request, err := a.gmailLabelRequestFromRef(refs[0])
	return request, notices, err
}

func (a *application) gmailLabelRequestFromRef(ref messageRef) (mail.GmailLabelRequest, error) {
	if a.accountExplicit() && a.resolved.Account.Value != ref.Account {
		return mail.GmailLabelRequest{}, clierr.New(clierr.CodeNotFound, fmt.Sprintf("message %s is in account %q, not the exact selected account %q", ref.ID, ref.Account, a.resolved.Account.Value)).WithHint("use the exact Mail.app account name from 'accounts list'")
	}
	return mail.GmailLabelRequest{Account: ref.Account, Mailbox: ref.Mailbox, MessageID: ref.ID}, nil
}

func (a *application) gmailLabelsWorkflow(cmd *cobra.Command, account string) (*mail.GmailLabelWorkflow, error) {
	emails, err := mail.GmailAccountEmails(cmd.Context(), a.mailClient, account)
	if err != nil {
		return nil, gmailLabelCommandError(err)
	}
	email, err := selectGmailLabelEmail(emails, a.labelsEmail, func(email string) error {
		_, err := gmail.LoadCredentials(email)
		return err
	})
	if err != nil {
		return nil, err
	}
	provider, err := gmail.NewStoredClient(email, nil)
	if err != nil {
		return nil, gmailCredentialsError(err)
	}
	return mail.NewGmailLabelWorkflow(a.mailClient, provider), nil
}

// A nil lookup selects the first address for an explicit authorization flow.
// Operational commands probe every actual address, failing closed on any
// Keychain error other than an absent credential entry.
func selectGmailLabelEmail(emails []string, explicit string, lookup func(string) error) (string, error) {
	if explicit != "" {
		value := strings.TrimSpace(explicit)
		parsed, err := netmail.ParseAddress(value)
		if err != nil || parsed.Address != value || !strings.Contains(value, "@") {
			return "", clierr.Usage("--email must be a valid email address configured on the selected Mail.app account")
		}
		for _, email := range emails {
			if strings.EqualFold(email, parsed.Address) {
				return email, nil
			}
		}
		return "", clierr.Usage("--email must match an actual address of the selected Mail.app account").WithHint("choose an address shown by 'accounts show <account-name>'")
	}
	if len(emails) == 0 {
		return "", clierr.New(clierr.CodeUnavailable, "Mail.app account has no actual email address")
	}
	if lookup == nil {
		return emails[0], nil
	}
	var matches []string
	for _, email := range emails {
		err := lookup(email)
		if err == nil {
			matches = append(matches, email)
		} else if !errors.Is(err, gmail.ErrCredentialsNotFound) {
			return "", gmailCredentialsError(err)
		}
	}
	switch len(matches) {
	case 0:
		return "", gmailCredentialsError(gmail.ErrCredentialsNotFound)
	case 1:
		return matches[0], nil
	default:
		return "", clierr.Usage("multiple Gmail credential sets match this Mail.app account; choose one with --email")
	}
}

func gmailCredentialsError(err error) *clierr.Error {
	return clierr.Wrap(clierr.CodeUnavailable, err, err.Error()).WithHint("connect this account with 'mail-app-cli labels auth connect --client-secret <path> --account <name> --email <address>' or import credentials with 'labels auth import --credentials <path> --email <address>'; resolve any macOS Keychain access error before retrying")
}

func (a *application) newLabelReconcileCmd() *cobra.Command {
	return &cobra.Command{
		Use: "reconcile <receipt-file>", Short: "Observe a Gmail label receipt without repeating its mutation", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{annotationAgentNotes: "Read-only fresh provider and Mail.app observation. Uses the receipt's account and provider identity; never repeats a label modification or synchronizes Mail.app. Provider confirmation and Mail.app convergence are separate."},
		RunE: func(cmd *cobra.Command, args []string) error {
			receipt, err := mail.ReadGmailLabelReceipt(args[0])
			if err != nil {
				return clierr.Usagef("read Gmail label receipt: %v", err)
			}
			if a.accountExplicit() && a.resolved.Account.Value != receipt.Account {
				return clierr.Usage("--account must match the Gmail label receipt account")
			}
			workflow, err := a.gmailLabelsWorkflow(cmd, receipt.Account)
			if err != nil {
				receipt.Status, receipt.ProviderConfirmed, receipt.MailStatus = "unknown", false, "pending"
				return a.writeGmailLabelResult(receipt, nil, err)
			}
			result, err := workflow.Reconcile(cmd.Context(), args[0])
			return a.writeGmailLabelResult(result, nil, err)
		},
	}
}

func (a *application) writeGmailLabelResult(result mail.GmailLabelResult, notices []string, err error) error {
	failure := gmailLabelCommandError(err)
	if err != nil {
		result.Error = err.Error()
	}
	if err != nil && result.ReceiptPath != "" {
		failure = clierr.Wrap(clierr.CodeMutationFailed, err, err.Error()).WithHint("observe the receipt with 'mail-app-cli labels reconcile <receipt-file>'; inspect the result before retrying")
	}
	return a.writer.Write(output.Result{Data: result, Notices: notices, Err: failure,
		Summary: fmt.Sprintf("Gmail labels: %s (provider confirmed: %v; Mail.app: %s)", result.Status, result.ProviderConfirmed, result.MailStatus),
	})
}

func gmailLabelCommandError(err error) *clierr.Error {
	if err == nil {
		return nil
	}
	var preflight *mail.GmailLabelError
	if errors.As(err, &preflight) {
		code := clierr.CodeUnavailable
		switch preflight.Kind {
		case "usage":
			code = clierr.CodeUsage
		case "not_found":
			code = clierr.CodeNotFound
		}
		return clierr.Wrap(code, err, err.Error()).WithHint(preflight.Hint)
	}
	var capability *mail.CapabilityError
	if errors.As(err, &capability) {
		return clierr.Wrap(clierr.CodeUnavailable, err, err.Error())
	}
	var provider *gmail.RequestError
	if errors.As(err, &provider) {
		return clierr.Wrap(clierr.CodeUnavailable, err, err.Error()).WithHint("check Gmail access; connect the correct account with 'labels auth connect' or 'labels auth import'")
	}
	return clierr.Classify(err)
}

func (a *application) newLabelsAuthCmd() *cobra.Command {
	auth := &cobra.Command{Use: "auth", Short: "Connect Gmail credentials to a Mail.app account"}
	for _, connect := range []bool{false, true} {
		var path string
		verb, flag, short := "import", "credentials", "Import a private credential file into macOS Keychain"
		if connect {
			verb, flag, short = "connect", "client-secret", "Authorize Gmail in a browser using a desktop OAuth client"
		}
		cmd := &cobra.Command{
			Use: verb, Short: short,
			Args: func(cmd *cobra.Command, args []string) error {
				if err := cobra.NoArgs(cmd, args); err != nil {
					return err
				}
				if strings.TrimSpace(path) == "" {
					return clierr.Usagef("--%s requires a file path", flag)
				}
				return nil
			},
			RunE: func(cmd *cobra.Command, args []string) error {
				account, err := a.requireAccount()
				if err != nil {
					return err
				}
				emails, err := mail.GmailAccountEmails(cmd.Context(), a.mailClient, account)
				if err != nil {
					return gmailLabelCommandError(err)
				}
				email, err := selectGmailLabelEmail(emails, a.labelsEmail, nil)
				if err != nil {
					return err
				}
				var profile gmail.Profile
				if connect {
					profile, err = gmail.Connect(cmd.Context(), path, email, func(url string) error { return exec.CommandContext(cmd.Context(), "open", url).Run() }, nil)
				} else {
					profile, err = gmail.ImportCredentials(cmd.Context(), path, email, nil)
				}
				if err != nil {
					return clierr.Wrap(clierr.CodeUnavailable, err, err.Error())
				}
				return a.writer.Write(output.Result{Data: map[string]string{"account": account, "providerEmail": profile.EmailAddress, "status": "connected"}, Summary: "Gmail credentials connected for " + account})
			},
		}
		cmd.Flags().StringVar(&path, flag, "", "Path to the private JSON file (required; never pass tokens as arguments)")
		markMutation(cmd)
		auth.AddCommand(cmd)
	}
	return auth
}
