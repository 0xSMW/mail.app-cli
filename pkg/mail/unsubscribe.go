package mail

import (
	"encoding/json"
	"fmt"
	"net/mail"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// GetUnsubscribeJSON inventories controls from the complete available local MIME
// source. Metadata resolution may load account inventory; no message automation
// fallback, remote content, or unsubscribe action is performed.
func (c *Client) GetUnsubscribeJSON(account, mailbox, id string) (*UnsubscribeObservation, error) {
	seed := Message{ID: id, Account: account, Mailbox: mailbox}
	if err := c.Done(); err != nil {
		return nil, err
	}
	message, _, err := c.getMessageDetailsFromDiskMode(account, mailbox, id, true)
	if message == nil {
		observation := unsubscribeObservation(seed, []string{mailbox})
		if err != nil {
			observation.Error = err.Error()
			return &observation, err
		}
		observation.Error = "Message is unavailable in the requested local scope."
		return &observation, fmt.Errorf("local message scope unavailable: %s/%s/%s", account, mailbox, id)
	}
	observation := unsubscribeObservation(*message, []string{mailbox})
	if err != nil {
		observation.Error = err.Error()
	}
	return &observation, err
}

// ReadLocalUnsubscribeMessages resolves ownership once for a bounded batch and
// reads at most four local files at a time. Cancellation returns only attempted
// inputs, allowing callers to keep unattempted IDs pending in checkpoints.
func (c *Client) ReadLocalUnsubscribeMessages(messages []ScanMessage) ([]UnsubscribeObservation, error) {
	if err := c.Done(); err != nil {
		return nil, err
	}
	unique := make([]ScanMessage, 0, len(messages))
	idAccounts := map[string]string{}
	positions := map[string]int{}
	for _, item := range messages {
		if account, exists := idAccounts[item.ID]; exists && account != item.Account {
			return nil, fmt.Errorf("local message ID %s has conflicting input account scopes", item.ID)
		}
		idAccounts[item.ID] = item.Account
		key := item.Account + "\x00" + item.ID
		if i, ok := positions[key]; ok {
			unique[i].Mailboxes = sortedUnique(append(unique[i].Mailboxes, append(item.Mailboxes, item.Mailbox)...))
			continue
		}
		item.Mailboxes = sortedUnique(append(append([]string{}, item.Mailboxes...), item.Mailbox))
		if item.Mailbox == "" && len(item.Mailboxes) > 0 {
			item.Mailbox = item.Mailboxes[0]
		}
		positions[key] = len(unique)
		unique = append(unique, item)
	}
	envelopes := make([]Message, len(unique))
	for i := range unique {
		envelopes[i] = unique[i].Message
	}
	paths, err := c.messageFilePathsForMessages("", "", envelopes)
	if err != nil {
		return nil, err
	}
	results := make([]UnsubscribeObservation, len(unique))
	attempted := make([]bool, len(unique))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				if c.Done() != nil {
					continue
				}
				item := unique[index]
				m := item.Message
				// Do not inherit stale link data supplied by a previous observation.
				m.Links = nil
				m.LinkStatus = nil
				m.Unsubscribe = nil
				m.Content = ""
				m.ContentError = ""
				m.BodyDiagnostics = nil
				if path, ok := paths[m.ID]; ok {
					_, readErr := c.hydrateLocalMessage(&m, path)
					if readErr != nil {
						m.ContentError = readErr.Error()
					}
				}
				results[index] = unsubscribeObservation(m, item.Mailboxes)
				attempted[index] = true
			}
		}()
	}
dispatch:
	for i := range unique {
		select {
		case <-c.Context().Done():
			break dispatch
		case jobs <- i:
		}
	}
	close(jobs)
	workers.Wait()
	observations := make([]UnsubscribeObservation, 0, len(unique))
	for i := range unique {
		if attempted[i] {
			observations = append(observations, results[i])
		}
	}
	return observations, c.Done()
}

