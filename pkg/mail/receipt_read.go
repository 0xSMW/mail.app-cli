package mail

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// ReadRelocationReceipt accepts a final receipt, CLI envelope, or durable
// JSON-lines journal. It never talks to Mail and refuses incomplete identities.
func ReadRelocationReceipt(path string) (BatchResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return BatchResult{}, err
	}
	defer f.Close()
	const maxSize = 64 << 20
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return BatchResult{}, err
	}
	if len(data) > maxSize {
		return BatchResult{}, fmt.Errorf("receipt exceeds 64 MiB")
	}
	var result BatchResult
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &envelope) == nil && len(envelope.Data) > 0 {
		if err := json.Unmarshal(envelope.Data, &result); err != nil {
			return result, fmt.Errorf("decode receipt: %w", err)
		}
	} else if json.Unmarshal(data, &result) != nil || result.Action == "" {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 4096), maxSize)
		order := []string{}
		items := map[string]BatchItem{}
		pendingReads := map[string]bool{}
		for scanner.Scan() {
			if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
				continue
			}
			var event struct {
				Event          string       `json:"event"`
				Action         string       `json:"action"`
				DryRun         bool         `json:"dryRun"`
				MarkReadBefore bool         `json:"markReadBefore"`
				Matched        int          `json:"matched"`
				Item           *BatchItem   `json:"item"`
				Result         *BatchResult `json:"result"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				return result, fmt.Errorf("decode journal: %w", err)
			}
			if event.Event == "started" {
				result.Action, result.DryRun, result.MarkReadRequested = event.Action, event.DryRun, event.MarkReadBefore
				result.Matched = event.Matched
			}
			if event.Result != nil {
				result = *event.Result
			}
			if event.Item != nil {
				// A crash after mutation_started can occur after Mail applied it
				// but before the completion record. Preserve that uncertainty for
				// read-only reconciliation instead of silently ignoring the item.
				if event.Event == "mutation_started" {
					event.Item.Status = "unknown"
				}
				key := event.Item.Account + "\x00" + event.Item.ID
				switch event.Event {
				case "mark_read_started":
					pendingReads[key] = true
				case "mark_read_succeeded", "mark_read_failed":
					delete(pendingReads, key)
				}
				if _, ok := items[key]; !ok {
					order = append(order, key)
				}
				items[key] = *event.Item
			}
		}
		if err := scanner.Err(); err != nil {
			return result, fmt.Errorf("read journal: %w", err)
		}
		if len(pendingReads) > 0 {
			return result, fmt.Errorf("journal contains an uncertain mark-read phase; inspect source read state before reconciliation (no relocation is inferred)")
		}
		if len(result.Items) == 0 {
			for _, key := range order {
				result.Items = append(result.Items, items[key])
			}
			for _, item := range result.Items {
				switch item.Status {
				case "succeeded":
					result.Attempted++
					result.Succeeded++
				case "unknown", "failed":
					result.Attempted++
					result.Failed++
				case "skipped":
					result.Skipped++
				}
			}
		}
	}
	if result.DryRun {
		return result, fmt.Errorf("a dry-run receipt has no applied relocation to reconcile")
	}
	if result.Action != "archive" && result.Action != "move" {
		return result, fmt.Errorf("reconciliation requires an archive or move receipt")
	}
	if len(result.Items) == 0 {
		return result, fmt.Errorf("receipt contains no message identities")
	}
	eligible := 0
	for _, item := range result.Items {
		if item.Status == "skipped" && item.MarkReadOnSkip && item.MarkedRead {
			if item.Account == "" || item.SourceMailbox == "" || item.ID == "" {
				return result, fmt.Errorf("skipped read item lacks account, mailbox or ID")
			}
			eligible++
		}
		if item.Status != "succeeded" && item.Status != "unknown" {
			continue
		}
		if item.Account == "" || item.SourceMailbox == "" || item.TargetMailbox == "" || !item.Identity.valid() {
			return result, fmt.Errorf("receipt item %q lacks an account, relocation scopes, or stable identity", item.ID)
		}
		if item.MarkedRead {
			result.MarkReadRequested = true
		}
		eligible++
	}
	if eligible == 0 {
		return result, fmt.Errorf("receipt contains no applied or uncertain identities to reconcile")
	}
	return result, nil
}
