package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/config"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/spf13/cobra"
)

type terminalBuffer struct{ bytes.Buffer }

func (terminalBuffer) Stat() (os.FileInfo, error) { return terminalFileInfo{}, nil }

type terminalFileInfo struct{}

func (terminalFileInfo) Name() string       { return "terminal" }
func (terminalFileInfo) Size() int64        { return 0 }
func (terminalFileInfo) Mode() os.FileMode  { return os.ModeCharDevice }
func (terminalFileInfo) ModTime() time.Time { return time.Time{} }
func (terminalFileInfo) IsDir() bool        { return false }
func (terminalFileInfo) Sys() any           { return nil }

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
	t.Setenv(config.EnvAccount, "")
	t.Setenv(config.EnvMailbox, "")
	t.Setenv(config.EnvOutput, "")
	var stdout, stderr bytes.Buffer
	code := Run(args, &stdout, &stderr)
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

func TestRunConcurrentInvocationsKeepCommandsAndWritersIndependent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.EnvConfigPath, filepath.Join(home, "config.json"))
	t.Setenv(config.EnvAccount, "")
	t.Setenv(config.EnvMailbox, "")
	t.Setenv(config.EnvOutput, "")
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_METRICS_FILE", "")

	cases := []struct {
		name                           string
		args                           []string
		code                           int
		stdoutContains, stderrContains string
		stdoutJSON, stderrJSON         bool
	}{
		{name: "JSON version", args: []string{"version", "--json"}, stdoutContains: `"version": "` + version + `"`, stdoutJSON: true},
		{name: "plain version", args: []string{"version", "--plain", "--no-color"}, stdoutContains: "mail-app-cli " + version},
		{name: "JSON parse error", args: []string{"concurrent-unknown", "--json"}, code: 1, stderrContains: `"code": "usage"`, stderrJSON: true},
		{name: "plain parse error", args: []string{"concurrent-unknown", "--plain"}, code: 1, stderrContains: `unknown command "concurrent-unknown"`},
		{name: "agent help", args: []string{"version", "--agent", "--help"}, stdoutContains: `"path": "version"`, stdoutJSON: true},
		{name: "human help", args: []string{"help", "version"}, stdoutContains: "Print the version"},
	}
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		for _, tc := range cases {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				var stdout, stderr bytes.Buffer
				code := Run(tc.args, &stdout, &stderr)
				if code != tc.code {
					t.Errorf("%s: exit = %d, want %d; stdout = %q, stderr = %q", tc.name, code, tc.code, stdout.String(), stderr.String())
				}
				if tc.stdoutContains == "" {
					if stdout.Len() != 0 {
						t.Errorf("%s: unexpected stdout = %q", tc.name, stdout.String())
					}
				} else if !strings.Contains(stdout.String(), tc.stdoutContains) {
					t.Errorf("%s: stdout = %q, want %q", tc.name, stdout.String(), tc.stdoutContains)
				}
				if tc.stderrContains == "" {
					if stderr.Len() != 0 {
						t.Errorf("%s: unexpected stderr = %q", tc.name, stderr.String())
					}
				} else if !strings.Contains(stderr.String(), tc.stderrContains) {
					t.Errorf("%s: stderr = %q, want %q", tc.name, stderr.String(), tc.stderrContains)
				}
				if tc.stdoutJSON && !json.Valid(stdout.Bytes()) {
					t.Errorf("%s: invalid JSON stdout = %q", tc.name, stdout.String())
				}
				if tc.stderrJSON && !json.Valid(stderr.Bytes()) {
					t.Errorf("%s: invalid JSON stderr = %q", tc.name, stderr.String())
				}
			}()
		}
	}
	close(start)
	workers.Wait()
}

