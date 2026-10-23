package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

func (a *application) buildUnsubscribeCommands() {
	a.unsubscribeCmd = &cobra.Command{
		Use: "unsubscribe", Short: "Discover unsubscribe opportunities from local messages without taking action",
		Long: "Inspect local message headers and bodies for unsubscribe and preference controls. No links are opened or fetched, no mail is sent, and no subscriptions are changed.",
	}
	a.unsubscribeCmd.AddCommand(a.newUnsubscribeShowCmd(), a.newUnsubscribeScanCmd())
}

func (a *application) newUnsubscribeShowCmd() *cobra.Command {
	return &cobra.Command{
		Use: "show <message-id>", Short: "Show unsubscribe and preferences destinations from a local message", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{annotationAgentNotes: "Read-only complete local discovery: inspect every supported local MIME alternative and headers. No Mail header/body fallback or rendering. Partial or missing local sources retain available controls and exit 5. For per-message Mail header fallback use links --unsubscribe. Discovery never opens URLs, sends mail, or unsubscribes."},
		RunE: func(cmd *cobra.Command, args []string) error {
			if !isNumericID(args[0]) {
				return clierr.Usagef("message ID %q is not numeric", args[0])
			}
			refs, notices, err := a.locateMessages(args)
			if err != nil {
				return err
			}
			ref := refs[0]
			observation, err := a.mailClient.GetUnsubscribeJSON(ref.Account, ref.Mailbox, ref.ID)
			if err != nil {
				return fmt.Errorf("discover unsubscribe controls: %w", err)
			}
			if observation == nil {
				return clierr.New(clierr.CodeNotFound, "message not found: "+ref.ID)
			}
			return a.writeUnsubscribeObservation(observation, notices)
		},
	}
}

func (a *application) writeUnsubscribeObservation(o *mail.UnsubscribeObservation, notices []string) error {
	var failure *clierr.Error
	if !o.Complete {
		failure = clierr.New(clierr.CodePartial, "local unsubscribe discovery incomplete; inspect available destinations and warnings")
	}
	return a.writer.Write(output.Result{Data: o, Notices: notices, Err: failure, Summary: fmt.Sprintf("%d unsubscribe and preferences destinations for message %s", len(o.Destinations), o.ID), Plain: func(p *output.Printer) {
		p.Line("Message %s (%s / %s)", linkDisplay(o.ID), linkDisplay(o.Account), linkDisplay(strings.Join(o.Mailboxes, ", ")))
		if o.ListID != "" {
			p.Line("List: %s", linkDisplay(o.ListID))
		}
		renderUnsubscribeDestinations(p, o.Destinations)
		if !o.Complete {
			p.Line("Local discovery is incomplete; additional controls may be unavailable.")
		}
		if o.Error != "" {
			p.Line("Source: %s", linkDisplay(o.Error))
		}
		for _, warning := range o.Warnings {
			p.Line("Source: %s", linkDisplay(warning))
		}
		if o.OneClickAdvertised {
			p.Line("One-click is advertised; authentication and endpoint behavior are unverified.")
		}
	}})
}

func (a *application) newUnsubscribeScanCmd() *cobra.Command {
	var request mail.UnsubscribeScanRequest
	var allAccounts bool
	cmd := &cobra.Command{
		Use: "scan <mailbox> [mailbox...]", Short: "Find and group unsubscribe opportunities across local mailboxes", Args: cobra.MinimumNArgs(1),
		Annotations: map[string]string{annotationAgentNotes: "Read-only local discovery of every enumerated message's headers and all supported body alternatives; no sampling or body-render fallback. Iterates mailbox pages until exhausted or budget/max-pages reached. Always checkpoints private compact discoveries before advancing; repeat with returned --checkpoint and original scopes to resume. Completed checkpoints replay stored observations; --restart-checkpoint starts fresh. --retry-incomplete retries only earlier partial/unavailable local messages and continues traversal. JSON groups by List-Id when available, otherwise provisional sender/header-host evidence, deduplicates exact destinations with provenance, and retains historical-only controls. Complete requires exhausted scopes, no pending work, and complete extraction for every observed message. Exit 5 retains groups and explicit exceptions. --page-size is pagination, not sampling. Sequential observations are not an atomic snapshot. Nothing is opened, fetched, sent, or unsubscribed. --jq is supported; the private checkpoint is still saved."},
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.mailboxExplicit() {
				return clierr.Usage("unsubscribe scan takes positional mailboxes; do not also pass --mailbox")
			}
			if allAccounts && cmd.Flags().Changed("account") {
				return clierr.Usage("--all-accounts cannot be combined with --account")
			}
			request.Scopes = nil
			for _, box := range uniqueStrings(trimAll(args)) {
				request.Scopes = append(request.Scopes, mail.SearchMailbox{Account: "validation", Mailbox: box})
			}
			if err := mail.ValidateUnsubscribeScanRequest(request); err != nil {
				return clierr.Usage(err.Error())
			}
			var accounts []string
			if allAccounts {
				inventory, err := a.mailClient.GetAccountsJSON()
				if err != nil {
					return err
				}
				for _, account := range inventory {
					accounts = append(accounts, account.Name)
				}
				if len(accounts) == 0 {
					return clierr.New(clierr.CodeNotFound, "no enabled Mail accounts")
				}
			} else {
				account, err := a.requireAccount()
				if err != nil {
					return err
				}
				accounts = []string{account}
			}
			request.Scopes = nil
			for _, account := range accounts {
				for _, box := range uniqueStrings(trimAll(args)) {
					request.Scopes = append(request.Scopes, mail.SearchMailbox{Account: account, Mailbox: box})
				}
			}
			result, err := a.mailClient.ScanUnsubscribe(request)
			if errors.Is(err, mail.ErrInvalidUnsubscribeCheckpoint) {
				return clierr.Usage(err.Error())
			}
			if err != nil {
				return err
			}
			return a.writeUnsubscribeScan(result)
		},
	}
	cmd.Flags().IntVar(&request.PageSize, "page-size", 200, "Messages per mailbox page (1–5000); every page is traversed")
	cmd.Flags().IntVar(&request.MaxPages, "max-pages", 0, "Maximum new pages this invocation (0 continues until exhausted or budget reached)")
	cmd.Flags().DurationVar(&request.Budget, "budget", 45*time.Second, "Time budget per invocation; saved pending work can be resumed")
	cmd.Flags().StringVar(&request.Since, "since", "", "Only messages received since a date or timestamp")
	cmd.Flags().StringVar(&request.Checkpoint, "checkpoint", "", "Resume or save private discovery state (default: a new private cache file)")
	cmd.Flags().BoolVar(&request.Restart, "restart-checkpoint", false, "Start a fresh traversal in an existing discovery checkpoint")
	cmd.Flags().BoolVar(&request.RetryIncomplete, "retry-incomplete", false, "Reinspect earlier incomplete local messages and continue pending work")
	cmd.Flags().BoolVar(&allAccounts, "all-accounts", false, "Scan the named mailboxes in every enabled account")
	return cmd
}

