package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0xSMW/mail.app-cli/internal/clierr"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/0xSMW/mail.app-cli/skills"
	"github.com/spf13/cobra"
)

const (
	skillDirName    = "mail-app-cli"
	skillMarkerName = ".managed-by-mail-app-cli"
	envSkillDir     = "MAIL_APP_CLI_SKILL_DIR"
)

var skillCmd = &cobra.Command{
	Use:   "skill",
	Short: "Print the embedded agent skill (SKILL.md)",
	Long: `Print the SKILL.md that teaches an agent how to use this CLI. 'skill install'
writes it to ~/.claude/skills/mail-app-cli/ so Claude Code picks it up.`,
	Annotations: map[string]string{
		annotationAgentNotes: "Raw Markdown, not an envelope. Read it once per session.",
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		_, err := fmt.Fprint(cmd.OutOrStdout(), skills.SkillMarkdown())
		return err
	},
}

var skillPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Print where 'skill install' writes",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := skillInstallDir()
		if err != nil {
			return err
		}
		_, statErr := os.Stat(filepath.Join(dir, "SKILL.md"))
		return writer.Write(output.Result{
			Data:    map[string]any{"path": filepath.Join(dir, "SKILL.md"), "installed": statErr == nil},
			Summary: filepath.Join(dir, "SKILL.md"),
			Plain:   renderLine("%s", filepath.Join(dir, "SKILL.md")),
		})
	},
}

var skillInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install the skill into ~/.claude/skills (or $MAIL_APP_CLI_SKILL_DIR)",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := skillInstallDir()
		if err != nil {
			return err
		}
		marker := filepath.Join(dir, skillMarkerName)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			if _, err := os.Stat(marker); err != nil {
				return clierr.New(clierr.CodeUsage, fmt.Sprintf("%s exists and was not written by mail-app-cli", dir)).
					WithHint("remove it, or set " + envSkillDir + " to another directory")
			}
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		target := filepath.Join(dir, "SKILL.md")
		if err := os.WriteFile(target, []byte(skills.SkillMarkdown()), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(marker, []byte("version "+version+"\n"), 0o644); err != nil {
			return err
		}
		return writer.Write(output.Result{
			Data:    map[string]any{"path": target, "version": version, "installed": true},
			Summary: "Installed skill to " + target,
			Plain:   renderLine("Installed skill to %s", target),
		})
	},
}

func skillInstallDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv(envSkillDir)); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills", skillDirName), nil
}

func init() {
	skillCmd.AddCommand(skillPathCmd, skillInstallCmd)
}
