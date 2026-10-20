package mail

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMailboxCollectionRoutingAndPartialResults(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "1")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	for _, tc := range []struct {
		name, selected, work, personal string
		accounts                       []Account
		wantCount                      int
		wantNil                        bool
		wantError                      string
	}{
		{name: "named error remains direct", selected: "Work", accounts: []Account{{Name: "Work"}, {Name: "Personal"}}, work: "error", wantNil: true, wantError: "jxa error:"},
		{name: "single error remains direct", accounts: []Account{{Name: "Work"}}, work: "error", wantNil: true, wantError: "jxa error:"},
		{name: "partial success", accounts: []Account{{Name: "Work"}, {Name: "Personal"}}, work: "error", personal: `[{"name":"INBOX","account":"Personal"}]`, wantCount: 1},
		{name: "empty success with error still fails", accounts: []Account{{Name: "Work"}, {Name: "Personal"}}, work: "error", personal: "[]", wantNil: true, wantError: "failed to get mailboxes from all accounts:"},
		{name: "all errors", accounts: []Account{{Name: "Work"}, {Name: "Personal"}}, work: "error", personal: "error", wantNil: true, wantError: "failed to get mailboxes from all accounts:"},
		{name: "all empty preserves nil", accounts: []Account{{Name: "Work"}, {Name: "Personal"}}, work: "[]", personal: "[]", wantNil: true},
		{name: "single empty preserves slice", accounts: []Account{{Name: "Work"}}, work: "[]"},
		{name: "no accounts preserves slice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WORK_RESPONSE", tc.work)
			t.Setenv("PERSONAL_RESPONSE", tc.personal)
			writeFakeOsaScript(t, `case "$*" in
  *"const accounts = mail.accounts();"*) response="[]" ;;
  *"byName('Work')"*) response="$WORK_RESPONSE" ;;
  *"byName('Personal')"*) response="$PERSONAL_RESPONSE" ;;
  *) exit 91 ;;
esac
if [ "$response" = error ]; then printf 'fixture failure\n' >&2; exit 1; fi
printf '%s\n' "$response"
`)
			c := NewClient()
			c.PrimeAccounts(tc.accounts)
			boxes, err := c.GetMailboxesJSON(tc.selected)
			if tc.wantError == "" && err != nil || tc.wantError != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.wantError)) {
				t.Fatalf("error = %v, want prefix %q", err, tc.wantError)
			}
			if len(boxes) != tc.wantCount || (boxes == nil) != tc.wantNil {
				t.Fatalf("mailboxes = %#v, want count %d, nil %v", boxes, tc.wantCount, tc.wantNil)
			}
		})
	}
}

func TestLegacyAccountCacheRetriesAndClones(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	c := NewClient()
	writeFakeOsaScript(t, "exit 1\n")
	if _, err := c.GetAccountsJSON(); err == nil || c.shared.accountsLoaded {
		t.Fatalf("failed fetch: loaded=%v, error=%v", c.shared.accountsLoaded, err)
	}
	writeFakeOsaScript(t, `printf '%s\n' '[{"name":"Work","emailAddresses":["a@example.test"]}]'`)
	accounts, err := c.GetAccountsJSON()
	if err != nil || len(accounts) != 1 {
		t.Fatalf("retry = %#v, %v", accounts, err)
	}
	accounts[0].Name = "Changed"
	accounts[0].EmailAddresses[0] = "changed@example.test"
	writeFakeOsaScript(t, "exit 92\n")
	cached, err := c.GetAccountsJSON()
	if err != nil || cached[0].Name != "Work" || cached[0].EmailAddresses[0] != "a@example.test" {
		t.Fatalf("cached = %#v, %v", cached, err)
	}
	c.ResetAccountCache()
	writeFakeOsaScript(t, "printf 'null\\n'\n")
	empty, err := c.GetAccountsJSON()
	if err != nil || empty == nil || len(empty) != 0 || !c.shared.accountsLoaded {
		t.Fatalf("empty cache = %#v, %v", empty, err)
	}
	writeFakeOsaScript(t, "exit 93\n")
	if _, err := c.GetAccountsJSON(); err != nil {
		t.Fatalf("empty success was not cached: %v", err)
	}
}

func TestLegacyContentBatchDecodeAndTimeout(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	for _, response := range []string{"null", "{}", `{"42":"body"}`} {
		writeFakeOsaScript(t, "printf '%s\\n' '"+response+"'\n")
		content, err := NewClient().getMessageContentBatch("Work", "INBOX", []string{"42"}, time.Second)
		if err != nil || (content == nil) != (response == "null") || response == `{"42":"body"}` && content["42"] != "body" {
			t.Fatalf("content (%s) = %#v, %v", response, content, err)
		}
	}
	writeFakeOsaScript(t, "printf 'invalid\\n'\n")
	_, err := NewClient().getMessageContentBatch("Work", "INBOX", nil, time.Second)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) || !strings.HasPrefix(err.Error(), "failed to parse content JSON: ") {
		t.Fatalf("decode error = %v", err)
	}
	writeFakeOsaScript(t, "/bin/sleep 2\n")
	_, err = NewClient().getMessageContentBatch("Work", "INBOX", nil, 30*time.Millisecond)
	var timeout *AutomationTimeoutError
	if !errors.As(err, &timeout) || timeout.Timeout != 30*time.Millisecond {
		t.Fatalf("timeout = %v", err)
	}
}

