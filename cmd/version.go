package cmd

import (
	"runtime"

	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version",
	RunE: func(cmd *cobra.Command, args []string) error {
		data := map[string]any{
			"version":       version,
			"schemaVersion": mail.SchemaVersion,
			"goVersion":     runtime.Version(),
			"os":            runtime.GOOS,
			"arch":          runtime.GOARCH,
		}
		return writer.Write(output.Result{
			Data:    data,
			Summary: "mail-app-cli " + version,
			Plain:   renderLine("mail-app-cli %s (schema %d, %s %s/%s)", version, mail.SchemaVersion, runtime.Version(), runtime.GOOS, runtime.GOARCH),
		})
	},
}
