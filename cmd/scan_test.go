package cmd

import (
	"testing"
)

func TestScanOptionsRejectedBeforeMailAccess(t *testing.T) {
	a := newApplication()

	for _, flags := range [][]string{{"--since", "nonsense"}, {"--query", "   \"\" "}, {"--limit", "-1"}, {"--restart-checkpoint"}, {"--checkpoint", "/nonexistent/mail-scan-fixture/state.json"}, {"--checkpoint", "fixture.json", "--cursor", "opaque"}} {
		command := a.newMessagesScanCmd()
		if err := command.ParseFlags(flags); err != nil {
			t.Fatal(err)
		}
		if err := command.RunE(command, []string{"INBOX"}); err == nil {
			t.Fatalf("flags=%v accepted", flags)
		}
	}
}
