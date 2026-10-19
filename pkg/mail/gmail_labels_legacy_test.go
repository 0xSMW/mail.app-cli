package mail

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGmailLabelLegacyBackendCommands(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required to execute the production legacy account projection")
	}
	for _, command := range []string{"list", "show", "add", "remove", "reconcile", "reject POP", "reject Exchange", "reject unknown"} {
		t.Run(command, func(t *testing.T) {
			w, provider, request := labelWorkflowFixture(t)
			// Seed reconciliation through the independent fixture account lookup,
			// so its regression exercises a fresh legacy lookup after dispatch.
			if command == "reconcile" {
				if _, err := w.Change(context.Background(), request); err != nil {
					t.Fatal(err)
				}
			}
			accountType := "IMAP"
			reject := strings.HasPrefix(command, "reject ")
			if reject {
				accountType = strings.TrimPrefix(command, "reject ")
			}
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
			t.Setenv("LABEL_ACCOUNT_TYPE", accountType)
			mock := filepath.Join(t.TempDir(), "accounts.js")
			if err := os.WriteFile(mock, []byte(`
const account = {
  id: () => 'work', name: () => 'Work', emailAddresses: () => ['me@example.com'],
  userName: () => 'me', enabled: () => true, accountType: () => process.env.LABEL_ACCOUNT_TYPE
};
const Application = () => ({accounts: () => [account]});
process.stdout.write(eval(process.argv[process.argv.length - 1]));
`), 0o600); err != nil {
				t.Fatal(err)
			}
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			writeFakeOsaScript(t, "exec "+quote(node)+" "+quote(mock)+" \"$@\"\n")
			client := NewClient()
			w.account = NewGmailLabelWorkflow(client, provider).account
			var result GmailLabelResult
			var err error
			switch command {
			case "list", "reject POP", "reject Exchange", "reject unknown":
				labels, listErr := w.List(context.Background(), request.Account)
				err = listErr
				if !reject && err == nil && len(labels) != len(provider.labels) {
					t.Fatalf("labels = %v, want provider catalog", labels)
				}
			case "show":
				result, err = w.Show(context.Background(), request)
			case "add", "remove":
				if command == "add" {
					request.Label, request.Add = "Label_1", true
				}
				result, err = w.Change(context.Background(), request)
			case "reconcile":
				result, err = w.Reconcile(context.Background(), provider.journal)
			}
			if reject {
				if err == nil || !strings.Contains(err.Error(), "must be an IMAP account") || provider.modifies != 0 {
					t.Fatalf("non-IMAP account accepted: error=%v, mutations=%d", err, provider.modifies)
				}
				return
			}
			if err != nil {
				t.Fatalf("legacy labels %s: %v", command, err)
			}
			accounts, err := client.GetAccountsJSON()
			if err != nil || len(accounts) != 1 || accounts[0].AccountType != "imap" {
				t.Fatalf("legacy account type was not normalized: accounts=%+v, error=%v", accounts, err)
			}
			if command != "list" && !result.ProviderConfirmed {
				t.Fatalf("provider was not freshly confirmed: %+v", result)
			}
			wantMutations := 0
			if command == "add" || command == "remove" || command == "reconcile" {
				wantMutations = 1
				if result.Status != "changed_verified" || result.MailStatus != "verified" || !result.MailAfter.Read {
					t.Fatalf("label change not verified with read state preserved: %+v", result)
				}
			}
			if provider.modifies != wantMutations {
				t.Fatalf("mutations = %d, want %d", provider.modifies, wantMutations)
			}
		})
	}
}
