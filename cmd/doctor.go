package cmd

import (
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

type doctorResult struct {
	EnvelopeIndexAvailable bool   `json:"envelopeIndexAvailable"`
	EnvelopeIndexError     string `json:"envelopeIndexError,omitempty"`
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check local Mail.app CLI health",
	RunE: func(cmd *cobra.Command, args []string) error {
		client := mail.NewClient()
		result := doctorResult{EnvelopeIndexAvailable: true}
		if err := client.CheckEnvelopeIndex(); err != nil {
			result.EnvelopeIndexAvailable = false
			result.EnvelopeIndexError = err.Error()
		}
		return printJSON(result, "doctor")
	},
}
