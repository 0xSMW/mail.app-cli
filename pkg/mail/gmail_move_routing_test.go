package mail

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMoveGmailInboxToAllMailUsesNativeArchive(t *testing.T) {
	m := newDirectArchiveMail()
	ax := &directArchiveAX{attempted: true}
	c := directArchiveClient(t, m, ax)
	if err := c.MoveMessage("Test", "INBOX", "7", "All Mail"); err != nil {
		t.Fatal(err)
	}
	if ax.presses != 1 || m.created != 1 || m.closed != 1 {
		t.Fatalf("native archive presses=%d created=%d closed=%d", ax.presses, m.created, m.closed)
	}
}

func TestMoveArchiveRoutingPreservesOrdinaryMoves(t *testing.T) {
	for _, tc := range []struct {
		name, source, target, probe, want string
	}{
		{"ordinary-inbox-all-mail", "INBOX", "All Mail", "false", "probe\nmove\n"},
		{"other-target", "INBOX", "Receipts", "true", "move\n"},
		{"other-source", "Important", "All Mail", "true", "move\n"},
		{"invalid-account-probe", "INBOX", "All Mail", "null", "probe\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "routing.log")
			t.Setenv("MOVE_ROUTING_LOG", log)
			t.Setenv("MOVE_PROBE_RESPONSE", tc.probe)
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
			writeFakeOsaScript(t, `case "$*" in
 *mail-app-cli-native-archive-account*) printf 'probe\n' >> "$MOVE_ROUTING_LOG"; printf '%s' "$MOVE_PROBE_RESPONSE" ;;
 *'msg.mailbox = destMbox'*) printf 'move\n' >> "$MOVE_ROUTING_LOG"; printf Success ;;
 *) printf 'unexpected\n' >> "$MOVE_ROUTING_LOG"; exit 1 ;;
esac`)
			err := NewClient().MoveMessage("Test", tc.source, "7", tc.target)
			if (err != nil) != (tc.probe == "null") {
				t.Fatalf("error=%v", err)
			}
			data, readErr := os.ReadFile(log)
			if readErr != nil || string(data) != tc.want {
				t.Fatalf("dispatch=%q err=%v want=%q", data, readErr, tc.want)
			}
		})
	}
}

func TestMoveGmailArchiveAliasesUseResolvedNativeArchive(t *testing.T) {
	for _, target := range []string{"Archive", "[Gmail]/All Mail", "Gmail/All Mail", "all mail"} {
		t.Run(target, func(t *testing.T) {
			log := filepath.Join(t.TempDir(), "routing.log")
			t.Setenv("MOVE_ROUTING_LOG", log)
			t.Setenv("MAIL_APP_CLI_AUTOMATION_LOCK_PATH", filepath.Join(t.TempDir(), "lock"))
			writeFakeOsaScript(t, `case "$*" in
 *mail-app-cli-native-archive-account*) printf 'probe\n' >> "$MOVE_ROUTING_LOG"; printf true ;;
 *'return name of archiveMailbox'*) printf 'resolve\n' >> "$MOVE_ROUTING_LOG"; printf 'All Mail' ;;
 *'const owner ='*) printf 'cleanup\n' >> "$MOVE_ROUTING_LOG"; printf closed ;;
 *'mail-app-cli-phase'*) printf 'archive\n' >> "$MOVE_ROUTING_LOG"; `+nativeArchiveOwnedEvent+nativeArchivePressEvent+`printf '%s' '{"prepared":true,"applied":true}' ;;
 *) printf 'unexpected\n' >> "$MOVE_ROUTING_LOG"; exit 1 ;;
esac`)
			if err := NewClient().MoveMessage("Test", "INBOX", "7", target); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(log)
			if err != nil || string(data) != "probe\nresolve\narchive\ncleanup\n" {
				t.Fatalf("dispatch=%q error=%v", data, err)
			}
		})
	}
}