func unsubscribeObservation(m Message, mailboxes []string) UnsubscribeObservation {
	o := UnsubscribeObservation{ID: m.ID, RFCMessageID: m.RFCMessageID, Account: m.Account, Mailboxes: sortedUnique(mailboxes), Sender: m.Sender, Subject: m.Subject, DateReceived: m.DateReceived, HeaderStatus: "unavailable", Error: m.ContentError, Destinations: []UnsubscribeDestination{}}
	if m.LinkStatus != nil {
		o.Complete = m.LinkStatus.Complete
		o.Warnings = append([]string{}, m.LinkStatus.Warnings...)
	}
	if o.Complete && m.BodyDiagnostics != nil && m.BodyDiagnostics.ExtractionError == "message file holds no text body" {
		o.Error = ""
	}
	if m.Unsubscribe != nil {
		o.HeaderStatus = m.Unsubscribe.HeaderStatus
		o.ListID = canonicalListID(m.Unsubscribe.ListID)
		o.OneClickAdvertised = m.Unsubscribe.OneClickAdvertised
		if m.Unsubscribe.ListID != "" && o.ListID == "" {
			o.Warnings = append(o.Warnings, "List-Id is malformed; grouping uses provisional sender evidence.")
		}
	}
	if o.HeaderStatus != "available" {
		o.Complete = false
		if o.Error == "" {
			o.Error = "Local message source is unavailable; headers and body were not inspected."
		}
	}
	if m.BodyDiagnostics != nil && len(m.BodyDiagnostics.linkRenderableParts) == 0 {
		o.Complete = false
		o.Warnings = append(o.Warnings, "No supported local text or HTML body was available; additional controls cannot be ruled out.")
	}
	destinations := map[string]*UnsubscribeDestination{}
	for _, link := range m.Links {
		if link.Kind != "unsubscribe" && link.Kind != "preferences" {
			continue
		}
		destination := destinations[link.URL]
		if destination == nil {
			destination = &UnsubscribeDestination{URL: link.URL, Kind: link.Kind, Openable: link.Openable}
			destinations[link.URL] = destination
		}
		if link.Kind == "unsubscribe" {
			destination.Kind = "unsubscribe"
		}
		destination.Openable = destination.Openable || link.Openable
		destination.Evidence = append(destination.Evidence, UnsubscribeEvidence{MessageID: m.ID, Source: link.Source, MIMEPart: link.MIMEPart, Text: link.Text, Kind: link.Kind, Selected: link.Selected})
		parsed, e := url.Parse(link.URL)
		if e == nil && strings.EqualFold(parsed.Scheme, "https") && link.Source == "header" && link.Kind == "unsubscribe" && link.Openable {
			o.HasHTTPSUnsubscribe = true
		}
	}
	for _, destination := range destinations {
		destination.Evidence = uniqueEvidence(destination.Evidence)
		o.Destinations = append(o.Destinations, *destination)
	}
	sortDestinations(o.Destinations)
	return o
}

func canonicalListID(raw string) string {
	raw = strings.TrimSpace(raw)
	start, end := strings.LastIndex(raw, "<"), strings.LastIndex(raw, ">")
	if start < 0 || end != len(strings.TrimSpace(raw))-1 || end <= start+1 || strings.ContainsAny(raw[:start], "<>") {
		return ""
	}
	id := raw[start+1 : end]
	if !strings.Contains(id, ".") || strings.ContainsAny(id, "@<> \t\r\n") {
		return ""
	}
	for _, part := range strings.Split(id, ".") {
		if part == "" || part[0] == '-' || part[len(part)-1] == '-' {
			return ""
		}
		for _, ch := range part {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return ""
			}
		}
	}
	return strings.ToLower(id)
}
func sortedUnique(values []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}
	sort.Strings(result)
	return result
}
func uniqueEvidence(values []UnsubscribeEvidence) []UnsubscribeEvidence {
	keyed := map[string]UnsubscribeEvidence{}
	for _, value := range values {
		encoded, _ := json.Marshal(value)
		keyed[string(encoded)] = value
	}
	keys := make([]string, 0, len(keyed))
	for key := range keyed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]UnsubscribeEvidence, 0, len(keys))
	for _, key := range keys {
		result = append(result, keyed[key])
	}
	return result
}
func sortDestinations(values []UnsubscribeDestination) {
	sort.Slice(values, func(i, j int) bool { return values[i].URL < values[j].URL })
}
func normalizedSender(sender string) string {
	if address, err := mail.ParseAddress(sender); err == nil {
		return strings.ToLower(address.Address)
	}
	return strings.ToLower(strings.TrimSpace(sender))
}
func unsubscribeGroupKey(o UnsubscribeObservation) (string, string, bool) {
	listID := canonicalListID("<" + o.ListID + ">")
	if listID != "" {
		return encodedGroupKey("list-id", o.Account, listID), "list-id", false
	}
	hosts := []string{}
	for _, d := range o.Destinations {
		for _, e := range d.Evidence {
			if e.Source == "header" {
				if u, err := url.Parse(d.URL); err == nil && u.Hostname() != "" {
					hosts = append(hosts, strings.ToLower(u.Hostname()))
				}
			}
		}
	}
	hosts = sortedUnique(hosts)
	sender := normalizedSender(o.Sender)
	if len(hosts) > 0 {
		return encodedGroupKey("sender-header-hosts", o.Account, sender, strings.Join(hosts, ",")), "sender-header-hosts", true
	}
	return encodedGroupKey("sender", o.Account, sender), "sender", true
}
func encodedGroupKey(parts ...string) string {
	encoded, _ := json.Marshal(parts)
	return string(encoded)
}
func newerObservation(a, b UnsubscribeObservation) bool {
	at, ae := time.Parse(time.RFC3339Nano, a.DateReceived)
	bt, be := time.Parse(time.RFC3339Nano, b.DateReceived)
	if ae == nil && be == nil && !at.Equal(bt) {
		return at.After(bt)
	}
	if ae == nil && be != nil {
		return true
	}
	if ae != nil && be == nil {
		return false
	}
	if a.DateReceived != b.DateReceived {
		return a.DateReceived > b.DateReceived
	}
	return a.ID > b.ID
}
func destinationSet(o UnsubscribeObservation) string {
	values := []string{}
	for _, d := range o.Destinations {
		values = append(values, d.URL)
	}
	return strings.Join(sortedUnique(values), "\x00")
}

