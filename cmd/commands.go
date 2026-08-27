package cmd

import (
	"sort"
	"strings"

	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// commandRecord is the machine-readable description of one command.
type commandRecord struct {
	Path          string          `json:"path"`
	Use           string          `json:"use"`
	Short         string          `json:"short"`
	Long          string          `json:"long,omitempty"`
	Aliases       []string        `json:"aliases,omitempty"`
	Group         string          `json:"group,omitempty"`
	Runnable      bool            `json:"runnable"`
	Compatibility bool            `json:"compatibility,omitempty"`
	HelpTopic     bool            `json:"helpTopic,omitempty"`
	AgentNotes    string          `json:"agentNotes,omitempty"`
	Flags         []flagRecord    `json:"flags"`
	GlobalFlags   []flagRecord    `json:"globalFlags,omitempty"`
	Subcommands   []commandRecord `json:"subcommands,omitempty"`
}

type flagRecord struct {
	Name      string `json:"name"`
	Shorthand string `json:"shorthand,omitempty"`
	Type      string `json:"type"`
	Default   string `json:"default,omitempty"`
	Usage     string `json:"usage"`
	Hidden    bool   `json:"hidden,omitempty"`
}

var commandsCmd = &cobra.Command{
	Use:   "commands",
	Short: "Describe every command and flag (add --json for the full tree)",
	Annotations: map[string]string{
		annotationAgentNotes: "The authoritative list. Each record carries agentNotes and compatibility; prefer commands without compatibility=true.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		record := describeCommand(cmd.Root(), true)
		return writer.Write(output.Result{
			Data:    record,
			Summary: "command tree for mail-app-cli " + version,
			Plain: func(p *output.Printer) {
				var rows [][]string
				var walk func(r commandRecord, depth int)
				walk = func(r commandRecord, depth int) {
					if r.Path != "" {
						name := strings.Repeat("  ", depth-1) + lastWord(r.Path)
						short := r.Short
						if r.Compatibility {
							short += p.Dim(" (1.x compatibility)")
						}
						rows = append(rows, []string{name, short})
					}
					for _, sub := range r.Subcommands {
						walk(sub, depth+1)
					}
				}
				walk(record, 0)
				p.Table([]string{"COMMAND", "DESCRIPTION"}, rows)
				p.Blank()
				p.Line("%s", p.Dim("mail-app-cli commands --json for flags and agent notes"))
			},
		})
	},
}

func lastWord(path string) string {
	parts := strings.Fields(path)
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

// describeCommand builds the record for cmd and, when recurse is set, its
// subcommands. The public help and completion commands are part of the CLI
// contract; only Cobra's hidden completion protocol commands are omitted.
func describeCommand(cmd *cobra.Command, recurse bool) commandRecord {
	// Cobra adds these commands and each command's --help flag lazily. Inventory
	// output must not depend on whether another command happened to initialize
	// them first.
	cmd.Root().InitDefaultHelpCmd()
	cmd.Root().InitDefaultCompletionCmd()
	cmd.InitDefaultHelpFlag()

	record := commandRecord{
		Path:          commandPath(cmd),
		Use:           cmd.Use,
		Short:         cmd.Short,
		Long:          cmd.Long,
		Aliases:       cmd.Aliases,
		Group:         cmd.GroupID,
		Runnable:      cmd.Runnable(),
		Compatibility: cmd.Annotations[annotationCompatibility] == "true",
		HelpTopic:     cmd.Annotations[annotationHelpTopic] == "true",
		AgentNotes:    cmd.Annotations[annotationAgentNotes],
		Flags:         describeFlags(cmd.NonInheritedFlags()),
	}
	if !cmd.HasParent() {
		record.GlobalFlags = describeFlags(cmd.PersistentFlags())
		record.Flags = []flagRecord{}
	} else {
		// Agent help for a subcommand must describe the root persistent flags it
		// can accept as well as its own flags. InheritedFlags excludes locals
		// which shadow an inherited flag, keeping the two fields disjoint.
		record.GlobalFlags = describeFlags(cmd.InheritedFlags())
	}
	if !recurse {
		return record
	}
	for _, sub := range cmd.Commands() {
		if isCompletionProtocolCommand(sub) {
			continue
		}
		record.Subcommands = append(record.Subcommands, describeCommand(sub, true))
	}
	return record
}

func isCompletionProtocolCommand(cmd *cobra.Command) bool {
	return cmd.Name() == "__complete" || cmd.Name() == "__completeNoDesc"
}

func describeFlags(set *pflag.FlagSet) []flagRecord {
	flags := []flagRecord{}
	set.VisitAll(func(f *pflag.Flag) {
		if f.Name == "help" {
			return
		}
		flags = append(flags, flagRecord{
			Name:      f.Name,
			Shorthand: f.Shorthand,
			Type:      f.Value.Type(),
			Default:   defaultValue(f),
			Usage:     f.Usage,
			Hidden:    f.Hidden,
		})
	})
	sort.Slice(flags, func(i, j int) bool { return flags[i].Name < flags[j].Name })
	return flags
}

func defaultValue(f *pflag.Flag) string {
	switch f.DefValue {
	case "", "[]", "false", "0":
		return ""
	}
	return f.DefValue
}
