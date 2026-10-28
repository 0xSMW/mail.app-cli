package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSendingRestrictionConfiguration(t *testing.T) {
	t.Setenv(EnvConfigPath, filepath.Join(t.TempDir(), "config.json"))
	cfg := Config{}
	resolve := func(flags map[string]string) Value {
		return Resolve(flags, cfg, func(string) string { return "" }).SendEnabled
	}
	if got := resolve(nil); got != (Value{"true", SourceDefault}) {
		t.Fatalf("default: %+v", got)
	}
	for _, value := range []string{"false", "true"} {
		if err := cfg.Set(KeySendEnabled, value); err != nil {
			t.Fatal(err)
		}
		if err := Save(cfg); err != nil {
			t.Fatal(err)
		}
		var err error
		cfg, err = Load()
		if err != nil {
			t.Fatal(err)
		}
		if got := resolve(nil); got != (Value{value, SourceConfig}) {
			t.Fatalf("roundtrip: %+v", got)
		}
		if got := resolve(map[string]string{KeySendEnabled: "false"}); got != (Value{"false", SourceFlag}) {
			t.Fatalf("flag: %+v", got)
		}
		if got := resolve(map[string]string{KeySendEnabled: "true"}); got.Value != value {
			t.Fatalf("flag weakened restriction: %+v", got)
		}
	}
	if err := cfg.Set(KeySendEnabled, "maybe"); err == nil {
		t.Fatal("invalid boolean accepted")
	}
	if err := cfg.Set(KeySendEnabled, ""); err != nil {
		t.Fatal(err)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	cfg, _ = Load()
	if got := resolve(nil); got != (Value{"true", SourceDefault}) {
		t.Fatalf("unset: %+v", got)
	}
	for _, raw := range []string{`{"send":{"enabled":"false"}}`, `{"send":{"enabled":0}}`, `{"send":false}`} {
		path, _ := Path()
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
}