// GroupUnsubscribeObservations keeps every control and local evidence record.
// List-ID groups are advertised identity, not remote verification; sender-based
// fallbacks remain explicitly provisional.
func GroupUnsubscribeObservations(observations []UnsubscribeObservation) []UnsubscribeGroup {
	// First merge repeated local IDs observed through multiple folder memberships.
	local := map[string]UnsubscribeObservation{}
	observations = append([]UnsubscribeObservation{}, observations...)
	sort.Slice(observations, func(i, j int) bool {
		if observations[i].Account != observations[j].Account {
			return observations[i].Account < observations[j].Account
		}
		if observations[i].ID != observations[j].ID {
			return observations[i].ID < observations[j].ID
		}
		a, _ := json.Marshal(observations[i])
		b, _ := json.Marshal(observations[j])
		return string(a) < string(b)
	})
	for _, o := range observations {
		key := encodedGroupKey(o.Account, o.ID)
		if prior, ok := local[key]; ok {
			o.Mailboxes = sortedUnique(append(append([]string{}, prior.Mailboxes...), o.Mailboxes...))
			o.Destinations = mergeUnsubscribeDestinations(prior.Destinations, o.Destinations)
			o.Warnings = sortedUnique(append(append([]string{}, prior.Warnings...), o.Warnings...))
			o.Complete = o.Complete || prior.Complete
			o.HasHTTPSUnsubscribe = o.HasHTTPSUnsubscribe || prior.HasHTTPSUnsubscribe
			o.OneClickAdvertised = o.OneClickAdvertised || prior.OneClickAdvertised
			if o.ListID == "" {
				o.ListID = prior.ListID
			}
			if o.RFCMessageID == "" {
				o.RFCMessageID = prior.RFCMessageID
			}
			if o.Sender == "" {
				o.Sender = prior.Sender
			}
			if o.Subject == "" {
				o.Subject = prior.Subject
			}
			if o.DateReceived == "" {
				o.DateReceived = prior.DateReceived
			}
			if prior.HeaderStatus == "available" {
				o.HeaderStatus = "available"
			}
			if o.Complete {
				o.Error = ""
			}
		}
		o.Mailboxes = sortedUnique(o.Mailboxes)
		local[key] = o
	}
	groups := map[string]*UnsubscribeGroup{}
	keys := []string{}
	for _, o := range local {
		if len(o.Destinations) == 0 {
			continue
		}
		key, source, provisional := unsubscribeGroupKey(o)
		g := groups[key]
		if g == nil {
			g = &UnsubscribeGroup{Key: key, KeySource: source, Provisional: provisional, Account: o.Account, ListID: o.ListID}
			groups[key] = g
			keys = append(keys, key)
		}
	}
	// Only controls establish a candidate group. Once established, include
	// matching observations even when their current destination set is empty.
	// Computing the full key avoids guessing sender-only messages into groups
	// whose identity also depends on header destination hosts.
	for _, o := range local {
		key, _, _ := unsubscribeGroupKey(o)
		if g := groups[key]; g != nil {
			g.Messages = append(g.Messages, o)
		}
	}
	sort.Strings(keys)
	result := make([]UnsubscribeGroup, 0, len(keys))
	for _, key := range keys {
		g := groups[key]
		sort.Slice(g.Messages, func(i, j int) bool { return newerObservation(g.Messages[i], g.Messages[j]) })
		g.Latest = g.Messages[0]
		firstSet := destinationSet(g.Latest)
		counted := map[string]bool{}
		var destinations []UnsubscribeDestination
		for _, o := range g.Messages {
			g.Senders = append(g.Senders, o.Sender)
			identity := o.ID
			if o.RFCMessageID != "" {
				identity = o.RFCMessageID
			}
			counted[encodedGroupKey(o.Account, identity)] = true
			destinations = append(destinations, o.Destinations...)
			if destinationSet(o) != firstSet {
				g.DestinationsVary = true
			}
		}
		g.Senders = sortedUnique(g.Senders)
		g.Destinations = mergeUnsubscribeDestinations(nil, destinations)
		g.MessageCount = len(counted)
		result = append(result, *g)
	}
	return result
}
func mergeUnsubscribeDestinations(a, b []UnsubscribeDestination) []UnsubscribeDestination {
	byURL := map[string]UnsubscribeDestination{}
	for _, d := range append(append([]UnsubscribeDestination{}, a...), b...) {
		if prior, ok := byURL[d.URL]; ok {
			d.Evidence = append(append([]UnsubscribeEvidence{}, prior.Evidence...), d.Evidence...)
			d.Openable = d.Openable || prior.Openable
			if prior.Kind == "unsubscribe" {
				d.Kind = "unsubscribe"
			}
		}
		d.Evidence = uniqueEvidence(d.Evidence)
		byURL[d.URL] = d
	}
	result := make([]UnsubscribeDestination, 0, len(byURL))
	for _, d := range byURL {
		result = append(result, d)
	}
	sortDestinations(result)
	return result
}