func TestRunJXAWrapperReusesSessionAndPreservesErrors(t *testing.T) {
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	log := filepath.Join(t.TempDir(), "launches")
	t.Setenv("SESSION_LAUNCH_LOG", log)
	writeFakeOsaScript(t, `printf 'start\n' >> "$SESSION_LAUNCH_LOG"
n=0
while IFS= read -r line; do
 n=$((n+1))
 if [ "$n" -eq 2 ]; then printf '{"error":"fixture failure"}\n';
 else printf '{"output":"Success"}\n'; fi
done
`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := newJXASession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := NewClient()
	c.session = s
	if out, err := c.runJXA("first"); err != nil || out != "Success" {
		t.Fatalf("first = %q, %v", out, err)
	}
	if _, err := c.runJXA("second"); err == nil || err.Error() != "jxa error: fixture failure" {
		t.Fatalf("session error = %v", err)
	}
	if out, err := c.runJXA("third"); err != nil || out != "Success" {
		t.Fatalf("third = %q, %v", out, err)
	}
	cancel()
	if _, err := c.runJXA("cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	data, err := os.ReadFile(log)
	if err != nil || strings.Count(string(data), "start") != 1 {
		t.Fatalf("launches = %q, %v", data, err)
	}
}

func TestMailboxCollectionIndexRouting(t *testing.T) {
	for _, tc := range []struct {
		name, selected string
		accounts       []Account
	}{
		{"named", "Work", []Account{{ID: "ABC", Name: "Work"}, {ID: "DEF", Name: "Personal"}}},
		{"single", "", []Account{{ID: "ABC", Name: "Work"}}},
		{"multiple", "", []Account{{ID: "ABC", Name: "Work"}, {ID: "DEF", Name: "Personal"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, log := snapshotSQLiteFixture(t, `
alter table mailboxes add column total_count integer;
alter table mailboxes add column unread_count integer;
insert into mailboxes values ('imap://ABC/INBOX', 7, 2), ('imap://DEF/INBOX', 4, 1);
`)
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
			c.ResetAccountCache()
			c.PrimeAccounts(tc.accounts)
			// PATH contains only the fixture sqlite3, so a live fallback fails.
			boxes, err := c.GetMailboxesJSON(tc.selected)
			want := 1
			if tc.name == "multiple" {
				want = 2
			}
			if err != nil || len(boxes) != want {
				t.Fatalf("indexed mailboxes = %#v, %v", boxes, err)
			}
			queries, err := os.ReadFile(log)
			if err != nil || strings.Count(string(queries), "from mailboxes") != want {
				t.Fatalf("index queries = %q, %v; want %d without archive enrichment", queries, err, want)
			}
		})
	}
}

func TestMailboxFallbackEnrichesOnlyAfterLiveSuccess(t *testing.T) {
	for _, mode := range []string{"index", "index error", "live", "live error"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "queries")
			t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
			t.Setenv("MAIL_APP_CLI_DISABLE_ENVELOPE_INDEX", "")
			t.Setenv("MAIL_APP_CLI_ENVELOPE_INDEX_PATH", filepath.Join(dir, "index.sqlite"))
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(dir, "lock"))
			t.Setenv("PLUMBING_MODE", mode)
			t.Setenv("PLUMBING_QUERY_LOG", log)
			t.Setenv("PATH", dir)
			for name, body := range map[string]string{
				"sqlite3": `printf 'query\n' >> "$PLUMBING_QUERY_LOG"
if [ "$PLUMBING_MODE" = 'index error' ]; then printf 'fixture schema failure\n' >&2; exit 1; fi
case "$*" in
 *"order by url;"*)
  if [ "$PLUMBING_MODE" != index ]; then printf '[]\n'; exit 0; fi ;;
esac
printf '%s\n' '[{"ID":7,"URL":"imap://ABC/%5BGmail%5D/All%20Mail","TotalCount":99,"UnreadCount":9}]'
`,
				"osascript": `if [ "$PLUMBING_MODE" != live ]; then printf 'unexpected live read\n' >&2; exit 1; fi
printf '%s\n' '[{"name":"Archive","account":"Work","totalCount":1,"unreadCount":0}]'
`,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0755); err != nil {
					t.Fatal(err)
				}
			}
			c := NewClient()
			c.PrimeAccounts([]Account{{ID: "ABC", Name: "Work"}})
			boxes, err := c.GetMailboxesJSON("Work")
			if strings.HasSuffix(mode, "error") {
				if err == nil || boxes != nil {
					t.Fatalf("failed lookup = %#v, %v", boxes, err)
				}
			} else if err != nil || len(boxes) != 1 || boxes[0].Name != "All Mail" || boxes[0].TotalCount != 99 || boxes[0].UnreadCount != 9 {
				t.Fatalf("mailboxes = %#v, %v", boxes, err)
			}
			queries, readErr := os.ReadFile(log)
			want := 1
			if mode == "live" {
				want = 2
			}
			if readErr != nil || strings.Count(string(queries), "query") != want {
				t.Fatalf("queries = %q, %v; want %d", queries, readErr, want)
			}
		})
	}
}
