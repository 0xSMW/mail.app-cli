package mail

import "testing"

func TestDryRunSummaryCountsOnlyPreviewedItems(t *testing.T) {
	result := BatchResult{
		Action: "move", DryRun: true, Matched: 2, Skipped: 2,
		Items: []BatchItem{
			{ID: "1", Status: "dry-run", TargetMailbox: "Receipts"},
			{ID: "2", Status: "skipped", Error: "already in Receipts"},
		},
	}
	got := result.Summary(BatchOptions{Action: "move", TargetMailbox: "Receipts"})
	if got != "Dry run: would have moved 1 message to Receipts; 1 already there" {
		t.Fatalf("Summary = %q", got)
	}
}
