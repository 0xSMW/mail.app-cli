package cmd

import (
	"fmt"
	"strings"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/pkg/mail"
	"github.com/spf13/cobra"
)

var (
	ruleDryRun     bool
	ruleQuery      string
	ruleLimit      int
	ruleFromDomain string
	ruleSubjects   []string
	ruleMoveTo     string
	ruleMarkRead   bool
	ruleEnabled    bool
)

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "List and manage Mail.app rules",
	Annotations: map[string]string{
		annotationAgentNotes: "Rules are global to Mail.app, not per account; --account on 'rules create' only picks where the target mailbox is looked up.",
	},
}

func renderRules(rules []mail.Rule) func(*output.Printer) {
	rows := make([][]string, 0, len(rules))
	for _, rule := range rules {
		enabled := "yes"
		if !rule.Enabled {
			enabled = "no"
		}
		rows = append(rows, []string{rule.Name, enabled, output.Truncate(strings.Join(rule.Conditions, "; "), 70), output.Truncate(strings.Join(rule.Actions, "; "), 40)})
	}
	return renderTable([]string{"NAME", "ENABLED", "CONDITIONS", "ACTIONS"}, rows, "no rules")
}

var rulesListCmd = &cobra.Command{
	Use:   "list",
	Short: "List rules",
	RunE: func(cmd *cobra.Command, args []string) error {
		rules, err := mailClient.ListRules()
		if err != nil {
			return fmt.Errorf("list rules: %w", err)
		}
		return writer.Write(output.Result{Data: rules, Summary: plural(len(rules), "rule"), Plain: renderRules(rules)})
	},
}

var rulesShowCmd = &cobra.Command{
	Use:   "show <rule-name>",
	Short: "Show one rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		rules, err := mailClient.ListRules()
		if err != nil {
			return err
		}
		for _, rule := range rules {
			if rule.Name == args[0] {
				return writer.Write(output.Result{Data: rule, Summary: "Rule " + rule.Name, Plain: renderRules([]mail.Rule{rule})})
			}
		}
		return clierr.New(clierr.CodeNotFound, "rule not found: "+args[0])
	},
}

var rulesEnableCmd = &cobra.Command{
	Use:   "enable <rule-name>",
	Short: "Enable a rule",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setRuleEnabled(args[0], true) },
}

var rulesDisableCmd = &cobra.Command{
	Use:   "disable <rule-name>",
	Short: "Disable a rule",
	Args:  cobra.ExactArgs(1),
	RunE:  func(cmd *cobra.Command, args []string) error { return setRuleEnabled(args[0], false) },
}

var rulesDeleteCmd = &cobra.Command{
	Use:   "delete <rule-name>",
	Short: "Delete a rule",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if ruleDryRun {
			return writer.Write(output.Result{
				Data:    map[string]any{"dryRun": true, "action": "delete", "rule": args[0]},
				Summary: "Dry run: would delete rule " + args[0],
				Plain:   renderLine("Dry run: would delete rule %q", args[0]),
			})
		}
		if err := mailClient.DeleteRule(args[0]); err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    map[string]any{"rule": args[0], "deleted": true},
			Summary: "Deleted rule " + args[0],
			Plain:   renderLine("Deleted rule %q", args[0]),
		})
	},
}

