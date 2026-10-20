package mail

import "testing"

func TestAppleDraftUUIDPreservesSignatureAndAttachmentContracts(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		valid       bool
	}{
		{"lowercase", "01234567-89ab-cdef-0123-456789abcdef", true},
		{"uppercase", "01234567-89AB-CDEF-0123-456789ABCDEF", true},
		{"shape without version restriction", "ffffffff-ffff-ffff-ffff-ffffffffffff", true},
		{"empty", "", false},
		{"missing hyphen", "0123456789ab-cdef-0123-456789abcdef", false},
		{"wrong separator", "01234567_89ab-cdef-0123-456789abcdef", false},
		{"nonhex", "01234567-89ab-cdef-0123-456789abcdeg", false},
		{"trailing whitespace", "01234567-89ab-cdef-0123-456789abcdef ", false},
		{"embedded newline", "01234567-89ab-cdef-0123-456789abcde\n", false},
		{"non-ASCII lookalike", "０1234567-89ab-cdef-0123-456789abcdef", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAppleDraftUUID(tc.value); got != tc.valid {
				t.Fatalf("attachment UUID accepted=%v, want %v", got, tc.valid)
			}
			if got, want := reconstructibleAppleDraftHeader("X-Apple-Mail-Signature", tc.value), tc.valid || tc.value == ""; got != want {
				t.Fatalf("signature accepted=%v, want %v", got, want)
			}
		})
	}
}
