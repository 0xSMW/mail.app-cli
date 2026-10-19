package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Real SQLite executes the generated SQL against an isolated fixture. The shim
// ignores the production database argument so these tests never open Mail data.
func snapshotSQLiteFixture(t *testing.T, data string) (*Client, string) {
	t.Helper()
	binary, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is required for Envelope Index integration tests")
	}
	binary, err = filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	database := filepath.Join(dir, "index.sqlite")
	schema := `create table mailboxes (url text);
create table subjects (subject text);
create table addresses (address text, comment text);
create table messages (mailbox integer, subject integer, sender integer, date_sent integer, size integer, read integer, deleted integer);
create table labels (message_id integer, mailbox_id integer);
insert into subjects values ('subject'), ('near subject');
insert into addresses values ('sender@example.com', ''), ('other@example.com', '');
` + data + `
alter table mailboxes add column total_count integer default 0;
alter table mailboxes add column unread_count integer default 0;
`
	cmd := exec.Command(binary, database)
	cmd.Stdin = strings.NewReader(schema)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create test database: %v: %s", err, output)
	}
	log := filepath.Join(dir, "queries")
	t.Setenv("SNAPSHOT_SQLITE_BINARY", binary)
	t.Setenv("SNAPSHOT_SQLITE_DATABASE", database)
	t.Setenv("SNAPSHOT_SQLITE_LOG", log)
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "")
	script := `#!/bin/sh
[ "$1" = '-readonly' ] && [ "$2" = '-json' ] || exit 92
if [ "$#" -eq 3 ]; then
  /bin/cat > "$SNAPSHOT_SQLITE_LOG.stdin"
  /bin/cat "$SNAPSHOT_SQLITE_LOG.stdin" >> "$SNAPSHOT_SQLITE_LOG"
  exec "$SNAPSHOT_SQLITE_BINARY" -readonly -json "$SNAPSHOT_SQLITE_DATABASE" < "$SNAPSHOT_SQLITE_LOG.stdin"
fi
[ "$#" -eq 4 ] || exit 91
printf '%s\n' "$4" >> "$SNAPSHOT_SQLITE_LOG"
exec "$SNAPSHOT_SQLITE_BINARY" -readonly -json "$SNAPSHOT_SQLITE_DATABASE" "$4"
`
	if err := os.WriteFile(filepath.Join(dir, "sqlite3"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	client := NewClient()
	client.PrimeAccounts([]Account{{ID: "ABC", Name: "Work"}, {ID: "DEF", Name: "Personal"}})
	return client, log
}

func TestVerificationSnapshotCustomAllMailLikeLabelRemainsPresent(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `
insert into mailboxes(rowid,url) values (1,'imap://ABC/All%20Work%20Mail'), (2,'imap://ABC/%5BGmail%5D/All%20Mail');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (101,2,1,1,1787961600,42,1,0);
insert into labels values (101,1);
`)
	source, destination := snapshotScope("Work", "All Work Mail"), snapshotScope("Work", "All Mail")
	results := client.getIdentitySnapshotForVerification([]identityVerificationScope{source, destination})
	for _, scope := range []identityVerificationScope{source, destination} {
		got := snapshotMessages(t, results, scope)
		if len(got) != 1 || got[0].ID != "101" {
			t.Fatalf("%s must retain membership while its label remains: %+v", scope.key.mailbox, got)
		}
	}
}

func TestVerificationSnapshotManyScopesShareOneStatement(t *testing.T) {
	var data strings.Builder
	var scopes []identityVerificationScope
	for i := 0; i < 501; i++ {
		mailbox := fmt.Sprintf("Folder%d", i)
		fmt.Fprintf(&data, "insert into mailboxes values ('imap://ABC/%s');\n", mailbox)
		scopes = append(scopes, snapshotScope("Work", mailbox))
	}
	client, log := snapshotSQLiteFixture(t, data.String())
	results := client.getIdentitySnapshotForVerification(scopes)
	for _, scope := range scopes {
		if got := snapshotMessages(t, results, scope); len(got) != 0 {
			t.Fatalf("empty mailbox %s has matches: %+v", scope.key.mailbox, got)
		}
	}
	query, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(query), "with verification_mailbox_0 as (") != 1 || strings.Count(string(query), ";") != 1 {
		t.Fatal("large scope sets must still share one statement and invocation")
	}
}

