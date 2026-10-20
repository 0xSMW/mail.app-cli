package mail

import (
	"fmt"
	"strings"
	"time"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

// Native search delegates matching to Mail's typed object test. A body term
// is evaluated by Mail only when the query asks for it; returned rows contain
// envelope metadata and never request the content renderer.
func nativeSearchPredicate(query, since string, phrase, body bool) (appleevents.Value, error) {
	var terms []string
	if phrase {
		terms = []string{query}
	} else {
		terms = searchTerms(query)
	}
	if len(terms) == 0 {
		return appleevents.Value{}, nil
	}
	tests := make([]appleevents.Value, 0, len(terms)+2)
	for _, term := range terms {
		fields := []string{"subj", "sndr"}
		if body {
			fields = append(fields, "ctnt")
		}
		alternatives := make([]appleevents.Value, 0, len(fields))
		for _, field := range fields {
			alternatives = append(alternatives, appleevents.Comparison("cont", appleevents.Property(field, appleevents.Examined()), appleevents.String(term)))
		}
		tests = append(tests, appleevents.Logical("OR  ", alternatives...))
	}
	tests = append(tests, appleevents.Comparison("=   ", appleevents.Property("isdl", appleevents.Examined()), appleevents.Bool(false)))
	if strings.TrimSpace(since) != "" {
		unix, _, err := parseSinceUnix(since)
		if err != nil {
			return appleevents.Value{}, err
		}
		tests = append(tests, appleevents.Comparison(">=  ", appleevents.Property("rdrc", appleevents.Examined()), appleevents.Date(time.Unix(unix, 0))))
	}
	return appleevents.Logical("AND ", tests...), nil
}

func (n *nativeMail) searchMailbox(predicate appleevents.Value, account, mailbox string, limit int) ([]Message, error) {
	acc, err := n.account(account)
	if err != nil {
		return nil, err
	}
	box, err := n.mailbox(acc, mailbox)
	if err != nil {
		return nil, err
	}
	// Only the potentially long-running search query uses its configured
	// timeout. Resolution, metadata reads, and mutation events keep defaults;
	// the operation context still caps the complete search.
	var v appleevents.Value
	if err := n.phase("search_mailbox", func() error {
		var err error
		v, err = n.eventWithTimeout("core", "getd", map[string]appleevents.Value{
			"----": appleevents.ByTest("mssg", predicate, box),
		}, mailSearchTimeout())
		return err
	}); err != nil {
		return nil, err
	}
	if v.Kind != "list" {
		return nil, fmt.Errorf("Mail returned %s for search results", v.Kind)
	}
	refs := v.Items
	if limit > 0 && len(refs) > limit {
		refs = refs[:limit]
	}
	result := make([]Message, 0, len(refs))
	for _, ref := range refs {
		message, err := n.messageMetadata(ref, account, mailbox)
		if err != nil {
			return nil, err
		}
		result = append(result, *message)
	}
	return result, nil
}

func (c *Client) nativeSearchMailbox(query, account, mailbox string, limit int, since string, body bool) ([]Message, error) {
	predicate, err := nativeSearchPredicate(query, since, false, body)
	if err != nil {
		return nil, err
	}
	if predicate.Kind == "" {
		return []Message{}, nil
	}
	if limit <= 0 {
		limit = 50
	}
	var result []Message
	err = c.withNativeAutomation(mailSearchTimeout(), func(n *nativeMail) error {
		var err error
		result, err = n.searchMailbox(predicate, account, mailbox, limit)
		return err
	})
	return result, err
}

func (c *Client) nativeSearchLegacy(query string, limit int) ([]Message, error) {
	predicate, err := nativeSearchPredicate(query, "", true, true)
	if err != nil {
		return nil, err
	}
	var result = []Message{}
	err = c.withNativeAutomation(mailSearchTimeout(), func(n *nativeMail) error {
		accounts, err := n.accounts()
		if err != nil {
			return err
		}
		for _, account := range accounts {
			acc, err := n.account(account.Name)
			if err != nil {
				return err
			}
			boxes, err := n.mailboxRefs(acc)
			if err != nil {
				return err
			}
			for _, box := range boxes {
				remaining := limit
				if limit > 0 {
					remaining -= len(result)
					if remaining <= 0 {
						return nil
					}
				}
				messages, err := n.searchMailbox(predicate, account.Name, box.path, remaining)
				if err != nil {
					return err
				}
				result = append(result, messages...)
			}
		}
		return nil
	})
	return result, err
}

func (c *Client) nativeIdentityCount(account, mailbox string, identity StableIdentity) (int, error) {
	if !identity.hasFallback() {
		return 0, fmt.Errorf("stable identity lacks complete metadata; refusing mailbox-wide RFC lookup in %s", mailbox)
	}
	if validRFCMessageID(identity.RFCMessageID) != "" && identity.rfcEnvelopeKey() == "" {
		return 0, fmt.Errorf("RFC stable identity lacks a valid sender address")
	}
	sent, ok := ParseMessageTime(identity.DateSent)
	if !ok {
		return 0, fmt.Errorf("invalid stable identity sent date")
	}
	// Exact envelope fields bound the candidate set. Size may vary after a
	// server move only when a live RFC header proves the same message.
	tests := []appleevents.Value{
		appleevents.Comparison("=   ", appleevents.Property("subj", appleevents.Examined()), appleevents.String(identity.Subject)),
		appleevents.Comparison("=   ", appleevents.Property("drcv", appleevents.Examined()), appleevents.Date(sent)),
		appleevents.Comparison("=   ", appleevents.Property("isdl", appleevents.Examined()), appleevents.Bool(false)),
	}
	if validRFCMessageID(identity.RFCMessageID) == "" {
		tests = append(tests, appleevents.Comparison("=   ", appleevents.Property("msze", appleevents.Examined()), appleevents.Int(int64(identity.MessageSize))))
	}
	predicate := appleevents.Logical("AND ", tests...)
	matches := 0
	err := c.withNativeAutomation(identityEnrichmentTimeout, func(n *nativeMail) error {
		acc, err := n.account(account)
		if err != nil {
			return err
		}
		box, err := n.mailbox(acc, mailbox)
		if err != nil {
			return err
		}
		v, err := n.get(appleevents.ByTest("mssg", predicate, box))
		if err != nil {
			return err
		}
		if v.Kind != "list" {
			return fmt.Errorf("Mail returned %s for identity candidates", v.Kind)
		}
		if len(v.Items) > maxVerificationIdentityCandidates {
			return fmt.Errorf("stable identity has too many metadata candidates in %s", mailbox)
		}
		for _, ref := range v.Items {
			m, err := n.messageMetadata(ref, account, mailbox)
			if err != nil {
				return err
			}
			if validRFCMessageID(identity.RFCMessageID) != "" && StableIdentityFromMessage(*m).RFCMessageID == "" {
				return fmt.Errorf("RFC identity candidate %s lacks a valid live header", m.ID)
			}
			if m.Deleted || !identity.matchesEnvelope(*m) {
				continue
			}
			matches++
			if matches == 2 {
				break
			}
		}
		return nil
	})
	return matches, err
}

func (c *Client) nativeGmailCapability(account string) (bool, error) {
	found := false
	err := c.withNativeAutomation(defaultAutomationTimeout, func(n *nativeMail) error {
		acc, err := n.account(account)
		if err != nil {
			return err
		}
		boxes, err := n.mailboxRefs(acc)
		if err != nil {
			return err
		}
		for _, box := range boxes {
			if strings.EqualFold(box.name, "All Mail") {
				found = true
				break
			}
		}
		return nil
	})
	return found, err
}
