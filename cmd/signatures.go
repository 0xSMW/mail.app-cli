package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/spf13/cobra"
)

var signaturesCmd = &cobra.Command{
	Use:   "signatures",
	Short: "List and show Mail.app signatures",
}

var signaturesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List signatures",
	RunE: func(cmd *cobra.Command, args []string) error {
		signatures, err := mailClient.ListSignatures(false)
		if err != nil {
			return fmt.Errorf("list signatures: %w", err)
		}
		rows := make([][]string, 0, len(signatures))
		for _, s := range signatures {
			rows = append(rows, []string{s.Name})
		}
		return writer.Write(output.Result{
			Data:    signatures,
			Summary: plural(len(signatures), "signature"),
			Plain:   renderTable([]string{"NAME"}, rows, "no signatures"),
		})
	},
}

var signaturesShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a signature's content",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		signature, err := mailClient.SignatureByName(args[0])
		if err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    signature,
			Summary: "Signature " + signature.Name,
			Plain: func(p *output.Printer) {
				p.Line("%s", p.Bold(signature.Name))
				p.Blank()
				p.Line("%s", signature.Content)
			},
		})
	},
}

func init() {
	signaturesCmd.AddCommand(signaturesListCmd, signaturesShowCmd)
}
