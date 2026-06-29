package mail

import "testing"

func TestNormalizeDraftContent(t *testing.T) {
	got := normalizeDraftContent("\r\nBody\r\n")
	if got != "Body" {
		t.Fatalf("normalizeDraftContent = %q, want Body", got)
	}
}
