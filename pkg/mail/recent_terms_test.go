package mail

import (
	"reflect"
	"testing"
)

func TestMergeRecentTermsPreservesOrderAndOwnsResult(t *testing.T) {
	existing := make([]string, 3, 8)
	copy(existing, []string{" Invoice ", "WORLD", "invoice"})
	next := []string{" world ", " New ", "", "CAFÉ"}
	got := mergeRecentTerms(existing, next)
	if want := []string{"invoice", "world", "new", "café"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("terms = %q, want %q", got, want)
	}
	got[0] = "changed"
	got = append(got, "appended")
	if want := []string{" Invoice ", "WORLD", "invoice"}; !reflect.DeepEqual(existing, want) {
		t.Fatalf("existing was modified: %q", existing)
	}
	if want := []string{" world ", " New ", "", "CAFÉ"}; !reflect.DeepEqual(next, want) {
		t.Fatalf("next was modified: %q", next)
	}
	if got := mergeRecentTerms(nil, []string{" ", ""}); got == nil || len(got) != 0 {
		t.Fatalf("empty terms = %#v, want non-nil empty slice", got)
	}
}