func (a *application) writeUnsubscribeScan(result mail.UnsubscribeScanResult) error {
	var failure *clierr.Error
	if !result.Complete {
		failure = clierr.New(clierr.CodePartial, "unsubscribe discovery incomplete; inspect coverage, exceptions, and pending work; resume with --checkpoint")
	}
	return a.writer.Write(output.Result{Data: result, Err: failure, Summary: fmt.Sprintf("%d unsubscribe groups from %d inspected messages (complete: %v)", len(result.Groups), result.Readability.Inspected, result.Complete), Plain: func(p *output.Printer) {
		p.Line("%d unsubscribe groups from %d inspected messages", len(result.Groups), result.Readability.Inspected)
		p.Line("%s", linkDisplay(result.Scope))
		for _, coverage := range result.Coverage {
			p.Line("%s/%s: %d enumerated, exhausted: %v", linkDisplay(coverage.Account), linkDisplay(coverage.Mailbox), coverage.Count, coverage.Exhausted)
			if coverage.Error != "" {
				p.Line("  %s", linkDisplay(coverage.Error))
			}
		}
		for i, group := range result.Groups {
			name := group.ListID
			if name == "" {
				name = strings.Join(group.Senders, ", ")
			}
			label := ""
			if group.Provisional {
				label = " (provisional grouping)"
			}
			p.Blank()
			p.Line("%d. %s%s", i+1, linkDisplay(name), label)
			p.Line("   %d messages; latest %s (%s)", group.MessageCount, linkDisplay(group.Latest.ID), linkDisplay(group.Latest.DateReceived))
			renderUnsubscribeDestinations(p, group.Destinations)
			if group.DestinationsVary {
				p.Line("   Destinations vary across messages; freshness is unverified.")
			}
		}
		for _, exception := range result.Exceptions {
			p.Line("Incomplete %s/%s: %s", linkDisplay(exception.Account), linkDisplay(exception.ID), linkDisplay(exception.Error))
		}
		p.Line("Coverage: %d complete, %d partial, %d unavailable, %d pending; complete: %v", result.Readability.Complete, result.Readability.Partial, result.Readability.Unavailable, result.Pending, result.Complete)
		p.Line("Checkpoint: %s", linkDisplay(result.Checkpoint))
	}})
}

func renderUnsubscribeDestinations(p *output.Printer, destinations []mail.UnsubscribeDestination) {
	if len(destinations) == 0 {
		p.Line("  No controls found in the available local source.")
		return
	}
	for i, d := range destinations {
		label := d.Kind
		sources := map[string]bool{}
		for _, e := range d.Evidence {
			sources[e.Source] = true
			if label == d.Kind && e.Text != "" {
				label = e.Text
			}
		}
		var sourceNames []string
		for source := range sources {
			sourceNames = append(sourceNames, source)
		}
		sort.Strings(sourceNames)
		if !d.Openable {
			sourceNames = append(sourceNames, "unsupported destination")
		}
		p.Line("  %d. %s [%s]", i+1, linkDisplay(label), linkDisplay(strings.Join(sourceNames, ", ")))
		p.Line("     %s", linkDisplay(d.URL))
	}
}
