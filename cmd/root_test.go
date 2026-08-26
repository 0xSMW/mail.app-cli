package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/internal/config"
	"github.com/0xSMW/mail.app-cli/internal/output"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// resetFlags returns every flag on the tree to its default so one test's
// arguments do not leak into the next.
func resetFlags(cmd *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		if slice, ok := f.Value.(pflag.SliceValue); ok {
			_ = slice.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	cmd.PersistentFlags().VisitAll(reset)
	cmd.Flags().VisitAll(reset)
	for _, sub := range cmd.Commands() {
		resetFlags(sub)
	}
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
	t.Setenv(config.EnvAccount, "")
	t.Setenv(config.EnvMailbox, "")
	t.Setenv(config.EnvOutput, "")
	resetFlags(rootCmd)
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
	resetFlags(rootCmd)
	return code, stdout.String(), stderr.String()
}

func TestUnknownCommandIsUsageError(t *testing.T) {
	code, _, stderr := run(t, "nonsense", "--json")
	if code != 1 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	var env output.ErrorEnvelope
	if err := json.Unmarshal([]byte(stderr), &env); err != nil {
		t.Fatalf("stderr is not an error envelope: %v\n%s", err, stderr)
	}
	if env.OK || env.Code != "usage" || env.ExitCode != 1 {
		t.Fatalf("envelope = %+v", env)
	}
}

func TestPlainAndJSONConflict(t *testing.T) {
	code, _, stderr := run(t, "version", "--plain", "--json")
	if code != 1 || !strings.Contains(stderr, "--plain cannot be combined") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
}

func TestVersionEnvelope(t *testing.T) {
	code, stdout, stderr := run(t, "version", "--json")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Version string `json:"version"`
		} `json:"data"`
		Meta map[string]any `json:"meta"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout)
	}
	if !env.OK || env.Data.Version != version || env.Meta["command"] != "version" {
		t.Fatalf("envelope = %+v", env)
	}
	code, stdout, _ = run(t, "version", "--quiet", "--jq", ".version")
	if code != 0 || strings.TrimSpace(stdout) != version {
		t.Fatalf("quiet jq = %q (exit %d)", stdout, code)
	}
	code, stdout, _ = run(t, "version", "--plain")
	if code != 0 || !strings.HasPrefix(stdout, "mail-app-cli "+version) {
		t.Fatalf("plain = %q", stdout)
	}
}

func TestConfigRoundTripAndPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfigPath, path)
	resetFlags(rootCmd)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"config", "set", "account", "Work", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("set exit = %d: %s", code, stderr.String())
	}
	resetFlags(rootCmd)
	stdout.Reset()
	if code := Run([]string{"config", "show", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("show exit = %d: %s", code, stderr.String())
	}
	var env struct {
		Data config.Resolved `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Account.Value != "Work" || env.Data.Account.Source != config.SourceConfig {
		t.Fatalf("account = %+v", env.Data.Account)
	}
	if env.Data.Mailbox.Value != "INBOX" || env.Data.Mailbox.Source != config.SourceDefault {
		t.Fatalf("mailbox = %+v", env.Data.Mailbox)
	}
	resetFlags(rootCmd)
	stdout.Reset()
	if code := Run([]string{"config", "show", "--json", "-a", "Other", "-m", "Archive"}, &stdout, &stderr); code != 0 {
		t.Fatalf("show with flags exit = %d: %s", code, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.Account.Value != "Other" || env.Data.Account.Source != config.SourceFlag || env.Data.Mailbox.Value != "Archive" {
		t.Fatalf("flag precedence = %+v", env.Data)
	}
	resetFlags(rootCmd)
	stdout.Reset()
	if code := Run([]string{"config", "set", "output", "table", "--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("invalid output value exit = %d", code)
	}
	resetFlags(rootCmd)
}

func TestCommandsTreeAndAgentHelp(t *testing.T) {
	code, stdout, stderr := run(t, "commands", "--json")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	var env struct {
		Data commandRecord `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &env); err != nil {
		t.Fatal(err)
	}
	paths := map[string]commandRecord{}
	var walk func(r commandRecord)
	walk = func(r commandRecord) {
		paths[r.Path] = r
		for _, s := range r.Subcommands {
			walk(s)
		}
	}
	walk(env.Data)
	for _, want := range []string{"inbox", "show", "archive", "messages list", "messages batch archive", "config set", "skill install"} {
		if _, ok := paths[want]; !ok {
			t.Fatalf("command %q missing from tree", want)
		}
	}
	if !paths["messages archive"].Compatibility || paths["messages list"].Compatibility {
		t.Fatal("compatibility annotation is on the wrong messages commands")
	}
	if paths["search"].AgentNotes == "" {
		t.Fatal("search has no agent notes")
	}
	hasGlobal := false
	for _, f := range env.Data.GlobalFlags {
		if f.Name == "jq" {
			hasGlobal = true
		}
	}
	if !hasGlobal {
		t.Fatal("global flags missing --jq")
	}

	code, stdout, stderr = run(t, "archive", "--agent", "--help")
	if code != 0 {
		t.Fatalf("agent help exit = %d: %s", code, stderr)
	}
	var record commandRecord
	if err := json.Unmarshal([]byte(stdout), &record); err != nil {
		t.Fatalf("agent help is not JSON: %v\n%s", err, stdout)
	}
	if record.Path != "archive" || len(record.Flags) == 0 {
		t.Fatalf("agent help record = %+v", record)
	}
}

func TestHelpTopicsPrint(t *testing.T) {
	code, stdout, _ := run(t, "help", "exit-codes")
	if code != 0 || !strings.Contains(stdout, "mutation_failed") {
		t.Fatalf("help exit-codes = %q", stdout)
	}
}

func TestSkillPrintsMarkdown(t *testing.T) {
	code, stdout, _ := run(t, "skill")
	if code != 0 || !strings.HasPrefix(stdout, "---\nname: mail-app-cli") {
		t.Fatalf("skill = %q", stdout[:min(len(stdout), 80)])
	}
	dir := filepath.Join(t.TempDir(), "skill")
	t.Setenv(envSkillDir, dir)
	code, _, stderr := run(t, "skill", "install", "--json")
	if code != 0 {
		t.Fatalf("install exit = %d: %s", code, stderr)
	}
	code, _, _ = run(t, "skill", "install", "--json")
	if code != 0 {
		t.Fatal("reinstall over a managed directory should succeed")
	}
}

func TestCountRefusedBeforeRunOnNonList(t *testing.T) {
	code, stdout, stderr := run(t, "version", "--count")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "--ids-only and --count") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, "version", "--jq", ".data[")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "invalid --jq") {
		t.Fatalf("bad jq: exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
}

func TestUnknownHelpTopicIsUsageError(t *testing.T) {
	code, _, stderr := run(t, "help", "nonsense")
	if code != 1 || !strings.Contains(stderr, "unknown help topic") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	code, stdout, _ := run(t, "help", "config", "set")
	if code != 0 || !strings.Contains(stdout, "Set a key") {
		t.Fatalf("help config set: exit = %d, stdout = %q", code, stdout)
	}
}

func TestCommandsHaveNoNullLists(t *testing.T) {
	_, stdout, _ := run(t, "commands", "--json")
	if strings.Contains(stdout, ": null") {
		t.Fatalf("commands --json contains null: %s", stdout[:200])
	}
}

func TestNonNumericIDIsUsageError(t *testing.T) {
	code, _, stderr := run(t, "seen", "abc", "--json")
	if code != 1 || !strings.Contains(stderr, "not numeric") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
}

func TestMoveRequiresTo(t *testing.T) {
	code, _, stderr := run(t, "move", "1", "--json")
	if code != 1 || !strings.Contains(stderr, "required flag") {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
}
