package mail

import "testing"

// TestEscapeMailboxURLPathFormsCoversBothNormalizations is the mirror image
// of TestMailboxLeafFromURLNormalizesToNFC: a caller typically types a
// mailbox name in NFC (precomposed), e.g. in a shell, but the Envelope
// Index stores IMAP folder names in whichever form the account's IMAP
// server reported them — observed as NFD (decomposed) for at least one real
// account, while other fixtures in this package model NFC. A SQL condition
// built from only one form can miss a real mailbox, so the exact/broad
// condition builders must check both.
func TestEscapeMailboxURLPathFormsCoversBothNormalizations(t *testing.T) {
	const nfc = "Entwürfe"        // precomposed, as a caller would type it
	const wantNFCEscaped = "Entw%C3%BCrfe" // percent-encoded NFC
	const wantNFDEscaped = "Entwu%CC%88rfe" // percent-encoded NFD, as observed in a real index

	got := escapeMailboxURLPathForms(nfc)

	if len(got) != 2 {
		t.Fatalf("escapeMailboxURLPathForms(%q) = %v, want 2 forms (NFC and NFD differ for this name)", nfc, got)
	}
	foundNFC, foundNFD := false, false
	for _, form := range got {
		if form == wantNFCEscaped {
			foundNFC = true
		}
		if form == wantNFDEscaped {
			foundNFD = true
		}
	}
	if !foundNFC || !foundNFD {
		t.Fatalf("escapeMailboxURLPathForms(%q) = %v, want both %q and %q", nfc, got, wantNFCEscaped, wantNFDEscaped)
	}
}

// TestEscapeMailboxURLPathFormsCollapsesForASCII keeps the common case (no
// combining marks) to a single comparison, so the exact/broad SQL
// conditions stay exactly as they were before this existed.
func TestEscapeMailboxURLPathFormsCollapsesForASCII(t *testing.T) {
	got := escapeMailboxURLPathForms("INBOX")
	if len(got) != 1 || got[0] != "INBOX" {
		t.Fatalf("escapeMailboxURLPathForms(\"INBOX\") = %v, want a single form [\"INBOX\"]", got)
	}
}
