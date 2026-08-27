package config

import (
	"path/filepath"
	"testing"
)

func TestResolvePrecedence(t *testing.T) {
	cfg := Config{Account: "FromFile", Mailbox: "Archive", Output: "json"}
	env := map[string]string{EnvMailbox: "FromEnv"}
	getenv := func(k string) string { return env[k] }

	got := Resolve(map[string]string{KeyAccount: "FromFlag"}, cfg, getenv)
	if got.Account.Value != "FromFlag" || got.Account.Source != SourceFlag {
		t.Fatalf("account = %+v", got.Account)
	}
	if got.Mailbox.Value != "FromEnv" || got.Mailbox.Source != SourceEnv {
		t.Fatalf("mailbox = %+v", got.Mailbox)
	}
	if got.Output.Value != "json" || got.Output.Source != SourceConfig {
		t.Fatalf("output = %+v", got.Output)
	}

	got = Resolve(nil, Config{}, func(string) string { return "" })
	if got.Mailbox.Value != "INBOX" || got.Mailbox.Source != SourceDefault {
		t.Fatalf("default mailbox = %+v", got.Mailbox)
	}
	if got.Output.Value != "auto" {
		t.Fatalf("default output = %+v", got.Output)
	}
	if got.Account.Value != "" {
		t.Fatalf("default account = %+v", got.Account)
	}
}

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	t.Setenv(EnvConfigPath, path)

	cfg, err := Load()
	if err != nil || cfg != (Config{}) {
		t.Fatalf("Load on missing file = %+v, %v", cfg, err)
	}
	if err := cfg.Set(KeyAccount, "Work"); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Set(KeyOutput, "table"); err == nil {
		t.Fatal("invalid output value was accepted")
	}
	if err := cfg.Set("nope", "x"); err == nil {
		t.Fatal("unknown key was accepted")
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Account != "Work" {
		t.Fatalf("loaded = %+v", loaded)
	}
	if err := loaded.Set(KeyAccount, ""); err != nil {
		t.Fatal(err)
	}
	if loaded.Account != "" {
		t.Fatal("empty value did not clear the key")
	}
}
