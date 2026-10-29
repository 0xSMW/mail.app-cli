package mail

import "testing"

// TestGmailSnapshotMailboxNormalizesToNFC covers the url.Parse success
// branch of gmailSnapshotMailbox, which decodes inline instead of calling
// mailboxLeafFromURL and so did not get that function's NFC normalization.
// Same root cause as TestMailboxLeafFromURLNormalizesToNFC: the Envelope
// Index stores folder names in NFD, Mail.app's live lookups expect NFC.
func TestGmailSnapshotMailboxNormalizesToNFC(t *testing.T) {
	const nfd = "Entwürfe" // decomposed, as stored in the index
	const nfc = "Entwürfe"  // precomposed, as Mail.app returns it live
	raw := "imap://00000000-0000-0000-0000-000000000000/Entwu%CC%88rfe"

	got := gmailSnapshotMailbox(raw)

	if got != nfc {
		t.Fatalf("gmailSnapshotMailbox() = %q (NFD), want %q (NFC)", got, nfc)
	}
	if got == nfd {
		t.Fatalf("gmailSnapshotMailbox() returned decomposed NFD form; Mail.app's live pnam property never matches it")
	}
}
