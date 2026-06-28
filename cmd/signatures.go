package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	signatureAccount string
	signatureDryRun  bool
)

var signaturesCmd = &cobra.Command{
	Use:   "signatures",
	Short: "List and inspect Mail.app signatures",
}

var signaturesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List signatures",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		signatures, err := client.ListSignatures(false)
		if err != nil {
			return fmt.Errorf("failed to list signatures: %w", err)
		}
		return printJSON(signatures, "signatures")
	},
}

var signaturesShowCmd = &cobra.Command{
	Use:   "show [name]",
	Short: "Show a signature",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		signature, err := client.SignatureByName(args[0])
		if err != nil {
			return err
		}
		return printJSON(signature, "signature")
	},
}

var signaturesApplyCmd = &cobra.Command{
	Use:   "apply [name]",
	Short: "Apply a signature as the default for an account",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if signatureAccount == "" {
			return fmt.Errorf("--account is required")
		}
		if signatureDryRun {
			return printJSON(map[string]any{"dryRun": true, "signature": args[0], "account": signatureAccount}, "signature apply dry-run")
		}
		return fmt.Errorf("signatures apply is unsupported because Mail.app default-signature assignment is not reliably exposed through this local scriptability layer")
	},
}

func init() {
	signaturesCmd.AddCommand(signaturesListCmd, signaturesShowCmd, signaturesApplyCmd)
	signaturesApplyCmd.Flags().StringVarP(&signatureAccount, "account", "a", "", "Account name")
	signaturesApplyCmd.Flags().BoolVar(&signatureDryRun, "dry-run", false, "Show mutation without applying it")
}
