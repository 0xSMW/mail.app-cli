package mail

import "testing"

func TestOutgoingBodyPreservesDraftWhitespace(t *testing.T) {
	for _, tc := range []struct {
		actual string
		want   bool
	}{
		{" Body\r\n", true},
		{"\n Body\n", true},
		{"Body\n", false},
		{" Body \n", false},
		{" Body\n\n", false},
	} {
		if got := outgoingBodyMatches(tc.actual, " Body\n"); got != tc.want {
			t.Errorf("match(%q) = %t, want %t", tc.actual, got, tc.want)
		}
	}
}
