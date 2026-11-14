package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

func (a *application) newManifestCmd() *cobra.Command {
	root := &cobra.Command{Use: "manifest", Short: "Apply or verify an explicit cleanup manifest"}
	var dryRun bool
	apply := &cobra.Command{Use: "apply <file>", Short: "Resolve every selected identity, then apply the manifest sequentially", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := mail.ReadManifest(args[0])
		if err != nil {
			return clierr.Usagef("read manifest: %v", err)
		}
		if err := a.validateManifestScope(m); err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(a.mailClient.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		r, err := mail.ApplyManifest(a.mailClient.WithContext(ctx), m, dryRun)
		for _, item := range m.Items {
			if !dryRun {
				invalidateMailboxCache(item.Account, item.SourceMailbox)
				for _, box := range item.RequiredPresent {
					invalidateMailboxCache(item.Account, box)
				}
			}
		}
		return a.writer.Write(output.Result{Data: r, Summary: fmt.Sprintf("Manifest: %d selected, %d verified, %d planned, %d unresolved", r.Matched, r.Verified, r.Planned, r.Failed), Err: clierr.Classify(err)})
	}}
	apply.Flags().BoolVar(&dryRun, "dry-run", false, "Resolve identities and preview changes without mutating mail")
	markMutation(apply)
	verify := &cobra.Command{Use: "verify <file>", Short: "Observe required memberships and read state without changing mail", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		m, err := mail.ReadManifest(args[0])
		if err != nil {
			return clierr.Usagef("read manifest: %v", err)
		}
		if err := a.validateManifestScope(m); err != nil {
			return err
		}
		r, err := mail.VerifyManifest(a.mailClient, m)
		return a.writer.Write(output.Result{Data: r, Summary: fmt.Sprintf("Manifest: %d of %d verified; %d unresolved", r.Verified, r.Matched, r.Failed), Err: clierr.Classify(err)})
	}}
	root.AddCommand(apply, verify)
	return root
}

func (a *application) validateManifestScope(m mail.CleanupManifest) error {
	for i, item := range m.Items {
		if a.accountExplicit() && item.Account != a.resolved.Account.Value {
			return clierr.Usagef("manifest item %d account %q conflicts with explicit --account %q", i+1, item.Account, a.resolved.Account.Value)
		}
		if a.mailboxExplicit() && item.SourceMailbox != a.resolved.Mailbox.Value {
			return clierr.Usagef("manifest item %d sourceMailbox %q conflicts with explicit --mailbox %q", i+1, item.SourceMailbox, a.resolved.Mailbox.Value)
		}
	}
	return nil
}