func TestRunDerivesAutomaticOutputFromSuppliedStdout(t *testing.T) {
	a := newApplication()

	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
	t.Setenv(config.EnvOutput, "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	var terminalStdout terminalBuffer
	var stderr bytes.Buffer
	if code := a.run([]string{"version"}, &terminalStdout, &stderr); code != 0 {
		t.Fatalf("terminal version exit = %d: %s", code, stderr.String())
	}
	if !strings.HasPrefix(terminalStdout.String(), "mail-app-cli "+version) {
		t.Fatalf("terminal stdout = %q, want plain version", terminalStdout.String())
	}
	if a.writer == nil || a.writer.Format != output.FormatPlain || !a.writer.Color {
		t.Fatalf("terminal writer = %#v, want plain color writer", a.writer)
	}

	a = newApplication()
	var bufferedStdout bytes.Buffer
	stderr.Reset()
	if code := a.run([]string{"version"}, &bufferedStdout, &stderr); code != 0 {
		t.Fatalf("buffered version exit = %d: %s", code, stderr.String())
	}
	var env output.Envelope
	if err := json.Unmarshal(bufferedStdout.Bytes(), &env); err != nil || !env.OK {
		t.Fatalf("buffered stdout = %q, unmarshal error = %v", bufferedStdout.String(), err)
	}
	if a.writer == nil || a.writer.Format != output.FormatJSON || a.writer.Color {
		t.Fatalf("buffered writer = %#v, want JSON non-color writer", a.writer)
	}
}

func TestRunParseErrorDerivesFallbackOutputFromSuppliedStdout(t *testing.T) {
	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
	t.Setenv(config.EnvOutput, "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	var terminalStdout terminalBuffer
	var terminalStderr bytes.Buffer
	if code := Run([]string{"not-a-command"}, &terminalStdout, &terminalStderr); code != 1 {
		t.Fatalf("terminal parse error exit = %d: %s", code, terminalStderr.String())
	}
	if !strings.Contains(terminalStderr.String(), "mail-app-cli:") || strings.HasPrefix(strings.TrimSpace(terminalStderr.String()), "{") {
		t.Fatalf("terminal parse error = %q, want plain error", terminalStderr.String())
	}

	var bufferedStdout bytes.Buffer
	var bufferedStderr bytes.Buffer
	if code := Run([]string{"not-a-command"}, &bufferedStdout, &bufferedStderr); code != 1 {
		t.Fatalf("buffered parse error exit = %d: %s", code, bufferedStderr.String())
	}
	var env output.ErrorEnvelope
	if err := json.Unmarshal(bufferedStderr.Bytes(), &env); err != nil || env.Code != "usage" {
		t.Fatalf("buffered parse error = %q, unmarshal error = %v", bufferedStderr.String(), err)
	}
}

func TestRunPreRunErrorResolvesConfiguredFallbackOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfigPath, path)
	t.Setenv(config.EnvOutput, "")

	writeConfig := func(outputValue string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(`{"output":"`+outputValue+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeConfig("json")
	var terminalStdout terminalBuffer
	var terminalStderr bytes.Buffer
	if code := Run([]string{"config", "set", "account"}, &terminalStdout, &terminalStderr); code != 1 {
		t.Fatalf("configured JSON exit = %d: %s", code, terminalStderr.String())
	}
	var jsonError output.ErrorEnvelope
	if err := json.Unmarshal(terminalStderr.Bytes(), &jsonError); err != nil || jsonError.Code != "usage" {
		t.Fatalf("configured JSON error = %q, unmarshal error = %v", terminalStderr.String(), err)
	}

	writeConfig("plain")
	var bufferedStdout bytes.Buffer
	var bufferedStderr bytes.Buffer
	if code := Run([]string{"config", "set", "account"}, &bufferedStdout, &bufferedStderr); code != 1 {
		t.Fatalf("configured plain exit = %d: %s", code, bufferedStderr.String())
	}
	if strings.HasPrefix(strings.TrimSpace(bufferedStderr.String()), "{") || !strings.Contains(bufferedStderr.String(), "mail-app-cli:") {
		t.Fatalf("configured plain error = %q, want plain error", bufferedStderr.String())
	}
}

func TestRunPreRunErrorFallbackOutputPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"output":"plain"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, path)
	t.Setenv(config.EnvOutput, "json")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run([]string{"config", "set", "account"}, &stdout, &stderr); code != 1 {
		t.Fatalf("environment JSON exit = %d: %s", code, stderr.String())
	}
	var envError output.ErrorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &envError); err != nil || envError.Code != "usage" {
		t.Fatalf("environment JSON error = %q, unmarshal error = %v", stderr.String(), err)
	}

	stderr.Reset()
	if code := Run([]string{"config", "set", "account", "--plain"}, &stdout, &stderr); code != 1 {
		t.Fatalf("flag plain exit = %d: %s", code, stderr.String())
	}
	if strings.HasPrefix(strings.TrimSpace(stderr.String()), "{") || !strings.Contains(stderr.String(), "mail-app-cli:") {
		t.Fatalf("flag plain error = %q, want plain error", stderr.String())
	}
}

