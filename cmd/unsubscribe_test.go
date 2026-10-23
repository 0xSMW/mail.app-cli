package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
)

func TestUnsubscribeAllAccountsSkipsDisabledInventory(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(map[bool]string{true: "mixed", false: "all disabled"}[enabled], func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			bin := t.TempDir()
			t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
			accounts, err := json.Marshal([]mail.Account{{Name: "Disabled", Enabled: false}, {Name: "Enabled", Enabled: enabled}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(bin, "osascript"), []byte("#!/bin/sh\nprintf '%s' '"+string(accounts)+"'\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			// The expired discovery budget exposes constructed scopes without
			// asking the fake transport to enumerate any message or mailbox.
			code, stdout, stderr := run(t, "unsubscribe", "scan", "INBOX", "--all-accounts", "--budget", "1ns", "--checkpoint", filepath.Join(t.TempDir(), "discovery.json"), "--json")
			if !enabled {
				if code == 0 || !strings.Contains(stderr, "no enabled Mail accounts") {
					t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
				}
				return
			}
			var env struct {
				Data mail.UnsubscribeScanResult `json:"data"`
			}
			if err = json.Unmarshal([]byte(stdout), &env); err != nil {
				t.Fatalf("exit=%d decode=%v stdout=%s stderr=%s", code, err, stdout, stderr)
			}
			if code != 5 || len(env.Data.Coverage) != 1 || env.Data.Coverage[0].Account != "Enabled" {
				t.Fatalf("exit=%d coverage=%+v stderr=%s", code, env.Data.Coverage, stderr)
			}
		})
	}
}

func TestUnsubscribeShowRetainsPartialControlsAndExactURLs(t *testing.T) {
	for _, complete := range []bool{true, false} {
		var stdout, stderr bytes.Buffer
		w, err := output.New(output.FormatJSON, &stdout, &stderr, false, "", "unsubscribe show", mail.SchemaVersion)
		if err != nil {
			t.Fatal(err)
		}
		a := &application{writer: w}
		o := &mail.UnsubscribeObservation{ID: "42", Complete: complete, Destinations: []mail.UnsubscribeDestination{{URL: "https://example.test/leave?token=a%2Fb&list=one", Kind: "unsubscribe"}}}
		err = a.writeUnsubscribeObservation(o, nil)
		if complete && err != nil || !complete && (err == nil || clierr.Classify(err).Code != clierr.CodePartial) {
			t.Fatalf("complete=%v err=%v", complete, err)
		}
		var env struct {
			OK   bool                        `json:"ok"`
			Data mail.UnsubscribeObservation `json:"data"`
		}
		if err = json.Unmarshal(stdout.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if env.OK != complete || env.Data.Destinations[0].URL != o.Destinations[0].URL {
			t.Fatalf("lost controls: %s", stdout.String())
		}
	}
}

func TestUnsubscribeGroupedOutputRetainsHistoricalControlsAndExceptions(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w, err := output.New(output.FormatJSON, &stdout, &stderr, false, ".data", "unsubscribe scan", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	a := &application{writer: w}
	r := mail.UnsubscribeScanResult{Groups: []mail.UnsubscribeGroup{{ListID: "weekly.example.test", Destinations: []mail.UnsubscribeDestination{{URL: "https://example.test/current"}, {URL: "https://example.test/historical"}}}}, TraversalComplete: true, Complete: false, Exceptions: []mail.UnsubscribeObservation{{ID: "43", Error: "missing local source"}}, Checkpoint: "/private/discovery.json"}
	err = a.writeUnsubscribeScan(r)
	if err == nil || clierr.Classify(err).Code != clierr.CodePartial {
		t.Fatalf("partial error=%v", err)
	}
	var got mail.UnsubscribeScanResult
	if err = json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("%v %s", err, stdout.String())
	}
	if len(got.Groups[0].Destinations) != 2 || len(got.Exceptions) != 1 || !got.TraversalComplete || got.Complete {
		t.Fatalf("lost history or coverage: %+v", got)
	}
}

func TestUnsubscribeHumanOutputEscapesMessageAndDestinationControls(t *testing.T) {
	var stdout, stderr bytes.Buffer
	w, err := output.New(output.FormatPlain, &stdout, &stderr, false, "", "unsubscribe show", mail.SchemaVersion)
	if err != nil {
		t.Fatal(err)
	}
	a := &application{writer: w}
	o := &mail.UnsubscribeObservation{ID: "42", Account: "Example\x1b[2J", Complete: true, Mailboxes: []string{"INBOX"}, Destinations: []mail.UnsubscribeDestination{{URL: "https://example.test/\u202eleave", Kind: "unsubscribe", Evidence: []mail.UnsubscribeEvidence{{Text: "Click\u2066", Source: "html"}}}}}
	if err = a.writeUnsubscribeObservation(o, nil); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(stdout.String(), "\x1b\u202e\u2066") || !strings.Contains(stdout.String(), `\u202e`) {
		t.Fatalf("unsafe display: %q", stdout.String())
	}
}

func TestUnsubscribeCommandsRejectInvalidArgumentsBeforeMail(t *testing.T) {
	for _, args := range [][]string{
		{"unsubscribe", "show"}, {"unsubscribe", "show", "bad"}, {"unsubscribe", "show", "1", "2"},
		{"unsubscribe", "scan"}, {"unsubscribe", "scan", "INBOX", "--page-size", "0"},
		{"unsubscribe", "scan", "INBOX", "--max-pages", "-1"}, {"unsubscribe", "scan", "INBOX", "--budget", "0s"},
		{"unsubscribe", "scan", "INBOX", "--retry-incomplete"}, {"unsubscribe", "scan", "INBOX", "--restart-checkpoint"},
		{"unsubscribe", "scan", "INBOX", "--mailbox", "Archive"},
	} {
		code, _, stderr := run(t, args...)
		if code != 1 {
			t.Fatalf("%v exit=%d stderr=%s", args, code, stderr)
		}
	}
}
