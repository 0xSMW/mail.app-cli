package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/clierr"
	"github.com/0xSMW/mail.app-cli/v3/internal/output"
	"github.com/0xSMW/mail.app-cli/v3/pkg/mail"
	"github.com/spf13/cobra"
)

// ReceiptStatus reports only durable observations. A journal without a terminal
// event cannot distinguish a running process from one killed without cleanup.
type ReceiptStatus struct {
	Action        string   `json:"action"`
	Phase         string   `json:"phase"`
	State         string   `json:"state"`
	Complete      bool     `json:"complete"`
	Matched       int      `json:"matched"`
	Attempted     int      `json:"attempted"`
	Succeeded     int      `json:"succeeded"`
	Failed        int      `json:"failed"`
	Skipped       int      `json:"skipped"`
	NotAttempted  int      `json:"notAttempted"`
	Unverified    int      `json:"unverified"`
	Planned       int      `json:"planned,omitempty"`
	Receipts      []string `json:"receipts,omitempty"`
	LastAction    string   `json:"lastAction"`
	PartialLine   bool     `json:"partialLine"`
	StoppedReason string   `json:"stoppedReason,omitempty"`
}

func (a *application) newMessagesStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use: "status <receipt-file>", Short: "Inspect durable receipt progress without contacting Mail", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{annotationAgentNotes: "Read-only local receipt inspection. Accepts a live durable batch JSONL journal, final receipt, or JSON envelope. A trailing partial line is ignored and reported. Without a terminal record, running_or_interrupted does not assert process liveness. Never repeats a mutation."},
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := readReceiptStatus(args[0])
			if err != nil {
				return clierr.Usagef("read receipt status: %v", err)
			}
			return a.writer.Write(output.Result{Data: result, Summary: fmt.Sprintf("%s: %s (%d succeeded, %d failed)", result.Action, result.State, result.Succeeded, result.Failed), Plain: func(p *output.Printer) {
				p.Line("%s: %s", result.Action, result.State)
				p.Line("Phase: %s", result.Phase)
				p.Line("Matched: %d; attempted: %d; succeeded: %d; failed: %d; skipped: %d; not attempted: %d; unverified: %d", result.Matched, result.Attempted, result.Succeeded, result.Failed, result.Skipped, result.NotAttempted, result.Unverified)
				p.Line("Last action: %s", result.LastAction)
				if result.StoppedReason != "" {
					p.Line("Stopped: %s", result.StoppedReason)
				}
				if result.PartialLine {
					p.Line("Trailing partial line ignored")
				}
			}})
		},
	}
}

func readReceiptStatus(path string) (ReceiptStatus, error) {
	f, err := os.Open(path)
	if err != nil {
		return ReceiptStatus{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ReceiptStatus{}, err
	}
	if !info.Mode().IsRegular() {
		return ReceiptStatus{}, fmt.Errorf("receipt must be a regular file")
	}
	const max = 64 << 20
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return ReceiptStatus{}, err
	}
	if len(data) > max {
		return ReceiptStatus{}, fmt.Errorf("receipt exceeds 64 MiB")
	}
	return parseReceiptStatus(data)
}

