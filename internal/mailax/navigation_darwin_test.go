//go:build darwin && cgo

package mailax

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

// Compile the real bridge into an isolated Foundation fixture. Only public AX
// boundary calls are replaced; ownership, traversal, expansion and row guards
// execute the production Objective-C implementation without contacting Mail.
func TestNativeNavigationProductionTrees(t *testing.T) {
	source, err := filepath.Abs("native_darwin.m")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "navigation-fixture")
	cmd := exec.Command("xcrun", "clang", "-fobjc-arc", "-framework", "Foundation", "-framework", "AppKit", "-framework", "ApplicationServices", "-DMAIL_AX_SOURCE="+`"`+source+`"`, "testdata/navigation_fixture.m", "-o", binary)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("compile production fixture: %v\n%s", err, out)
	}
	for _, tc := range []struct {
		name              string
		matched           bool
		errorExpected     bool
		expansions, pages int
	}{
		{"conversation", true, false, 1, 0},
		{"button-expansion", true, false, 1, 0},
		{"button-expansion-numeric", true, false, 1, 0},
		{"button-expansion-unknown", false, false, 0, 1},
		{"guard-expansion", false, true, 0, 0},
		{"guard-scroll", false, true, 0, 0},
		{"leaf-metadata", true, false, 1, 0},
		{"recycled-row", true, false, 0, 1},
		{"recycled-delayed", true, false, 0, 1},
		{"offscreen", true, false, 0, 1},
		{"reset-top", true, false, 0, 0},
		{"async-scroll", true, false, 0, 1},
		{"wrong-child", false, false, 1, 1},
		{"duplicate-subject", true, false, 0, 0},
		{"expanded-parent", false, false, 1, 1},
		{"no-progress", false, false, 0, 1},
		{"moved-expansion", false, true, 1, 0},
		{"moved-scroll", false, true, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := exec.Command(binary, tc.name).CombinedOutput()
			if err != nil {
				t.Fatalf("fixture: %v %s", err, out)
			}
			var got struct {
				Matched                       bool
				Error                         string
				Expansions, Pages, Selections int
				SelectedIDs                   []string
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode: %v %s", err, out)
			}
			if got.Matched != tc.matched || (got.Error != "") != tc.errorExpected || got.Expansions != tc.expansions || got.Pages != tc.pages {
				t.Fatalf("result: %s", out)
			}
			for _, id := range got.SelectedIDs {
				if id == "parent" {
					t.Fatalf("conversation selected: %s", out)
				}
			}
		})
	}
}
