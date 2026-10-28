package cmd

import (
	"os"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/spf13/cobra"
)

func (a *application) buildConfigCommands() {
	a.configCmd = &cobra.Command{
		Use:   "config",
		Short: "Read and write defaults and the sending restriction",
		Long: `Settings live in ` + "`~/.config/mail-app-cli/config.json`" + ` (or $XDG_CONFIG_HOME, or
$MAIL_APP_CLI_CONFIG). Account, mailbox, and output resolve as flag > env > config > default.
Sending is enabled by default. Either send.enabled=false or --no-send disables it;
--no-send=false cannot override a configured restriction. There is no sending env override.

Keys:
  account   default for --account       env MAIL_APP_CLI_ACCOUNT
  mailbox   default for --mailbox       env MAIL_APP_CLI_MAILBOX   (default INBOX)
  output    auto | json | plain          env MAIL_APP_CLI_OUTPUT    (default auto)
  send.enabled  true | false             --no-send can restrict    (default true)`,
		Annotations: map[string]string{
			annotationAgentNotes: "'config show' reports each value with its source, so you can tell a flag from a stale default.",
		},
	}
	configShowCmd := &cobra.Command{
		Use:   "show",
		Short: "Show every setting with where it came from",
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.writer.Write(output.Result{
				Data:    a.resolved,
				Summary: "config from " + a.resolved.Path,
				Plain: func(p *output.Printer) {
					p.Table([]string{"KEY", "VALUE", "SOURCE"}, a.resolved.Rows())
					p.Blank()
					p.Line("%s", p.Dim("file: "+a.resolved.Path))
				},
			})
		},
	}
	a.configSetCmd = &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a key in the config file",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.cfg.Set(args[0], args[1]); err != nil {
				return clierr.Wrap(clierr.CodeUsage, err, err.Error())
			}
			if err := config.Save(a.cfg); err != nil {
				return err
			}
			return a.writer.Write(output.Result{
				Data:    map[string]any{"key": args[0], "value": args[1], "path": a.resolved.Path},
				Summary: "Set " + args[0] + " = " + args[1],
				Plain:   renderLine("Set %s = %s", args[0], args[1]),
			})
		},
	}
	a.configUnsetCmd = &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove a key from the config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.cfg.Set(args[0], ""); err != nil {
				return clierr.Wrap(clierr.CodeUsage, err, err.Error())
			}
			if err := config.Save(a.cfg); err != nil {
				return err
			}
			return a.writer.Write(output.Result{
				Data:    map[string]any{"key": args[0], "path": a.resolved.Path},
				Summary: "Unset " + args[0],
				Plain:   renderLine("Unset %s", args[0]),
			})
		},
	}
	a.configPathCmd = &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, statErr := os.Stat(a.resolved.Path)
			return a.writer.Write(output.Result{
				Data:    map[string]any{"path": a.resolved.Path, "exists": statErr == nil},
				Summary: a.resolved.Path,
				Plain:   renderLine("%s", a.resolved.Path),
			})
		},
	}

	a.configCmd.AddCommand(configShowCmd, a.configSetCmd, a.configUnsetCmd, a.configPathCmd)
}