func statusFromBatch(raw json.RawMessage, state string) (ReceiptStatus, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ReceiptStatus{}, err
	}
	for _, key := range []string{"action", "matched", "attempted", "succeeded", "failed", "skipped", "items"} {
		if len(fields[key]) == 0 || bytes.Equal(fields[key], []byte("null")) {
			return ReceiptStatus{}, fmt.Errorf("final receipt missing %s", key)
		}
	}
	var result mail.BatchResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ReceiptStatus{}, err
	}
	if result.Action == "" || result.Matched < 0 || result.Attempted < 0 || result.Succeeded < 0 || result.Failed < 0 || result.Skipped < 0 || result.NotAttempted < 0 || result.Unverified < 0 || len(result.Items) > result.Matched || result.Attempted > result.Matched || result.Succeeded > result.Matched || result.Failed > result.Matched || result.Skipped > result.Matched || result.NotAttempted > result.Matched {
		return ReceiptStatus{}, fmt.Errorf("invalid final receipt counts")
	}
	seen := map[string]bool{}
	succeeded, failed, skipped, notAttempted := 0, 0, 0, 0
	for _, item := range result.Items {
		key := item.Account + "\x00" + item.ID
		if item.ID == "" || seen[key] {
			return ReceiptStatus{}, fmt.Errorf("invalid final receipt item")
		}
		seen[key] = true
		switch item.Status {
		case "succeeded":
			succeeded++
		case "failed", "unknown":
			failed++
			if item.MarkReadOnSkip {
				skipped++
			}
		case "skipped", "dry-run":
			skipped++
		case "not_attempted":
			notAttempted++
		default:
			return ReceiptStatus{}, fmt.Errorf("invalid final receipt item status %q", item.Status)
		}
	}
	if succeeded != result.Succeeded || failed != result.Failed || skipped != result.Skipped || notAttempted != result.NotAttempted {
		return ReceiptStatus{}, fmt.Errorf("final receipt counts disagree with items")
	}
	if state == "" {
		state = "completed"
		if result.StoppedReason != "" {
			state = "stopped"
		}
		for _, item := range result.Items {
			if item.Status == "unknown" {
				state = "interrupted"
			}
		}
	}
	return ReceiptStatus{Action: result.Action, Phase: state, State: state, Complete: state == "completed", Matched: result.Matched, Attempted: result.Attempted, Succeeded: result.Succeeded, Failed: result.Failed, Skipped: result.Skipped, NotAttempted: result.NotAttempted, Unverified: result.Unverified, StoppedReason: result.StoppedReason, LastAction: state}, nil
}

