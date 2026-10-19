package mail

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

func TestAttachmentNamesCanonicalUnicode(t *testing.T) {
	for _, pair := range [][2]string{{"検証 résumé.bin", "検証 re\u0301sume\u0301.bin"}, {"é", "e\u0301"}} {
		if !attachmentNamesEqual(pair[0], pair[1]) || !attachmentNamesEqual(pair[1], pair[0]) {
			t.Fatalf("canonical spellings differ: %q", pair)
		}
	}
	for _, pair := range [][2]string{{"Résumé.bin", "résumé.bin"}, {"①.bin", "1.bin"}, {"resume.bin", "résumé.bin"}} {
		if attachmentNamesEqual(pair[0], pair[1]) {
			t.Fatalf("distinct names matched: %q", pair)
		}
	}
}

func TestLocalAttachmentCanonicalUnicodeSelection(t *testing.T) {
	c, root, events := localReadFixture(t)
	composed, decomposed := "検証 résumé.bin", "検証 re\u0301sume\u0301.bin"
	payload := []byte{0, 0xff, 13, 10, 0xc3, 0xa9}
	part := func(name string, data []byte) string {
		return fmt.Sprintf("Content-Type: application/octet-stream\nContent-Disposition: attachment; filename=\"%s\"\nContent-Transfer-Encoding: base64\n\n%s", name, base64.StdEncoding.EncodeToString(data))
	}
	for _, names := range [][2]string{{composed, decomposed}, {decomposed, composed}} {
		writeEMLX(t, attachmentFixturePath(root), attachmentMIME(part(names[0], payload)))
		for _, index := range []int{-1, 0} {
			path := filepath.Join(t.TempDir(), "saved.bin")
			if _, handled, err := c.saveAttachmentFromDiskSelection("Work", "INBOX", "101", names[1], index, path); !handled || err != nil {
				t.Fatalf("index %d: handled=%v err=%v", index, handled, err)
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("payload=%x err=%v", got, err)
			}
		}
	}
	writeEMLX(t, attachmentFixturePath(root), attachmentMIME(part(composed, []byte("first")), part(decomposed, payload)))
	path := filepath.Join(t.TempDir(), "ambiguous.bin")
	if _, handled, err := c.saveAttachmentFromDisk("Work", "INBOX", "101", composed, path); !handled || err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous selection: %v %v", handled, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("ambiguous selection published: %v", err)
	}
	if _, handled, err := c.saveAttachmentFromDiskSelection("Work", "INBOX", "101", composed, 1, path); !handled || err != nil {
		t.Fatalf("indexed duplicate: %v %v", handled, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("indexed payload=%x err=%v", got, err)
	}
	if events.Load() != 0 {
		t.Fatalf("cached selections sent %d Mail events", events.Load())
	}
}

func TestNativeAttachmentCanonicalUnicodeSelection(t *testing.T) {
	for _, index := range []int{-1, 0} {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			f := newNativeReadFixture()
			c := fixtureClient(t, f)
			f.properties["attc:att"]["pnam"] = appleevents.String("検証 re\u0301sume\u0301.bin")
			path := filepath.Join(t.TempDir(), "saved.bin")
			if err := c.SaveAttachmentByIndex("Personal", "INBOX", "42", "検証 résumé.bin", index, path); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != "data" {
				t.Fatalf("payload=%q err=%v", got, err)
			}
		})
	}
}

// Run the generated script in JXA with every Mail object replaced by plain
// JavaScript fixtures. This exercises the legacy engine without Mail events.
func TestLegacyAttachmentCanonicalUnicodeSelection(t *testing.T) {
	if _, err := os.Stat("/usr/bin/osascript"); err != nil {
		t.Skip("JXA unavailable")
	}
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	capture := filepath.Join(t.TempDir(), "script.js")
	t.Setenv("ATTACHMENT_SCRIPT_CAPTURE", capture)
	writeFakeOsaScript(t, `printf '%s' "$4" > "$ATTACHMENT_SCRIPT_CAPTURE"
printf 'Success\n'
`)
	for _, tc := range []struct {
		name  string
		names []string
		index int
		want  string
		saves int
	}{
		{"name", []string{"検証 re\u0301sume\u0301.bin"}, -1, "Success", 1},
		{"index", []string{"検証 re\u0301sume\u0301.bin"}, 0, "Success", 1},
		{"ambiguous", []string{"検証 re\u0301sume\u0301.bin", "検証 résumé.bin"}, -1, "ambiguous", 0},
		{"mismatch", []string{"other.bin"}, 0, "unexpected name", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			helper := `function messageById() { return {mailAttachments: function() { return fixtureAttachments; }}; }`
			if err := NewClient().saveAttachmentByIndex("Work", "INBOX", "101", "検証 résumé.bin", tc.index, "/fixture/output.bin", helper, "({})"); err != nil {
				t.Fatal(err)
			}
			script, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			namesJSON, _ := json.Marshal(tc.names)
			scriptJSON, _ := json.Marshal(string(script))
			fixture := fmt.Sprintf(`(function() { var saves = 0;
function Application() { return {accounts: {byName: function() { return {}; }}}; }
Application.currentApplication = function() { return {}; };
function Path(path) { return path; }
var fixtureAttachments = %s.map(function(name) { return {name: function() { return name; }, save: function() { saves++; }}; });
var outcome = eval(%s);
return JSON.stringify({outcome: outcome, saves: saves}); })();`, namesJSON, scriptJSON)
			out, err := exec.Command("/usr/bin/osascript", "-l", "JavaScript", "-e", fixture).CombinedOutput()
			if err != nil {
				t.Fatalf("mock JXA: %s: %v", out, err)
			}
			var got struct {
				Outcome string
				Saves   int
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("JXA output %q: %v", out, err)
			}
			if !strings.Contains(got.Outcome, tc.want) || got.Saves != tc.saves {
				t.Fatalf("result=%+v want %q and %d saves", got, tc.want, tc.saves)
			}
		})
	}
}
