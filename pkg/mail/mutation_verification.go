package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StableIdentity identifies a logical message across Mail.app local-ID
// changes. RFCMessageID is used only when it is an actual RFC header; the
// fallback is deliberately all-or-nothing to avoid matching a nearby mail.
type StableIdentity struct {
	RFCMessageID string `json:"rfcMessageId,omitempty"`
	Sender       string `json:"sender,omitempty"`
	Subject      string `json:"subject,omitempty"`
	DateSent     string `json:"dateSent,omitempty"`
	MessageSize  int    `json:"messageSize,omitempty"`
}

func StableIdentityFromMessage(message Message) StableIdentity {
	return StableIdentity{
		RFCMessageID: validRFCMessageID(message.RFCMessageID),
		Sender:       strings.TrimSpace(message.Sender),
		Subject:      strings.TrimSpace(message.Subject),
		DateSent:     canonicalIdentityDate(message.DateSent),
		MessageSize:  message.MessageSize,
	}
}

func canonicalIdentityDate(value string) string {
	value = strings.TrimSpace(value)
	if parsed, ok := ParseMessageTime(value); ok {
		return parsed.UTC().Format(time.RFC3339)
	}
	return value
}

func validRFCMessageID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 3 || value[0] != '<' || value[len(value)-1] != '>' || strings.ContainsAny(value[1:len(value)-1], " \t\r\n<>") {
		return ""
	}
	return value
}

func (identity StableIdentity) hasFallback() bool {
	return identity.Sender != "" && identity.Subject != "" && identity.DateSent != "" && identity.MessageSize > 0
}

func (identity StableIdentity) valid() bool {
	return identity.RFCMessageID != "" || identity.hasFallback()
}

func (c *Client) captureStableIdentity(item BatchItem) (StableIdentity, error) {
	identity := item.Identity
	identity.Sender = strings.TrimSpace(identity.Sender)
	identity.Subject = strings.TrimSpace(identity.Subject)
	identity.DateSent = canonicalIdentityDate(identity.DateSent)
	identity.RFCMessageID = validRFCMessageID(identity.RFCMessageID)
	if !identity.valid() {
		identity = StableIdentity{Sender: strings.TrimSpace(item.Sender), Subject: strings.TrimSpace(item.Subject), DateSent: canonicalIdentityDate(item.DateSent), MessageSize: item.MessageSize}
	}
	// Mail's header property is the only source accepted as an RFC Message-ID.
	// The Envelope Index's numeric message_id is a local database value.
	message, err := c.GetMessageDetailsForVerificationJSON(item.Account, item.SourceMailbox, item.ID)
	return completeStableIdentity(identity, message, err)
}

// completeStableIdentity treats the live RFC read as enrichment. A complete
// index-captured fallback is already safe and must not be discarded merely
// because Mail.app did not answer the optional header read in time.
func completeStableIdentity(identity StableIdentity, message *Message, enrichmentErr error) (StableIdentity, error) {
	if enrichmentErr != nil {
		if identity.valid() {
			return identity, nil
		}
		return StableIdentity{}, enrichmentErr
	}
	if message != nil {
		fromMail := StableIdentityFromMessage(*message)
		if fromMail.RFCMessageID != "" {
			identity.RFCMessageID = fromMail.RFCMessageID
		}
		if !identity.hasFallback() {
			identity.Sender, identity.Subject = fromMail.Sender, fromMail.Subject
			identity.DateSent, identity.MessageSize = fromMail.DateSent, fromMail.MessageSize
		}
	}
	if !identity.valid() {
		return StableIdentity{}, fmt.Errorf("message has neither an RFC Message-ID nor a complete sender+subject+sent-date+size identity")
	}
	return identity, nil
}

// verificationBackoff is deliberately small but bounded: Mail and IMAP can
// accept a move before the destination row is visible in the Envelope Index.
var verificationBackoff = []time.Duration{0, 100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}

type verificationPresence struct {
	Source      bool
	Destination bool
}

type verificationLookup func() (verificationPresence, error)

func verifyRelocationWithLookup(ctx context.Context, item BatchItem, lookup verificationLookup, pause func(time.Duration)) (string, error) {
	for attempt, delay := range verificationBackoff {
		if attempt > 0 {
			pause(delay)
		}
		if err := ctx.Err(); err != nil {
			return "unknown_after_timeout", err
		}
		presence, err := lookup()
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isAutomationTimeout(err) {
				return "unknown_after_timeout", err
			}
			return "unknown_after_timeout", err
		}
		if presence.Destination {
			return "confirmed_destination", nil
		}
		if !presence.Source && isValidGmailInboxTransition(item) {
			return "confirmed_source_removed", nil
		}
		if attempt == len(verificationBackoff)-1 {
			if presence.Source {
				return "present_in_source", fmt.Errorf("message still present in %s", item.SourceMailbox)
			}
			return "source_removed_destination_unverified", fmt.Errorf("message left %s but was not found in %s", item.SourceMailbox, item.TargetMailbox)
		}
	}
	return "unknown_after_timeout", context.DeadlineExceeded
}

func isAutomationTimeout(err error) bool {
	var timeout *AutomationTimeoutError
	return errors.As(err, &timeout)
}

func isValidGmailInboxTransition(item BatchItem) bool {
	return item.GmailInboxSource && strings.EqualFold(item.SourceMailbox, "INBOX") && strings.EqualFold(item.TargetMailbox, "All Mail")
}
