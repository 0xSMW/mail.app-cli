package cmd

import (
	"os"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/config"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/spf13/cobra"
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Read and write the default account, mailbox, and output format",
	Long: `Settings live in ` + "`~/.config/mail-app-cli/config.json`" + ` (or $XDG_CONFIG_HOME, or
$MAIL_APP_CLI_CONFIG). Each key resolves as flag > env > config > default.

Keys:
  account   default for --account       env MAIL_APP_CLI_ACCOUNT
  mailbox   default for --mailbox       env MAIL_APP_CLI_MAILBOX   (default INBOX)
  output    auto | json | plain          env MAIL_APP_CLI_OUTPUT    (default auto)`,
	Annotations: map[string]string{
		annotationAgentNotes: "'config show' reports each value with its source, so you can tell a flag from a stale default.",
	},
}

var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show every setting with where it came from",
	RunE: func(cmd *cobra.Command, args []string) error {
		rows := make([][]string, 0, 3)
		for _, row := range resolved.Rows() {
			rows = append(rows, row)
		}
		return writer.Write(output.Result{
			Data:    resolved,
			Summary: "config from " + resolved.Path,
			Plain: func(p *output.Printer) {
				p.Table([]string{"KEY", "VALUE", "SOURCE"}, rows)
				p.Blank()
				p.Line("%s", p.Dim("file: "+resolved.Path))
			},
		})
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a key in the config file",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := cfg.Set(args[0], args[1]); err != nil {
			return clierr.Wrap(clierr.CodeUsage, err, err.Error())
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    map[string]any{"key": args[0], "value": args[1], "path": resolved.Path},
			Summary: "Set " + args[0] + " = " + args[1],
			Plain:   renderLine("Set %s = %s", args[0], args[1]),
		})
	},
}

var configUnsetCmd = &cobra.Command{
	Use:   "unset <key>",
	Short: "Remove a key from the config file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := cfg.Set(args[0], ""); err != nil {
			return clierr.Wrap(clierr.CodeUsage, err, err.Error())
		}
		if err := config.Save(cfg); err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    map[string]any{"key": args[0], "path": resolved.Path},
			Summary: "Unset " + args[0],
			Plain:   renderLine("Unset %s", args[0]),
		})
	},
}

var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print the config file path",
	RunE: func(cmd *cobra.Command, args []string) error {
		_, statErr := os.Stat(resolved.Path)
		return writer.Write(output.Result{
			Data:    map[string]any{"path": resolved.Path, "exists": statErr == nil},
			Summary: resolved.Path,
			Plain:   renderLine("%s", resolved.Path),
		})
	},
}

func init() {
	configCmd.AddCommand(configShowCmd, configSetCmd, configUnsetCmd, configPathCmd)
}
