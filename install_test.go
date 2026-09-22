package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerUsesNamedCommand(t *testing.T) {
	for _, explicitGOBIN := range []bool{true, false} {
		name := "GOPATH fallback"
		if explicitGOBIN {
			name = "explicit GOBIN"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "tools")
			destination := filepath.Join(root, "custom bin")
			if !explicitGOBIN {
				destination = filepath.Join(root, "gopath", "bin")
			}
			for _, dir := range []string{bin, destination} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			scripts := map[string]string{
				"uname": "#!/bin/sh\nprintf 'Darwin\\n'\n",
				"go": `#!/bin/sh
set -eu
case "$*" in
 "install github.com/0xSMW/mail.app-cli/v2/cmd/mail-app-cli@v2.test")
  /bin/cat > "$INSTALL_DEST/mail-app-cli" <<'BIN'
#!/bin/sh
printf 'mail-app-cli test-version\n'
BIN
  /bin/chmod +x "$INSTALL_DEST/mail-app-cli"
  ;;
 "env GOBIN") printf '%s' "$GOBIN" ;;
 "env GOPATH") printf '%s' "$INSTALL_GOPATH" ;;
 *) printf 'Unexpected go invocation: %s\n' "$*" >&2; exit 86 ;;
esac
`,
			}
			for name, script := range scripts {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("/bin/sh", "install.sh")
			gobin := ""
			if explicitGOBIN {
				gobin = destination
			}
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+destination, "GOBIN="+gobin, "INSTALL_DEST="+destination, "INSTALL_GOPATH="+filepath.Join(root, "gopath"), "MAIL_APP_CLI_VERSION=v2.test")
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("installer: %v: %s", err, output)
			}
			if !strings.Contains(string(output), "mail-app-cli test-version") || !strings.Contains(string(output), "Installed mail-app-cli to "+destination+"/mail-app-cli") {
				t.Fatalf("output=%s", output)
			}
			if _, err := os.Stat(filepath.Join(destination, "mail.app-cli")); !os.IsNotExist(err) {
				t.Fatalf("unexpected dotted executable: %v", err)
			}
		})
	}
}
