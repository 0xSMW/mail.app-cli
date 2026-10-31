package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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
		Long: "Inspect and change Gmail labels using credentials bound to a Mail.app account. show, add, and remove accept several message IDs from one Mail.app account. Check authorization with 'labels auth status'. Existing archive and move commands keep their Mail.app behavior. See docs/gmail-labels.md for setup and receipt semantics.",
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
		Use: "show <message-id>...", Short: "Show current Gmail labels for Mail.app messages", Args: labelMessageArgs,
		Annotations: map[string]string{annotationAgentNotes: "Read-only fresh provider and Mail.app observation. Several IDs must be in one Mail.app account; they are read in argument order and the run stops at the first failure with later IDs reported as not_attempted. One ID returns the single result shape; several return items with per-message results."},
		RunE: func(cmd *cobra.Command, args []string) error {
			requests, notices, err := a.gmailLabelRequests(args)
			if err != nil {
				return err
			}
			workflow, err := a.gmailLabelsWorkflow(cmd, requests[0].Account)
			if err != nil {
				return err
			}
			return a.runGmailLabelRequests(cmd.Context(), "show", "", false, requests, notices, workflow.Show)
		},
	}
	labels.AddCommand(list, show, a.newLabelChangeCmd(true), a.newLabelChangeCmd(false), a.newLabelReconcileCmd(), a.newLabelsAuthCmd())
	return labels
}

// labelMessageArgs requires one or more numeric Mail.app message IDs and
// rejects duplicates, which locateMessages would otherwise collapse silently.
func labelMessageArgs(cmd *cobra.Command, args []string) error {
	if err := cobra.MinimumNArgs(1)(cmd, args); err != nil {
		return err
	}
	seen := make(map[string]bool, len(args))
	for _, arg := range args {
		id := strings.TrimSpace(arg)
		if !isNumericID(id) {
			return clierr.Usagef("message ID %q is not numeric; use a Mail.app message ID from list or search", arg)
		}
		if seen[id] {
			return clierr.Usagef("message ID %s is given more than once; pass each ID once", id)
		}
		seen[id] = true
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
		Use: verb + " <message-id>...", Short: strings.ToUpper(verb[:1]) + verb[1:] + " one Gmail label for one or more messages and verify fresh provider state",
		Annotations: map[string]string{annotationAgentNotes: "Resolve each Mail.app message to one exact Gmail identity. Several IDs must be in one Mail.app account; they run in argument order, each with its own receipt and fresh provider verification, and the run stops at the first failure with later IDs reported as not_attempted. One ID returns the single result shape. --label accepts a canonical ID or exact full name. Always verifies a fresh provider read after a dispatched mutation; receipt distinguishes provider confirmation from Mail.app convergence. Reconcile the receipt after interruption; do not blindly replay."},
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
			requests, notices, err := a.gmailLabelRequests(args)
			if err != nil {
				return err
			}
			for i := range requests {
				requests[i].Label, requests[i].Add, requests[i].DryRun = label, add, dryRun
			}
			workflow, err := a.gmailLabelsWorkflow(cmd, requests[0].Account)
			if err != nil {
				return err
			}
			return a.runGmailLabelRequests(cmd.Context(), verb, label, dryRun, requests, notices, workflow.Change)
		},
	}
	cmd.Flags().StringVar(&label, "label", "", "Canonical Gmail label ID or exact full name (required)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Resolve identity and label and read memberships without changing labels")
	markMutation(cmd)
	return cmd
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
	provider, err := selectGmailLabelProvider(emails, a.labelsEmail, func(email string) (*gmail.Client, error) {
		return gmail.NewStoredClient(email, nil)
	})
	if err != nil {
		return nil, err
	}
	return mail.NewGmailLabelWorkflow(a.mailClient, provider), nil
}

func selectGmailLabelProvider(emails []string, explicit string, load func(string) (*gmail.Client, error)) (*gmail.Client, error) {
	providers := make(map[string]*gmail.Client)
	email, err := selectGmailLabelEmail(emails, explicit, func(email string) error {
		provider, err := load(email)
		if err == nil {
			providers[email] = provider
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if provider, ok := providers[email]; ok {
		return provider, nil
	}
	// Explicit address selection validates membership before reading Keychain.
	provider, err := load(email)
	if err != nil {
		return nil, gmailCredentialsError(err)
	}
	return provider, nil
}

// A nil lookup selects the first address for an explicit authorization flow.
// Operational commands probe every actual address, failing closed on any
// Keychain error other than an absent credential entry. labels auth status
// applies the same rule through gmail.AuthStatus.Stored.
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
		} else if gmail.ClassifyAuthError(err).Stored() {
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
	if hint, ok := gmailAuthHint(err); ok {
		return clierr.Wrap(clierr.CodeUnavailable, err, err.Error()).WithHint(hint)
	}
	return clierr.Wrap(clierr.CodeUnavailable, err, err.Error()).WithHint("connect this account with 'mail-app-cli labels auth connect --client-secret <path> --account <name> --email <address>' or import credentials with 'labels auth import --credentials <path> --email <address>'; resolve any macOS Keychain access error before retrying")
}

// gmailAuthHint returns the recovery step for a typed Gmail credential,
// token or provider error. A context error comes from the caller's deadline
// or cancellation, which may be Mail.app's, so it is not given a Gmail hint.
func gmailAuthHint(err error) (string, bool) {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "", false
	}
	status := gmail.ClassifyAuthError(err)
	if status == gmail.AuthUnknown || status == gmail.AuthConnected {
		return "", false
	}
	return status.Hint() + "; run 'mail-app-cli labels auth status -a <account>' for details", true
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
		hint := preflight.Hint
		// The workflow wraps a failed Gmail profile read in a generic
		// preflight error. A typed Gmail cause has a more specific hint.
		if provider, ok := gmailProviderHint(preflight.Cause); ok {
			hint = provider
		}
		return clierr.Wrap(code, err, err.Error()).WithHint(hint)
	}
	var capability *mail.CapabilityError
	if errors.As(err, &capability) {
		return clierr.Wrap(clierr.CodeUnavailable, err, err.Error())
	}
	if hint, ok := gmailProviderHint(err); ok {
		return clierr.Wrap(clierr.CodeUnavailable, err, err.Error()).WithHint(hint)
	}
	return clierr.Classify(err)
}

// gmailProviderHint returns the hint for a Gmail request or authorization
// error. A Gmail API 4xx other than 401, 403 and 429 is about the requested
// message or label, not authorization.
func gmailProviderHint(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var request *gmail.RequestError
	if errors.As(err, &request) {
		switch code := request.StatusCode; {
		case code == http.StatusNotFound || code == http.StatusGone:
			return "Gmail did not find the message or label; read it again with 'labels show' before retrying", true
		case code >= 400 && code < 500 && code != http.StatusUnauthorized && code != http.StatusForbidden && code != http.StatusTooManyRequests:
			return "Gmail rejected the request; read the message again with 'labels show' and check the label before retrying", true
		}
	}
	return gmailAuthHint(err)
}

func (a *application) newLabelsAuthCmd() *cobra.Command {
	auth := &cobra.Command{Use: "auth", Short: "Connect and check Gmail credentials for a Mail.app account"}
	auth.AddCommand(a.newLabelsAuthStatusCmd())
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
