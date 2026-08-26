package cmd

import (
	"bytes"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// TestSurfaceSnapshot fails when a command or flag appears or disappears
// without .surface being regenerated. Rewrite it with:
//
//	UPDATE_SURFACE=1 go test ./cmd -run TestSurfaceSnapshot
func TestSurfaceSnapshot(t *testing.T) {
	var lines []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Name() == "help" || cmd.Name() == "completion" {
			return
		}
		path := commandPath(cmd)
		if path == "" {
			path = "(root)"
		}
		lines = append(lines, path)
		flags := cmd.NonInheritedFlags()
		if !cmd.HasParent() {
			flags = cmd.PersistentFlags()
		}
		flags.VisitAll(func(f *pflag.Flag) {
			if f.Name == "help" {
				return
			}
			entry := path + " --" + f.Name
			if f.Shorthand != "" {
				entry += " (-" + f.Shorthand + ")"
			}
			if f.Hidden {
				entry += " [hidden]"
			}
			lines = append(lines, entry)
		})
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	sort.Strings(lines)
	want := strings.Join(lines, "\n") + "\n"

	const path = "../.surface"
	if os.Getenv("UPDATE_SURFACE") != "" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with UPDATE_SURFACE=1 to create it)", path, err)
	}
	if !bytes.Equal(got, []byte(want)) {
		t.Fatalf("command surface changed; review the diff and run UPDATE_SURFACE=1 go test ./cmd -run TestSurfaceSnapshot\n--- .surface\n+++ current\n%s", diffLines(string(got), want))
	}
}

func diffLines(a, b string) string {
	as := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(a), "\n") {
		as[l] = true
	}
	bs := map[string]bool{}
	for _, l := range strings.Split(strings.TrimSpace(b), "\n") {
		bs[l] = true
	}
	var out []string
	for l := range as {
		if !bs[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range bs {
		if !as[l] {
			out = append(out, "+ "+l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}
