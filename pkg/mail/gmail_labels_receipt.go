package mail

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"
)

// ReadGmailLabelReceipt returns the durable pre-dispatch state, never a later
// success claim. A partial final line after interruption cannot erase it.
func ReadGmailLabelReceipt(path string) (GmailLabelResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return GmailLabelResult{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return GmailLabelResult{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return GmailLabelResult{}, fmt.Errorf("Gmail receipt must be a regular file at most 4 MiB")
	}
	r := bufio.NewReader(io.LimitReader(f, 4<<20))
	var out GmailLabelResult
	found := false
	for {
		line, readErr := r.ReadBytes('\n')
		if readErr != nil && readErr != io.EOF {
			return out, readErr
		}
		if len(line) > 0 {
			var event struct {
				Event  string           `json:"event"`
				Result GmailLabelResult `json:"result"`
			}
			if err := json.Unmarshal(line, &event); err != nil {
				if readErr == io.EOF && found {
					break
				}
				return out, fmt.Errorf("invalid Gmail receipt JSON")
			}
			if event.Event == "gmail_label_dispatch" || event.Event == "gmail_label_noop" {
				if found {
					return out, fmt.Errorf("receipt contains more than one Gmail dispatch")
				}
				out = event.Result
				found = true
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	if !found || out.Account == "" || gmailEmail(out.ProviderEmail) == "" || out.ProviderMessageID == "" || gmailRFCMessageID(out.Identity.RFCMessageID) == "" || !out.Identity.hasFallback() || out.Label.ID == "" || out.BeforeLabelIDs == nil || out.MailBefore == nil || len(out.MailBefore.LocalIDs) != 1 || out.MailBefore.Memberships == nil {
		return out, fmt.Errorf("receipt lacks complete Gmail dispatch identity and baseline")
	}
	out.ReceiptPath = path
	out.Identity.RFCMessageID = gmailRFCMessageID(out.Identity.RFCMessageID)
	return out, nil
}

// Reconcile only reads provider and Mail state. It never dispatches or retries
// a mutation, and never appends to or truncates the original receipt.
func (w *GmailLabelWorkflow) Reconcile(ctx context.Context, path string) (GmailLabelResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	out, err := ReadGmailLabelReceipt(path)
	if err != nil {
		return gmailResultError(out, err)
	}
	out.ProviderConfirmed = false
	out.MailStatus = "pending"
	out.MailAfter = nil
	email, err := w.bind(ctx, out.Account)
	if err != nil {
		out.Status = "unknown"
		return gmailResultError(out, err)
	}
	if email != gmailEmail(out.ProviderEmail) {
		out.Status = "unknown"
		return gmailResultError(out, fmt.Errorf("current Gmail profile differs from receipt profile"))
	}
	labels, err := w.Provider.ListLabels(ctx)
	if err != nil {
		out.Status = "unknown"
		return gmailResultError(out, err)
	}
	label, err := resolveGmailLabel(labels, out.Label.ID)
	if err != nil {
		out.Status = "unknown"
		return gmailResultError(out, err)
	}
	out.Label = label
	out.Labels = labels
	out, err = w.observe(ctx, out)
	return gmailResultError(out, err)
}
