package cmd

import (
	"fmt"

	"github.com/0xSMW/mail.app-cli/v2/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v2/internal/output"
	"github.com/0xSMW/mail.app-cli/v2/pkg/mail"
	"github.com/spf13/cobra"
)

var smartLimit int

var smartCmd = &cobra.Command{
	Use:   "smart",
	Short: "List and query smart mailboxes",
}

func renderSmart(boxes []mail.SmartMailbox) func(*output.Printer) {
	rows := make([][]string, 0, len(boxes))
	for _, box := range boxes {
		rows = append(rows, []string{box.Name, fmt.Sprint(box.Unread), fmt.Sprint(box.TotalCount)})
	}
	return renderTable([]string{"NAME", "UNREAD", "TOTAL"}, rows, "no smart mailboxes")
}

var smartListCmd = &cobra.Command{
	Use:   "list",
	Short: "List smart mailboxes",
	RunE: func(cmd *cobra.Command, args []string) error {
		boxes, err := mailClient.ListSmartMailboxes()
		if err != nil {
			return fmt.Errorf("list smart mailboxes: %w", err)
		}
		return writer.Write(output.Result{Data: boxes, Summary: plural(len(boxes), "smart mailbox"), Plain: renderSmart(boxes)})
	},
}

var smartShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show a smart mailbox",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		boxes, err := mailClient.ListSmartMailboxes()
		if err != nil {
			return err
		}
		for _, box := range boxes {
			if box.Name == args[0] {
				return writer.Write(output.Result{Data: box, Summary: "Smart mailbox " + box.Name, Plain: renderSmart([]mail.SmartMailbox{box})})
			}
		}
		return clierr.New(clierr.CodeNotFound, "smart mailbox not found: "+args[0])
	},
}

var smartQueryCmd = &cobra.Command{
	Use:   "query <query>",
	Short: "Search across accounts with normal search semantics",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		messages, err := mailClient.SearchMessagesJSON(args[0], "", "", smartLimit)
		if err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    messages,
			Summary: fmt.Sprintf("%s for %q", plural(len(messages), "match"), args[0]),
			Plain:   renderMessages(messages, true),
		})
	},
}

func init() {
	smartCmd.AddCommand(smartListCmd, smartShowCmd, smartQueryCmd)
	smartQueryCmd.Flags().IntVarP(&smartLimit, "limit", "l", 50, "Maximum messages")
}