func TestRunPreRunErrorDoesNotHideMalformedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, path)
	t.Setenv(config.EnvOutput, "")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run([]string{"config", "set", "account"}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "accepts 2 arg(s), received 1") || strings.Contains(stderr.String(), "parse config") {
		t.Fatalf("error = %q, want original argument error", stderr.String())
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
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"config", "set", "account", "Work", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("set exit = %d: %s", code, stderr.String())
	}
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
	stdout.Reset()
	if code := Run([]string{"config", "set", "output", "table", "--json"}, &stdout, &stderr); code != 1 {
		t.Fatalf("invalid output value exit = %d", code)
	}
}

func TestConfigPathWorksWithMalformedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvConfigPath, path)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"config", "path", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr.String())
	}
	var env struct {
		Data struct {
			Path   string `json:"path"`
			Exists bool   `json:"exists"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("stdout is not an envelope: %v\n%s", err, stdout.String())
	}
	if env.Data.Path != path || !env.Data.Exists {
		t.Fatalf("config path data = %+v, want existing %q", env.Data, path)
	}
}

func TestRunResetsFlagsBetweenInvocations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfigPath, path)
	t.Setenv(config.EnvAccount, "")
	t.Setenv(config.EnvMailbox, "")
	t.Setenv(config.EnvOutput, "")

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"config", "show", "--json", "--account", "Work", "--mailbox", "Archive"}, &stdout, &stderr); code != 0 {
		t.Fatalf("first exit = %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"config", "show", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("second exit = %d: %s", code, stderr.String())
	}
	var env struct {
		Data config.Resolved `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("second stdout is not an envelope: %v\n%s", err, stdout.String())
	}
	if env.Data.Account.Source != config.SourceDefault || env.Data.Account.Value != "" {
		t.Fatalf("account leaked from prior invocation: %+v", env.Data.Account)
	}
	if env.Data.Mailbox.Source != config.SourceDefault || env.Data.Mailbox.Value != "INBOX" {
		t.Fatalf("mailbox leaked from prior invocation: %+v", env.Data.Mailbox)
	}
}

