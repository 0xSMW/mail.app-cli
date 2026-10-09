package cmd

import (
	"fmt"
	"time"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

func newMessagesReconcileCmd() *cobra.Command {
	var settle, budget time.Duration
	cmd := &cobra.Command{
		Use:         "reconcile <receipt-file>",
		Short:       "Recheck archived or moved identities without changing mail",
		Args:        cobra.ExactArgs(1),
		Annotations: map[string]string{annotationAgentNotes: "Read-only final observation of an archive/move receipt or journal, including identities previously confirmed. Does not repeat mutations or synchronize accounts. Checks current source and destination memberships and requested read state; exits 6 when unresolved. This is an observation, not a guarantee against future provider changes."},
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := mail.BatchOptions{Verify: true, Settle: settle, VerifyBudget: budget}
			if err := mail.ValidateBatchOptions(opts); err != nil {
				return clierr.Usage(err.Error())
			}
			receipt, err := mail.ReadRelocationReceipt(args[0])
			if err != nil {
				return clierr.Usagef("read relocation receipt: %v", err)
			}
			opts.Action = receipt.Action
			opts.MarkReadBefore = receipt.MarkReadRequested
			result, err := mail.ReconcileMutations(mailClient, opts, receipt)
			return writer.Write(output.Result{Data: result, Summary: fmt.Sprintf("Reconciled %d %s identities (%d unverified)", len(result.Items), result.Action, result.Unverified), Err: clierr.Classify(err)})
		},
	}
	cmd.Flags().DurationVar(&settle, "settle", 0, "Final observation settling window (0 uses the default)")
	cmd.Flags().DurationVar(&budget, "verify-budget", 30*time.Second, "Overall reconciliation budget including reads and queueing")
	return cmd
}

func init() { messagesCmd.AddCommand(newMessagesReconcileCmd()) }