var rulesCreateCmd = &cobra.Command{
	Use:   "create <rule-name>",
	Short: "Create a rule that moves mail from a sender domain",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if ruleMoveTo == "" {
			return clierr.Usage("--move-to is required")
		}
		if ruleFromDomain == "" {
			return clierr.Usage("--from-domain is required")
		}
		input := mail.RuleInput{
			Name:            args[0],
			Account:         resolved.Account.Value,
			FromDomain:      ruleFromDomain,
			MoveTo:          ruleMoveTo,
			Enabled:         ruleEnabled,
			SubjectContains: ruleSubjects,
			MarkRead:        ruleMarkRead,
		}
		if err := mail.ValidateRuleInput(input); err != nil {
			return clierr.Wrap(clierr.CodeUsage, err, err.Error())
		}
		if ruleDryRun {
			return writer.Write(output.Result{
				Data:    map[string]any{"dryRun": true, "rule": input},
				Summary: "Dry run: would create rule " + input.Name,
				Plain:   renderLine("Dry run: would create rule %q moving %s mail to %s", input.Name, input.FromDomain, input.MoveTo),
			})
		}
		rule, err := mailClient.CreateRule(input)
		if err != nil {
			return fmt.Errorf("create rule: %w", err)
		}
		return writer.Write(output.Result{Data: rule, Summary: "Created rule " + rule.Name, Plain: renderRules([]mail.Rule{*rule})})
	},
}

var rulesApplyCmd = &cobra.Command{
	Use:   "apply <rule-name>",
	Short: "Preview only: search the mailbox in scope with --query; the rule itself is never run",
	Args:  cobra.ExactArgs(1),
	Annotations: map[string]string{
		annotationAgentNotes: "Mail.app cannot run a rule on demand. This searches with --query and labels the result with the rule name; nothing is applied.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		account, err := requireAccount()
		if err != nil {
			return err
		}
		if ruleQuery == "" {
			return clierr.Usage("--query is required for rule apply preview")
		}
		messages, err := mailClient.SearchMessagesJSON(ruleQuery, account, mailboxInScope(), ruleLimit)
		if err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    map[string]any{"rule": args[0], "dryRun": true, "matched": len(messages), "items": messages},
			Summary: fmt.Sprintf("%s would match rule %s", plural(len(messages), "message"), args[0]),
			Plain:   renderMessages(messages, false),
		})
	},
}

func setRuleEnabled(name string, enabled bool) error {
	state := "enabled"
	if !enabled {
		state = "disabled"
	}
	if ruleDryRun {
		return writer.Write(output.Result{
			Data:    map[string]any{"dryRun": true, "rule": name, "enabled": enabled},
			Summary: fmt.Sprintf("Dry run: would set rule %s %s", name, state),
			Plain:   renderLine("Dry run: would set rule %q %s", name, state),
		})
	}
	if err := mailClient.SetRuleEnabled(name, enabled); err != nil {
		return err
	}
	return writer.Write(output.Result{
		Data:    map[string]any{"rule": name, "enabled": enabled},
		Summary: fmt.Sprintf("Rule %s %s", name, state),
		Plain:   renderLine("Rule %q %s", name, state),
	})
}

func init() {
	rulesCmd.AddCommand(rulesListCmd, rulesShowCmd, rulesCreateCmd, rulesEnableCmd, rulesDisableCmd, rulesDeleteCmd, rulesApplyCmd)
	for _, cmd := range []*cobra.Command{rulesCreateCmd, rulesEnableCmd, rulesDisableCmd, rulesDeleteCmd} {
		cmd.Flags().BoolVar(&ruleDryRun, "dry-run", false, "Report what would change without touching Mail.app")
	}
	rulesApplyCmd.Flags().StringVar(&ruleQuery, "query", "", "Selector query for the preview")
	rulesApplyCmd.Flags().IntVarP(&ruleLimit, "limit", "l", 100, "Maximum matched messages")
	rulesCreateCmd.Flags().StringVar(&ruleFromDomain, "from-domain", "", "Sender domain condition")
	rulesCreateCmd.Flags().StringArrayVar(&ruleSubjects, "subject-contains", nil, "Subject text condition (repeatable)")
	rulesCreateCmd.Flags().StringVar(&ruleMoveTo, "move-to", "", "Target mailbox action")
	rulesCreateCmd.Flags().BoolVar(&ruleMarkRead, "mark-read", false, "Mark matching messages read")
	rulesCreateCmd.Flags().BoolVar(&ruleEnabled, "enabled", true, "Create the rule enabled")
}
