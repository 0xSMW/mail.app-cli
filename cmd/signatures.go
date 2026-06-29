package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
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

func init() {
	signaturesCmd.AddCommand(signaturesListCmd, signaturesShowCmd)
}