func TestVerificationSnapshotLargeIdentitiesAvoidArgumentLimit(t *testing.T) {
	source, destination := snapshotScope("Work", "INBOX"), snapshotScope("Work", "All Mail")
	source.identities, destination.identities = nil, nil
	var data strings.Builder
	data.WriteString(`insert into mailboxes(rowid,url) values (1,'imap://ABC/INBOX'), (2,'imap://ABC/%5BGmail%5D/All%20Mail');`)
	for i := 0; i < 500; i++ {
		identity := snapshotTestIdentity()
		identity.Subject = fmt.Sprintf("%d %s", i, strings.Repeat("x", 1024))
		source.identities = append(source.identities, identity)
		destination.identities = append(destination.identities, identity)
		if i == 499 {
			fmt.Fprintf(&data, "insert into subjects(rowid,subject) values (3,%s);\n", sqlQuote(identity.Subject))
			data.WriteString("insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (101,2,3,1,1787961600,42,1,0); insert into labels values (101,1);")
		}
	}
	client, log := snapshotSQLiteFixture(t, data.String())
	results := client.getIdentitySnapshotForVerification([]identityVerificationScope{source, destination})
	for _, scope := range []identityVerificationScope{source, destination} {
		result := results[scope.key]
		if result.err != nil {
			t.Fatal(result.err)
		}
		got := result.matches[scope.identities[len(scope.identities)-1].fallbackKey()]
		if len(got) != 1 || got[0].ID != "101" {
			t.Fatalf("large query lost exact identity in %s: %+v", scope.key.mailbox, got)
		}
	}
	query, err := os.ReadFile(log + ".stdin")
	if err != nil {
		t.Fatal(err)
	}
	if len(query) < 1<<20 {
		t.Fatalf("fixture should exceed the host argv budget, got %d bytes", len(query))
	}
}

func snapshotTestIdentity() StableIdentity {
	return StableIdentity{Sender: "sender@example.com", Subject: "subject", DateSent: "2026-08-29T00:00:00Z", MessageSize: 42}
}

func snapshotScope(account, mailbox string) identityVerificationScope {
	return identityVerificationScope{key: verificationMailboxKey{account, mailbox}, identities: []StableIdentity{snapshotTestIdentity()}}
}

func snapshotMessages(t *testing.T, results map[verificationMailboxKey]identityVerificationResult, scope identityVerificationScope) []Message {
	t.Helper()
	result, ok := results[scope.key]
	if !ok || result.err != nil {
		t.Fatalf("snapshot %v: missing=%v error=%v", scope.key, !ok, result.err)
	}
	return result.matches[snapshotTestIdentity().fallbackKey()]
}

func TestVerificationSnapshotLabelMembershipAndBackingDestination(t *testing.T) {
	client, log := snapshotSQLiteFixture(t, `
insert into mailboxes(rowid,url) values (1,'imap://ABC/INBOX'), (2,'imap://ABC/%5BGmail%5D/All%20Mail'), (3,'imap://DEF/INBOX'), (4,'imap://DEF/%5BGmail%5D/All%20Mail');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values
 (101,2,1,1,1787961600,42,0,0), (102,4,1,1,1787961600,42,1,0), (103,1,1,1,1787961600,42,1,0), (104,2,1,1,1787961600,42,1,1);
insert into labels values (101,1), (102,3), (103,2);
`)
	source, destination, personal := snapshotScope("Work", "INBOX"), snapshotScope("Work", "All Mail"), snapshotScope("Personal", "INBOX")
	results := client.getIdentitySnapshotForVerification([]identityVerificationScope{source, destination, personal})
	sourceMessages := snapshotMessages(t, results, source)
	if len(sourceMessages) != 2 {
		t.Fatalf("source matches = %+v; want backing and label membership", sourceMessages)
	}
	ids := map[string]bool{}
	for _, message := range sourceMessages {
		ids[message.ID] = true
		if message.Account != "Work" || message.Mailbox != "INBOX" {
			t.Fatalf("source provenance: %+v", message)
		}
	}
	if !ids["101"] || !ids["103"] {
		t.Fatalf("source IDs = %v; changed local IDs must be found", ids)
	}
	destinationMessages := snapshotMessages(t, results, destination)
	if len(destinationMessages) != 1 || destinationMessages[0].ID != "101" || destinationMessages[0].Mailbox != "All Mail" {
		t.Fatalf("destination must use backing membership: %+v", destinationMessages)
	}
	personalMessages := snapshotMessages(t, results, personal)
	if len(personalMessages) != 1 || personalMessages[0].ID != "102" || personalMessages[0].Account != "Personal" {
		t.Fatalf("account isolation: %+v", personalMessages)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "with verification_mailbox_0 as (") != 1 || strings.Count(string(data), "union all") != 2 {
		t.Fatalf("source and destination must share one SQL invocation: %s", data)
	}
}

func TestVerificationSnapshotMissingMailboxIsError(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `insert into mailboxes values ('imap://ABC/INBOX');`)
	present, missing := snapshotScope("Work", "INBOX"), snapshotScope("Work", "Missing")
	results := client.getIdentitySnapshotForVerification([]identityVerificationScope{present, missing})
	if got := snapshotMessages(t, results, present); len(got) != 0 {
		t.Fatalf("empty mailbox: %+v", got)
	}
	if results[missing.key].err == nil {
		t.Fatal("missing mailbox must be an error, not successful absence")
	}
}

func TestVerificationSnapshotArchiveResolutionPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, exact string
		want        string
	}{
		{name: "literal before unrelated nested All Mail", want: "202"},
		{name: "exact Gmail system mailbox before literal", exact: `insert into mailboxes(rowid,url) values (4,'imap://ABC/%5BGmail%5D/All%20Mail'); insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (204,4,1,1,1787961600,42,0,0);`, want: "204"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, _ := snapshotSQLiteFixture(t, `
insert into mailboxes(rowid,url) values (1,'imap://ABC/Unrelated/All%20Mail'), (2,'imap://ABC/Archive'), (3,'imap://ABC/Nested/Archive');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values (201,1,1,1,1787961600,42,0,0), (202,2,1,1,1787961600,42,0,0), (203,3,1,1,1787961600,42,0,0);
`+tc.exact)
			scope := snapshotScope("Work", "Archive")
			got := snapshotMessages(t, client.getIdentitySnapshotForVerification([]identityVerificationScope{scope}), scope)
			if len(got) != 1 || got[0].ID != tc.want {
				t.Fatalf("Archive resolution: %+v, want ID %s", got, tc.want)
			}
		})
	}
}

func TestVerificationSnapshotCompleteTupleAndDuplicateReadStates(t *testing.T) {
	client, _ := snapshotSQLiteFixture(t, `
insert into mailboxes values ('imap://ABC/INBOX');
insert into messages(rowid,mailbox,subject,sender,date_sent,size,read,deleted) values
 (301,1,1,1,1787961600,42,0,0), (302,1,1,1,1787961600,42,1,0),
 (303,1,2,1,1787961600,42,0,0), (304,1,1,2,1787961600,42,0,0),
 (305,1,1,1,1787961601,42,0,0), (306,1,1,1,1787961600,43,0,0);
`)
	scope := snapshotScope("Work", "INBOX")
	scope.identities = append(scope.identities, scope.identities[0])
	got := snapshotMessages(t, client.getIdentitySnapshotForVerification([]identityVerificationScope{scope}), scope)
	if len(got) != 2 {
		t.Fatalf("full tuple should reject each near match and preserve two exact duplicates: %+v", got)
	}
	states := map[string]bool{}
	for _, message := range got {
		states[message.ID] = message.Read
	}
	if unread, ok := states["301"]; !ok || unread {
		t.Fatalf("unread duplicate lost: %+v", got)
	}
	if read, ok := states["302"]; !ok || !read {
		t.Fatalf("read duplicate lost: %+v", got)
	}
}

// Corrupt or incomplete subprocess output must not become evidence of absence.
func TestVerificationSnapshotRejectsMalformedOutput(t *testing.T) {
	row := func(scope int, matches string) map[string]any {
		return map[string]any{"VerificationScope": scope, "MailboxID": 1, "MailboxURL": "imap://ABC/INBOX", "Matches": matches}
	}
	missingID := row(0, "[]")
	delete(missingID, "MailboxID")
	missingURL := row(0, "[]")
	delete(missingURL, "MailboxURL")
	tests := []struct {
		name string
		rows []map[string]any
	}{
		{"null matches", []map[string]any{row(0, "null")}},
		{"object matches", []map[string]any{row(0, "{}")}},
		{"incomplete identity", []map[string]any{row(0, `[{"ID":301}]`)}},
		{"zero ID", []map[string]any{row(0, `[{"ID":0,"Subject":"subject","Sender":"sender@example.com","DateSent":"2026-08-29T00:00:00Z","MessageSize":42}]`)}},
		{"missing subject", []map[string]any{row(0, `[{"ID":301,"Sender":"sender@example.com","DateSent":"2026-08-29T00:00:00Z","MessageSize":42}]`)}},
		{"missing mailbox ID", []map[string]any{missingID}},
		{"missing mailbox URL", []map[string]any{missingURL}},
		{"missing scope row", []map[string]any{}},
		{"duplicate scope", []map[string]any{row(0, "[]"), row(0, "[]")}},
		{"out of range scope", []map[string]any{row(1, "[]")}},
		{"negative scope", []map[string]any{row(-1, "[]")}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			output, err := json.Marshal(tc.rows)
			if err != nil {
				t.Fatal(err)
			}
			outputPath := filepath.Join(dir, "snapshot.json")
			if err := os.WriteFile(outputPath, output, 0600); err != nil {
				t.Fatal(err)
			}
			// Read the injected JSON with a shell builtin; no real sqlite or Mail access.
			script := `#!/bin/sh
IFS= read -r output < "$SNAPSHOT_OUTPUT"
printf '%s' "$output"
`
			if err := os.WriteFile(filepath.Join(dir, "sqlite3"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SNAPSHOT_OUTPUT", outputPath)
			t.Setenv("PATH", dir)
			t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "")
			client := NewClient()
			client.PrimeAccounts([]Account{{ID: "ABC", Name: "Work"}})
			scope := snapshotScope("Work", "INBOX")
			results := client.getIdentitySnapshotForVerification([]identityVerificationScope{scope})
			result, ok := results[scope.key]
			if !ok || result.err == nil {
				t.Fatalf("malformed source must produce an error, got present=%v result=%+v", ok, result)
			}
			if result.matches != nil {
				t.Fatalf("malformed source must not expose successful matches: %+v", result)
			}
		})
	}
}
