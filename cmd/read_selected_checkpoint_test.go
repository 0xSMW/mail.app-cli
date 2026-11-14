package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func TestReadCommandCheckpointRetriesMissingIDsAndPreservesRecoveredResults(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for fake serial bridge")
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "bridge.log")
	script := `#!` + python + `
import sys, json, base64, re, os

def message(script, metadata):
    if "JSON.stringify({id:trashAccountID(account)})" in script:
        return {'id':'ABC'}
    if metadata:
        match = re.search(r"Number\('([0-9]+)'\)", script)
    else:
        match = re.search(r"const requestedId = '([0-9]+)'", script)
    if match is None:
        raise RuntimeError('unexpected fixture script')
    mid = match.group(1)
    with open(os.environ['READ_FIXTURE_LOG'], 'a') as log:
        log.write(('metadata ' if metadata else 'body ') + mid + '\n')
    return {'id':mid,'account':'Work','mailbox':'INBOX','rfcMessageId':'<'+mid+'@fixture.test>','sender':'sender@fixture.test','subject':'Message '+mid,'dateSent':'2026-11-12T12:00:00Z','messageSize':100,'read':metadata,'content':'body '+mid}
script = sys.argv[-1]
if "const input = $.NSFileHandle" in script:
    for line in sys.stdin:
        decoded = base64.b64decode(line).decode('utf-8')
        body = "const requestedId =" in decoded
        value = {'message':message(decoded, False),'phases':[]} if body else message(decoded, True)
        print(json.dumps({'output':json.dumps(value)}), flush=True)
else:
    print(json.dumps(message(script, True)))
`
	if err := os.WriteFile(filepath.Join(bin, "osascript"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "sqlite3"), []byte("#!/bin/sh\ncase \"$4\" in *\"Message 2\"*) printf '%s\\n' '[{\"ID\":2,\"URL\":\"imap://ABC/INBOX\",\"Subject\":\"Message 2\",\"Sender\":\"sender@fixture.test\",\"DateSent\":\"2026-11-12T12:00:00Z\",\"MessageSize\":100}]'; exit 0;; esac\nprintf '%s\\n' '[{\"ID\":1,\"URL\":\"imap://ABC/INBOX\",\"Subject\":\"Message 1\",\"Sender\":\"sender@fixture.test\",\"DateSent\":\"2026-11-12T12:00:00Z\",\"MessageSize\":100}]'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("READ_FIXTURE_LOG", log)
	t.Setenv("MAIL_APP_CLI_BACKEND", "legacy")
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	a := newApplication()
	a.mailClient = mail.NewClient()
	a.mailClient.PrimeAccounts([]mail.Account{{ID: "ABC", Name: "Work"}})
	locatedSecond := false
	missingOriginal := false
	a.messageLocator = func([]string) (map[string]mail.MessageLocation, error) {
		out := map[string]mail.MessageLocation{}
		if !missingOriginal {
			out["1"] = mail.MessageLocation{Account: "Work", Mailbox: "INBOX"}
		}
		if locatedSecond {
			out["2"] = mail.MessageLocation{Account: "Work", Mailbox: "INBOX"}
		}
		return out, nil
	}
	checkpoint := filepath.Join(t.TempDir(), "read.json")
	var stdout, stderr bytes.Buffer
	a.writer, _ = output.New(output.FormatJSON, &stdout, &stderr, false, "", "messages read", mail.SchemaVersion)
	run := func() error {
		cmd := a.newMessagesReadCmd()
		cmd.SetErr(&stderr)
		if err := cmd.ParseFlags([]string{"--checkpoint", checkpoint, "--body-source", "mail", "--events"}); err != nil {
			t.Fatal(err)
		}
		return cmd.RunE(cmd, []string{"1", "2"})
	}
	err = run()
	var partial *clierr.Error
	if !errors.As(err, &partial) || partial.Code != clierr.CodePartial {
		t.Fatalf("first err=%v stdout=%s", err, stdout.String())
	}
	var envelope struct {
		Data mail.SelectedReadResult `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Complete || len(envelope.Data.Items) != 2 || envelope.Data.Items[0].Message == nil || envelope.Data.Items[1].Error == "" {
		t.Fatalf("first=%+v", envelope.Data)
	}
	stdout.Reset()
	stderr.Reset()
	locatedSecond = true
	if err := run(); err != nil {
		t.Fatalf("resume err=%v stdout=%s", err, stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.Complete || len(envelope.Data.Items) != 2 || !envelope.Data.Items[0].Message.Read || envelope.Data.Items[1].Message == nil {
		t.Fatalf("resume=%+v", envelope.Data)
	}
	calls, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(calls) != "body 1\nmetadata 1\nmetadata 1\nbody 1\nmetadata 1\nbody 2\n" {
		t.Fatalf("bridge calls=%q", calls)
	}
	for _, line := range strings.Split(strings.TrimSpace(stderr.String()), "\n") {
		if !json.Valid([]byte(line)) {
			t.Fatalf("non-JSON progress %q", line)
		}
	}
	stdout.Reset()
	stderr.Reset()
	missingOriginal = true
	if err := run(); err != nil {
		t.Fatalf("missing original locator recovery err=%v stdout=%s", err, stdout.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.Complete || len(envelope.Data.Items) != 2 || envelope.Data.Items[0].ID != "1" || envelope.Data.Items[0].Message == nil {
		t.Fatalf("locator error overwrote recovered body: %+v", envelope.Data)
	}
}
