package mail

import "testing"

// TestMailboxLeafFromURLNormalizesToNFC: the Envelope Index stores IMAP
// folder names in NFD (decomposed) form, e.g. the German "Entwürfe" is
// stored as "Entwu" + U+0308 COMBINING DIAERESIS + "rfe" (percent-encoded
// as Entwu%CC%88rfe). Confirmed directly against a real index:
//
//	$ sqlite3 "~/Library/Mail/V10/MailData/Envelope Index" \
//	    "select url from mailboxes where url like '%ntw%rfe%';"
//	imap://.../Entwu%CC%88rfe
//
// mailboxExact() compares this name against Mail.app's live Apple Events
// "pnam" property, which returns NFC (precomposed "ü"). The mismatched forms
// are never equal under Go's == even though they render identically, so a
// mailbox name with any combining diacritic never resolves when the name
// came from the index. mailboxLeafFromURL must normalize to NFC so every
// consumer (locate.go, gmail_labels_snapshot.go, verification_snapshot.go)
// gets the same form Mail.app itself returns.
func TestMailboxLeafFromURLNormalizesToNFC(t *testing.T) {
	const nfd = "Entwürfe"  // decomposed, as stored in the index
	const nfc = "Entwürfe"   // precomposed, as Mail.app returns it live
	rawURL := "imap://00000000-0000-0000-0000-000000000000/Entwu%CC%88rfe"

	got := mailboxLeafFromURL(rawURL)

	if got != nfc {
		t.Fatalf("mailboxLeafFromURL() = %q (NFD), want %q (NFC)", got, nfc)
	}
	if got == nfd {
		t.Fatalf("mailboxLeafFromURL() returned decomposed NFD form; Mail.app's live pnam property never matches it")
	}
}
