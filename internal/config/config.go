// Package config reads and writes the user configuration file and resolves
// each setting from flag, environment, file, or default in that order.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	KeyAccount = "account"
	KeyMailbox = "mailbox"
	KeyOutput  = "output"

	EnvConfigPath = "MAIL_APP_CLI_CONFIG"
	EnvAccount    = "MAIL_APP_CLI_ACCOUNT"
	EnvMailbox    = "MAIL_APP_CLI_MAILBOX"
	EnvOutput     = "MAIL_APP_CLI_OUTPUT"

	SourceFlag    = "flag"
	SourceEnv     = "env"
	SourceConfig  = "config"
	SourceDefault = "default"
)

// Config is the on-disk shape. Empty values are omitted so the file only
// records what the user set.
type Config struct {
	Account string `json:"account,omitempty"`
	Mailbox string `json:"mailbox,omitempty"`
	Output  string `json:"output,omitempty"`
}

// Keys lists the settable keys in display order.
func Keys() []string {
	return []string{KeyAccount, KeyMailbox, KeyOutput}
}

var outputValues = []string{"auto", "json", "plain"}

// Path returns the config file location: $MAIL_APP_CLI_CONFIG, else
// $XDG_CONFIG_HOME/mail-app-cli/config.json, else ~/.config/mail-app-cli/config.json.
func Path() (string, error) {
	if override := strings.TrimSpace(os.Getenv(EnvConfigPath)); override != "" {
		return override, nil
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "mail-app-cli", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	return filepath.Join(home, ".config", "mail-app-cli", "config.json"), nil
}

// Load reads the config file. A missing file is an empty config, not an error.
func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the config file, creating its directory.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

// Get returns the value stored for key.
func (c Config) Get(key string) (string, error) {
	switch key {
	case KeyAccount:
		return c.Account, nil
	case KeyMailbox:
		return c.Mailbox, nil
	case KeyOutput:
		return c.Output, nil
	}
	return "", unknownKey(key)
}

// Set validates and stores a value. An empty value clears the key.
func (c *Config) Set(key, value string) error {
	value = strings.TrimSpace(value)
	switch key {
	case KeyAccount:
		c.Account = value
	case KeyMailbox:
		c.Mailbox = value
	case KeyOutput:
		if value != "" && !validOutput(value) {
			return fmt.Errorf("output must be one of %s", strings.Join(outputValues, ", "))
		}
		c.Output = value
	default:
		return unknownKey(key)
	}
	return nil
}

func unknownKey(key string) error {
	return fmt.Errorf("unknown config key %q; valid keys: %s", key, strings.Join(Keys(), ", "))
}

func validOutput(value string) bool {
	for _, candidate := range outputValues {
		if value == candidate {
			return true
		}
	}
	return false
}

// Value is a resolved setting with where it came from.
type Value struct {
	Value  string `json:"value"`
	Source string `json:"source"`
}

// Resolved holds every setting after precedence is applied.
type Resolved struct {
	Account Value  `json:"account"`
	Mailbox Value  `json:"mailbox"`
	Output  Value  `json:"output"`
	Path    string `json:"path"`
}

// Resolve applies flag > env > config > default for each key. flags holds
// only the flags the user explicitly set.
func Resolve(flags map[string]string, cfg Config, getenv func(string) string) Resolved {
	resolve := func(key, envName, fileValue, fallback string) Value {
		if v, ok := flags[key]; ok && strings.TrimSpace(v) != "" {
			return Value{Value: v, Source: SourceFlag}
		}
		if v := strings.TrimSpace(getenv(envName)); v != "" {
			return Value{Value: v, Source: SourceEnv}
		}
		if strings.TrimSpace(fileValue) != "" {
			return Value{Value: fileValue, Source: SourceConfig}
		}
		return Value{Value: fallback, Source: SourceDefault}
	}
	path, _ := Path()
	return Resolved{
		Account: resolve(KeyAccount, EnvAccount, cfg.Account, ""),
		Mailbox: resolve(KeyMailbox, EnvMailbox, cfg.Mailbox, "INBOX"),
		Output:  resolve(KeyOutput, EnvOutput, cfg.Output, "auto"),
		Path:    path,
	}
}

// Rows renders the resolved settings as key, value, source triples in key order.
func (r Resolved) Rows() [][]string {
	rows := [][]string{
		{KeyAccount, r.Account.Value, r.Account.Source},
		{KeyMailbox, r.Mailbox.Value, r.Mailbox.Source},
		{KeyOutput, r.Output.Value, r.Output.Source},
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	return rows
}