func TestRunResetsFlagsAfterError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"version", "--json", "--plain"}, &stdout, &stderr); code != 1 {
		t.Fatalf("error exit = %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"version", "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("next exit = %d: %s", code, stderr.String())
	}
	var env struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil || !env.OK {
		t.Fatalf("next stdout = %q, unmarshal error = %v", stdout.String(), err)
	}
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
	for _, want := range []string{"inbox", "show", "archive", "messages list", "messages batch archive", "config set", "skill install", "help", "completion", "completion bash"} {
		if _, ok := paths[want]; !ok {
			t.Fatalf("command %q missing from tree", want)
		}
	}
	for _, protocol := range []string{"__complete", "__completeNoDesc"} {
		if _, ok := paths[protocol]; ok {
			t.Fatalf("hidden completion protocol command %q present in tree", protocol)
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
	assertHelpFlag := func(t *testing.T, record commandRecord) {
		t.Helper()
		for _, f := range record.Flags {
			if f.Name == "help" {
				if f.Shorthand != "h" {
					t.Fatalf("%s --help shorthand = %q, want h", record.Path, f.Shorthand)
				}
				return
			}
		}
		t.Fatalf("%s flags missing --help: %+v", record.Path, record.Flags)
	}
	assertNoDuplicateFlags := func(t *testing.T, record commandRecord) {
		t.Helper()
		flags := map[string]bool{}
		for _, f := range record.Flags {
			if flags[f.Name] {
				t.Fatalf("%s repeats --%s in flags", record.Path, f.Name)
			}
			flags[f.Name] = true
		}
		for _, f := range record.GlobalFlags {
			if flags[f.Name] {
				t.Fatalf("%s repeats --%s in flags and globalFlags", record.Path, f.Name)
			}
			flags[f.Name] = true
		}
	}
	for _, path := range []string{"", "archive", "completion", "completion bash", "help"} {
		assertHelpFlag(t, paths[path])
		assertNoDuplicateFlags(t, paths[path])
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
	assertHelpFlag(t, record)
	global := map[string]bool{}
	for _, f := range record.GlobalFlags {
		global[f.Name] = true
	}
	for _, want := range []string{"account", "mailbox", "json", "jq", "ids-only", "count"} {
		if !global[want] {
			t.Fatalf("archive agent help globals missing --%s: %+v", want, record.GlobalFlags)
		}
	}
	assertNoDuplicateFlags(t, record)
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
	if code != 1 || stdout != "" || !strings.Contains(stderr, "--count only applies") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, "version", "--ids-only")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "--ids-only only applies") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
	code, stdout, stderr = run(t, "version", "--jq", ".data[")
	if code != 1 || stdout != "" || !strings.Contains(stderr, "invalid --jq") {
		t.Fatalf("bad jq: exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
}

func TestJQRuntimeFailureStopsAnnotatedMutationBeforeRunE(t *testing.T) {
	a := newApplication()

	var jqOutput bytes.Buffer
	jqWriter, err := output.New(output.FormatJSON, &jqOutput, io.Discard, false, "1 / 0", "test", 1)
	if err != nil {
		t.Fatalf("compile jq expression: %v", err)
	}
	if err := jqWriter.Write(output.Result{Data: map[string]any{"receipt": true}}); err == nil {
		t.Fatal("1 / 0 did not fail when jq evaluated it")
	}

	ran := false
	mutation := &cobra.Command{
		Use:         "jq-mutation-preflight-test",
		Args:        cobra.NoArgs,
		Annotations: map[string]string{annotationMutation: "true"},
		RunE: func(*cobra.Command, []string) error {
			ran = true
			return nil
		},
	}
	a.rootCmd.AddCommand(mutation)
	t.Cleanup(func() { a.rootCmd.RemoveCommand(mutation) })

	t.Setenv(config.EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
	var stdoutBuffer, stderrBuffer bytes.Buffer
	code := a.run([]string{"jq-mutation-preflight-test", "--jq", "1 / 0"}, &stdoutBuffer, &stderrBuffer)
	stdout, stderr := stdoutBuffer.String(), stderrBuffer.String()
	if code != 1 || stdout != "" || !strings.Contains(stderr, "--jq cannot be combined with a command that changes state") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %s", code, stdout, stderr)
	}
	if ran {
		t.Fatal("mutation RunE ran before the jq runtime failure was rejected")
	}
}

func TestStateChangingCommandsRejectJQBeforeRun(t *testing.T) {
	a := newApplication()

	for _, cmd := range []*cobra.Command{
		a.archiveCmd, a.sendCmd, a.attachmentsSaveCmd, a.configSetCmd, a.draftsCreateCmd,
		a.exportAttachmentsCmd, a.messagesBatchArchiveCmd, a.recentClearCmd,
		a.skillInstallCmd, a.syncCmd, a.threadsArchiveCmd,
	} {
		if cmd.Annotations[annotationMutation] != "true" {
			t.Fatalf("%s is not annotated as a mutation", cmd.CommandPath())
		}
	}
	if a.exportMessagesCmd.Annotations[annotationFileOutputMutation] != "true" {
		t.Fatalf("%s is not annotated as a file-output mutation", a.exportMessagesCmd.CommandPath())
	}
}

func TestReadOnlyPreviewsAllowJQ(t *testing.T) {
	a := newApplication()

	for _, cmd := range []*cobra.Command{a.importMessagesCmd, a.rulesApplyCmd} {
		if cmd.Annotations[annotationMutation] == "true" {
			t.Fatalf("%s is incorrectly annotated as a mutation", cmd.CommandPath())
		}
	}

	fixture := filepath.Join(t.TempDir(), "messages.json")
	if err := os.WriteFile(fixture, []byte(`[{"id":"42"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run(t, "import", "messages", "--account", "Work", "--file", fixture, "--jq", ".data.validated")
	if code != 0 || stdout != "1\n" || stderr != "" {
		t.Fatalf("import jq exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "osascript.log")
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	t.Setenv("MAIL_APP_CLI_TEST_OSASCRIPT_LOG", logPath)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	if err := os.WriteFile(filepath.Join(binDir, "osascript"), []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$MAIL_APP_CLI_TEST_OSASCRIPT_LOG"
case "$*" in
  *"const accounts = mail.accounts()"*) printf '%s' '[{"id":"work","name":"Work","enabled":true}]' ;;
  *) printf '%s' '[{"id":"42","subject":"Receipt","sender":"billing@example.com","dateReceived":"2026-08-27T00:00:00Z","dateSent":"2026-08-27T00:00:00Z","read":false,"flagged":false,"messageSize":1,"mailbox":"INBOX","account":"Work"}]' ;;
esac
`), 0o755); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = run(t, "rules", "apply", "receipt-rule", "--account", "Work", "--mailbox", "INBOX", "--query", "receipt", "--jq", ".data.matched")
	if code != 0 || stdout != "1\n" || stderr != "" {
		t.Fatalf("rules apply jq exit = %d, stdout = %q, stderr = %q", code, stdout, stderr)
	}
	invocation, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(invocation), "const messages = mbox.messages();") {
		t.Fatalf("rules apply did not reach its read-only search: %s", invocation)
	}
}

func TestListOutputEligibilityAnnotations(t *testing.T) {
	a := newApplication()

	tests := []struct {
		path   []string
		idList bool
	}{
		{path: []string{"inbox"}, idList: true},
		{path: []string{"search"}, idList: true},
		{path: []string{"accounts", "list"}, idList: true},
		{path: []string{"mailboxes", "list"}},
		{path: []string{"messages", "list"}, idList: true},
		{path: []string{"attachments", "list"}},
		{path: []string{"drafts", "list"}, idList: true},
		{path: []string{"rules", "list"}},
		{path: []string{"smart", "list"}},
		{path: []string{"smart", "query"}, idList: true},
		{path: []string{"signatures", "list"}},
		{path: []string{"threads", "list"}, idList: true},
		{path: []string{"recent", "search"}, idList: true},
		{path: []string{"export", "attachments"}},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.path, " "), func(t *testing.T) {
			cmd, _, err := a.rootCmd.Find(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Annotations[annotationList] != "true" {
				t.Fatalf("%s does not accept --count", cmd.CommandPath())
			}
			if got := cmd.Annotations[annotationIDList] == "true"; got != tt.idList {
				t.Fatalf("%s ids-only eligibility = %t, want %t", cmd.CommandPath(), got, tt.idList)
			}
		})
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