func parseReceiptStatus(data []byte) (ReceiptStatus, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) == nil && len(object["event"]) == 0 {
		if raw, ok := object["data"]; ok {
			return statusFromReceipt(raw)
		}
		return statusFromReceipt(data)
	}
	if firstJournalEvent(data) == "manifest_started" {
		return parseManifestReceiptStatus(data)
	}
	status := ReceiptStatus{State: "running_or_interrupted"}
	lines := bytes.Split(data, []byte{'\n'})
	if len(lines[len(lines)-1]) > 0 {
		status.PartialLine = true
	}
	lines = lines[:len(lines)-1] // Only newline-terminated journal records are durable.
	started, terminal := false, false
	batchStarted := false
	cleanupStarted := false
	items := map[string]mail.BatchItem{}
	attempted := map[string]bool{}
	for n, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if terminal {
			return status, fmt.Errorf("record follows terminal event at line %d", n+1)
		}
		var event struct {
			Event        string          `json:"event"`
			Action       string          `json:"action"`
			Matched      *int            `json:"matched"`
			Item         *mail.BatchItem `json:"item"`
			Result       json.RawMessage `json:"result"`
			LabelResults json.RawMessage `json:"labelResults"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return status, fmt.Errorf("malformed journal line %d: %w", n+1, err)
		}
		status.Phase = event.Event
		status.LastAction = event.Event
		if event.Event == "started" || event.Event == "label_cleanup_started" {
			if batchStarted || event.Event == "label_cleanup_started" && cleanupStarted || event.Action == "" || event.Matched == nil || *event.Matched < 0 {
				return status, fmt.Errorf("invalid started record")
			}
			if started && (status.Action != event.Action || status.Matched != *event.Matched) {
				return status, fmt.Errorf("start records disagree")
			}
			if event.Event == "started" {
				batchStarted = true
			} else {
				cleanupStarted = true
			}
			started = true
			status.Action = event.Action
			status.Matched = *event.Matched
			continue
		}
		if !started {
			return status, fmt.Errorf("journal lacks started record")
		}
		switch event.Event {
		case "label_cleanup_result":
			if !cleanupStarted || batchStarted || len(event.LabelResults) == 0 {
				return status, fmt.Errorf("invalid label cleanup result")
			}
			var labels []mail.GmailLabelResult
			if err := json.Unmarshal(event.LabelResults, &labels); err != nil {
				return status, fmt.Errorf("invalid label cleanup results")
			}
			continue
		case "completed", "stopped", "interrupted":
			final, err := statusFromBatch(event.Result, event.Event)
			if err != nil {
				return status, fmt.Errorf("invalid terminal record: %w", err)
			}
			if final.Action != status.Action || final.Matched != status.Matched {
				return status, fmt.Errorf("terminal record disagrees with started record")
			}
			final.PartialLine = status.PartialLine
			status = final
			terminal = true
			continue
		case "manifest_before_read", "manifest_after_read", "identity_capture_started", "identity_capture_result", "mutation_started", "mutation_succeeded", "mutation_failed", "mutation_unknown", "mark_read_started", "mark_read_succeeded", "mark_read_failed", "verification_result", "preflight_failed", "skipped", "not_attempted":
			if event.Item == nil || event.Item.ID == "" {
				return status, fmt.Errorf("%s record lacks item", event.Event)
			}
			item := *event.Item
			key := item.Account + "\x00" + item.ID
			status.LastAction = event.Event + " " + item.ID
			if strings.HasPrefix(event.Event, "mutation_") || event.Event == "preflight_failed" || strings.HasPrefix(event.Event, "mark_read_") && !item.MarkReadOnSkip {
				attempted[key] = true
			}
			switch event.Event {
			case "mutation_started":
				item.Status = "unknown"
			case "mutation_succeeded":
				item.Status = "succeeded"
			case "mutation_failed", "preflight_failed", "mark_read_failed":
				item.Status = "failed"
			case "mutation_unknown":
				item.Status = "unknown"
			case "skipped":
				item.Status = "skipped"
			case "not_attempted":
				item.Status = "not_attempted"
			}
			items[key] = item
		default:
			return status, fmt.Errorf("unknown journal event %q", event.Event)
		}
	}
	if !started {
		return status, fmt.Errorf("no complete started record")
	}
	if terminal {
		return status, nil
	}
	status.Attempted = len(attempted)
	for _, item := range items {
		switch item.Status {
		case "succeeded":
			status.Succeeded++
		case "failed":
			status.Failed++
		case "skipped", "dry-run":
			status.Skipped++
		case "not_attempted":
			status.NotAttempted++
		case "unknown":
			status.Unverified++
		}
		if item.VerifyError != "" && item.Status != "unknown" {
			status.Unverified++
		}
	}
	if len(items) > status.Matched || status.Attempted > status.Matched {
		return status, fmt.Errorf("journal counts exceed selection")
	}
	return status, nil
}

func statusFromReceipt(raw json.RawMessage) (ReceiptStatus, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ReceiptStatus{}, err
	}
	if _, ok := fields["verified"]; ok {
		return statusFromManifest(raw)
	}
	return statusFromBatch(raw, "")
}

func statusFromManifest(raw json.RawMessage) (ReceiptStatus, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return ReceiptStatus{}, err
	}
	for _, key := range []string{"version", "matched", "verified", "failed", "planned", "notAttempted", "items"} {
		if len(fields[key]) == 0 || bytes.Equal(fields[key], []byte("null")) {
			return ReceiptStatus{}, fmt.Errorf("manifest receipt missing %s", key)
		}
	}
	var result mail.ManifestResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ReceiptStatus{}, err
	}
	if result.Version != 1 || result.Matched < 0 || len(result.Items) != result.Matched || result.Verified < 0 || result.Verified > result.Matched || result.Failed < 0 || result.Failed > result.Matched || result.Planned < 0 || result.Planned > result.Matched || result.NotAttempted < 0 || result.NotAttempted > result.Matched {
		return ReceiptStatus{}, fmt.Errorf("invalid manifest counts")
	}
	status := ReceiptStatus{Action: "manifest", Phase: "manifest_completed", State: "completed", Complete: true, Matched: result.Matched, Succeeded: result.Verified, Failed: result.Failed, Planned: result.Planned, NotAttempted: result.NotAttempted, LastAction: "manifest_completed"}
	seen := map[string]bool{}
	for _, item := range result.Items {
		key := item.Account + "\x00" + item.ID
		if item.Account == "" || item.ID == "" || seen[key] {
			return status, fmt.Errorf("invalid manifest result item")
		}
		seen[key] = true
		switch item.Status {
		case "verified", "unverified", "failed", "planned", "not_attempted", "succeeded", "unknown":
		default:
			return status, fmt.Errorf("invalid manifest item status %q", item.Status)
		}
		if item.MutationStatus == "unknown" {
			status.State = "interrupted"
			status.Complete = false
		}
	}
	if status.NotAttempted > 0 && status.State != "interrupted" {
		status.State = "stopped"
		status.Complete = false
	}
	for _, batch := range result.Batches {
		status.Attempted += batch.Attempted
		status.Unverified += batch.Unverified
		if batch.ReceiptPath != "" {
			status.Receipts = append(status.Receipts, batch.ReceiptPath)
		}
	}
	if status.Attempted > status.Matched {
		return status, fmt.Errorf("manifest attempts exceed selection")
	}
	return status, nil
}

func parseManifestReceiptStatus(data []byte) (ReceiptStatus, error) {
	status := ReceiptStatus{Action: "manifest", State: "running_or_interrupted"}
	lines := bytes.Split(data, []byte{'\n'})
	status.PartialLine = len(lines[len(lines)-1]) > 0
	lines = lines[:len(lines)-1]
	started, terminal := false, false
	children := map[string]bool{}
	for n, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if terminal {
			return status, fmt.Errorf("record follows manifest terminal at line %d", n+1)
		}
		var event struct {
			Event       string                `json:"event"`
			Manifest    *mail.CleanupManifest `json:"manifest"`
			ReceiptPath string                `json:"receiptPath"`
			Indices     []int                 `json:"indices"`
			Result      json.RawMessage       `json:"result"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return status, fmt.Errorf("malformed manifest journal line %d: %w", n+1, err)
		}
		status.Phase = event.Event
		status.LastAction = event.Event
		switch event.Event {
		case "manifest_started":
			if started || event.Manifest == nil || event.Manifest.Version != 1 || len(event.Manifest.Items) == 0 {
				return status, fmt.Errorf("invalid manifest started record")
			}
			started = true
			status.Matched = len(event.Manifest.Items)
		case "manifest_batch_started":
			if !started || event.ReceiptPath == "" || len(event.Indices) == 0 {
				return status, fmt.Errorf("invalid manifest batch started record")
			}
			if _, ok := children[event.ReceiptPath]; ok {
				return status, fmt.Errorf("duplicate manifest child receipt")
			}
			indices := map[int]bool{}
			for _, i := range event.Indices {
				if i < 0 || i >= status.Matched || indices[i] {
					return status, fmt.Errorf("invalid manifest group indices")
				}
				indices[i] = true
			}
			children[event.ReceiptPath] = false
			status.Receipts = append(status.Receipts, event.ReceiptPath)
			status.LastAction = event.Event + " " + event.ReceiptPath
		case "manifest_batch_completed":
			complete, ok := children[event.ReceiptPath]
			if !started || !ok || complete {
				return status, fmt.Errorf("unknown or repeated manifest child completion")
			}
			batch, err := statusFromBatch(event.Result, "")
			if err != nil {
				return status, err
			}
			children[event.ReceiptPath] = true
			status.Attempted += batch.Attempted
			status.Succeeded += batch.Succeeded
			status.Failed += batch.Failed
			status.Skipped += batch.Skipped
			status.NotAttempted += batch.NotAttempted
			status.Unverified += batch.Unverified
			status.LastAction = event.Event + " " + event.ReceiptPath
		case "manifest_completed":
			if !started {
				return status, fmt.Errorf("manifest terminal lacks start")
			}
			final, err := statusFromManifest(event.Result)
			if err != nil {
				return status, err
			}
			if final.Matched != status.Matched {
				return status, fmt.Errorf("manifest terminal disagrees with start")
			}
			final.PartialLine = status.PartialLine
			status = final
			terminal = true
		default:
			return status, fmt.Errorf("unknown manifest event %q", event.Event)
		}
	}
	if !started {
		return status, fmt.Errorf("no complete manifest start")
	}
	return status, nil
}

func firstJournalEvent(data []byte) string {
	for {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			return ""
		}
		line := bytes.TrimSpace(data[:end])
		data = data[end+1:]
		if len(line) == 0 {
			continue
		}
		var record struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(line, &record) != nil {
			return ""
		}
		return record.Event
	}
}
