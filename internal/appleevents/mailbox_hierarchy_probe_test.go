//go:build darwin && cgo

package appleevents

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// Explicit scope is a known account fixture. Values stay in memory; only
// descriptor kinds, counts, shape checks, and timings are logged.
func TestReadOnlyMailboxHierarchyProbe(t *testing.T) {
	if os.Getenv("MAIL_AE_MAILBOX_HIERARCHY_PROBE") != "1" {
		t.Skip("read-only mailbox hierarchy probe disabled")
	}
	accountName := os.Getenv("MAIL_AE_PROBE_ACCOUNT")
	if accountName == "" {
		t.Fatal("read-only probe requires explicit MAIL_AE_PROBE_ACCOUNT")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	get := func(label string, direct Value) (Value, bool) {
		t.Helper()
		value, stats, err := Send(ctx, Event{Class: "core", ID: "getd", Params: map[string]Value{"----": direct}, Timeout: 5 * time.Second})
		if err != nil {
			var native *Error
			if errors.As(err, &native) {
				t.Logf("probe=%s nativeCode=%d attempted=%t", label, native.Code, native.Attempted)
			} else {
				t.Logf("probe=%s errorType=%T", label, err)
			}
			return Value{}, false
		}
		t.Logf("probe=%s kind=%s itemCount=%d events=%d elapsedMS=%.2f", label, value.Kind, len(value.Items), stats.Events, stats.ElapsedMS)
		return value, true
	}
	account := ByName("mact", accountName, Null())
	matches, ok := get("account-test", ByTest("mact", Comparison("=   ", Property("pnam", Examined()), String(accountName)), Null()))
	if ok {
		t.Logf("account-test validUniqueList=%t", matches.Kind == "list" && len(matches.Items) == 1 && matches.Items[0].Kind == "opaque")
	}
	rootBoxes := All("mbxp", account)
	rootNames, rootOK := get("root-mailbox-names", Property("pnam", rootBoxes))
	childNames, childOK := get("child-mailbox-names", Property("pnam", All("mbxp", rootBoxes)))
	if !rootOK || !childOK {
		return
	}
	rootSet := map[string]bool{}
	rootStringCount := 0
	for _, item := range rootNames.Items {
		if item.Kind == "string" {
			rootStringCount++
			rootSet[item.Text] = true
		}
	}
	childListCount, childStringCount, childInRootCount := 0, 0, 0
	nonemptyChildLists := 0
	for _, item := range childNames.Items {
		if item.Kind == "list" {
			childListCount++
			if len(item.Items) > 0 {
				nonemptyChildLists++
			}
			for _, child := range item.Items {
				if child.Kind == "string" {
					childStringCount++
					if rootSet[child.Text] {
						childInRootCount++
					}
				}
			}
		}
	}
	t.Logf("hierarchy rootStringCount=%d childListCount=%d childStringCount=%d nonemptyChildLists=%d childNamesPresentInRoot=%d outerAligned=%t", rootStringCount, childListCount, childStringCount, nonemptyChildLists, childInRootCount, len(rootNames.Items) == len(childNames.Items))
	rootRefs, rootRefsOK := get("root-mailbox-references", rootBoxes)
	childRefs, childRefsOK := get("child-mailbox-references", All("mbxp", rootBoxes))
	if rootRefsOK && childRefsOK {
		rootReferenceSet := map[string]bool{}
		for _, reference := range rootRefs.Items {
			if reference.Kind == "opaque" {
				rootReferenceSet[reference.Data] = true
			}
		}
		childReferenceCount, childReferencesInRoot := 0, 0
		for _, children := range childRefs.Items {
			if children.Kind == "list" {
				for _, reference := range children.Items {
					if reference.Kind == "opaque" {
						childReferenceCount++
						if rootReferenceSet[reference.Data] {
							childReferencesInRoot++
						}
					}
				}
			}
		}
		t.Logf("hierarchy canonicalRootReferences=%d childReferences=%d exactChildReferencesInRoot=%d", len(rootReferenceSet), childReferenceCount, childReferencesInRoot)
	}

}
