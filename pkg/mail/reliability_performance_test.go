package mail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the exact same test against the previous source using a Go overlay.
// The bridge is a fixture: 5ms startup and 25ms body rendering, no real Mail.
// Latencies model process/renderer work; they are not live-provider timings.
func TestReliabilityIdentityPerformance(t *testing.T) {
	python := "/usr/bin/python3"
	if _, err := os.Stat(python); err != nil {
		t.Skip("Python fixture unavailable")
	}
	bin := t.TempDir()
	log := filepath.Join(t.TempDir(), "bridge.log")
	t.Setenv("RELIABILITY_BRIDGE_LOG", log)
	osa := `#!/usr/bin/python3
import sys,os,json,time,re,base64
with open(os.environ['RELIABILITY_BRIDGE_LOG'],'a') as f:f.write('account-probe\n' if 'mail-app-cli-native-archive-account' in sys.argv[-1] else 'permission\n' if 'AXIsProcessTrusted' in sys.argv[-1] else 'process\n')
time.sleep(.005)
def response(script):
    if 'mail-app-cli-native-archive-account' in script:return 'true'
    if 'AXIsProcessTrusted' in script:return 'ready'
    if 'msg.content()' in script:
        with open(os.environ['RELIABILITY_BRIDGE_LOG'],'a') as f:f.write('render\n')
        time.sleep(.025)
    found=re.search(r"Number\('([0-9]+)'\)",script)
    n=int(found.group(1)) if found else 1
    return json.dumps({'id':str(n),'subject':'fixture '+str(n),'sender':'fixture@example.invalid','dateSent':'2026-01-01T00:00:00Z','messageSize':100+n,'read':True,'content':'fixture'})
script=sys.argv[-1]
if 'while (true)' in script:
    for line in sys.stdin:
        request=base64.b64decode(line).decode()
        print(json.dumps({'output':response(request)}),flush=True)
else:print(response(script))
`
	const n = 30
	items := make([]BatchItem, n)
	rows := make([]map[string]any, n)
	for i := range items {
		id := fmt.Sprint(i + 1)
		items[i] = BatchItem{ID: id, Account: "Fixture", SourceMailbox: "INBOX", TargetMailbox: "All Mail", Sender: "fixture@example.invalid", Subject: "fixture " + id, DateSent: "2026-01-01T00:00:00Z", MessageSize: 101 + i}
		rows[i] = map[string]any{"ID": i + 101, "Subject": items[i].Subject, "Sender": items[i].Sender, "DateSent": items[i].DateSent, "MessageSize": items[i].MessageSize, "Read": 1}
	}
	data, _ := json.Marshal(rows)
	t.Setenv("RELIABILITY_ROWS", string(data))
	sql := `#!/usr/bin/python3
import sys, os, json, re
q = sys.argv[-1]
if 'with verification_mailbox_0' in q:
    rows = []
    for scope, url in re.findall(r"verification_mailbox_(\d+) as \(select ROWID as ID, url as URL from mailboxes where url = '([^']+)'", q):
        source = url.endswith('/INBOX')
        rows.append({'VerificationScope': int(scope), 'MailboxID': 1 if source else 2, 'MailboxURL': url,
                     'Matches': '[]' if source else os.environ['RELIABILITY_ROWS']})
    print(json.dumps(rows))
elif 'm.size =' in q:
    print('[]' if 'l.mailbox_id = 1' in q else os.environ['RELIABILITY_ROWS'])
elif 'All%20Mail' in q:
    print('[{"ID":2,"URL":"imap://ABC/%5BGmail%5D/All%20Mail","TotalCount":30,"UnreadCount":0}]')
elif 'imap://ABC/INBOX' in q:
    print('[{"ID":1,"URL":"imap://ABC/INBOX","TotalCount":30,"UnreadCount":0}]')
else:
    print('[]')
`

	for name, script := range map[string]string{"osascript": osa, "sqlite3": sql} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
	previous := verificationBackoff
	verificationBackoff = []time.Duration{0}
	t.Cleanup(func() { verificationBackoff = previous })
	client := NewClient()
	client.PrimeAccounts([]Account{{ID: "ABC", Name: "Fixture"}})
	journal, err := CreateBatchJournal(filepath.Join(t.TempDir(), "receipt.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	started := time.Now()
	result, err := RunBatch(client, BatchOptions{Action: "archive", Verify: true, TrustSource: true, ReuseBridge: true, Receipt: journal}, items, func(*Client, *BatchItem) error { return nil })
	elapsed := time.Since(started)
	if err != nil || result.Succeeded != n || result.Unverified != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	processes, renders := strings.Count(string(logged), "process\n"), strings.Count(string(logged), "render\n")
	if os.Getenv("RELIABILITY_BASELINE") != "1" && (renders != 0 || processes > 3) {
		t.Fatalf("processes=%d renders=%d, want <=3 identity sessions and no renderer calls", processes, renders)
	}
	accountProbeProcesses := strings.Count(string(logged), "account-probe\n")
	permissionProcesses := strings.Count(string(logged), "permission\n")
	// Permission setup is separate from the identity/verification performance contract.
	measurement := map[string]any{"accountProbeProcesses": accountProbeProcesses, "permissionProcesses": permissionProcesses, "identityBridgeProcesses": processes, "messages": n, "elapsedMs": float64(elapsed.Microseconds()) / 1000, "bridgeProcesses": processes + permissionProcesses + accountProbeProcesses, "bodyRenderCalls": renders, "succeeded": result.Succeeded, "unverified": result.Unverified, "fixtureStartupMs": 5, "fixtureRenderMs": 25}
	encoded, _ := json.Marshal(measurement)
	t.Log(string(encoded))
	if path := os.Getenv("RELIABILITY_RESULT_FILE"); path != "" {
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
