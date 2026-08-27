// Package skills embeds the agent-facing SKILL.md so the binary can print and
// install it without a network or a checkout.
package skills

import _ "embed"

//go:embed mail-app-cli/SKILL.md
var skillMarkdown string

// SkillMarkdown returns the embedded SKILL.md.
func SkillMarkdown() string { return skillMarkdown }
