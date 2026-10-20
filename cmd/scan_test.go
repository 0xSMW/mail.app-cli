package cmd

import (
	"errors"
	"strconv"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
)

func TestScanOptionsRejectedBeforeMailAccess(t *testing.T) {
	a := newApplication()

	for _, flags := range [][]string{{"--since", "nonsense"}, {"--query", "   \"\" "}, {"--limit", "-1"}, {"--limit", strconv.Itoa(int(^uint(0) >> 1))}, {"--restart-checkpoint"}, {"--checkpoint", "/nonexistent/mail-scan-fixture/state.json"}, {"--checkpoint", "fixture.json", "--cursor", "opaque"}} {
		command := a.newMessagesScanCmd()
		if err := command.ParseFlags(flags); err != nil {
			t.Fatal(err)
		}
		err := command.RunE(command, []string{"INBOX", "Archive"})
		var usage *clierr.Error
		if !errors.As(err, &usage) || usage.Code != clierr.CodeUsage {
			t.Fatalf("flags=%v error=%v, want usage failure before account resolution", flags, err)
		}
	}
}

func TestScanRejectsBlankMailboxBeforeAccountResolution(t *testing.T) {
	a := newApplication()
	command := a.newMessagesScanCmd()
	err := command.RunE(command, []string{"INBOX", " "})
	var usage *clierr.Error
	if !errors.As(err, &usage) || usage.Code != clierr.CodeUsage || usage.Message != "scan scopes need both account and mailbox" {
		t.Fatalf("blank mailbox error=%v, want scope usage failure", err)
	}
}
