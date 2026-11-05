package mail

import (
	"bytes"
	"fmt"
	netmail "net/mail"
	"regexp"
	"strings"
)

// Older senders emitted an empty encoded display name or failed to quote a
// display name before an explicit mailbox. Recover only an unambiguous single
// angle address. Never search free text, display names, or bodies for addresses.
var emptyRecipientEncodedWord = regexp.MustCompile(`=\?[^?\s]+\?[bBqQ]\?\?=`)

func parseRecipientAddressList(parser *netmail.AddressParser, raw string) ([]*netmail.Address, error) {
	addresses, originalErr := parser.ParseList(raw)
	if originalErr == nil {
		return addresses, nil
	}
	if strings.Count(raw, "<") != 1 || strings.Count(raw, ">") != 1 {
		return nil, originalErr
	}
	start, end := strings.IndexByte(raw, '<'), strings.IndexByte(raw, '>')
	if end <= start || strings.TrimSpace(raw[end+1:]) != "" {
		return nil, originalErr
	}
	explicit, err := parser.Parse(raw[start : end+1])
	if err != nil || !usableRecipientAddress(explicit.Address) {
		return nil, originalErr
	}
	prefix := strings.TrimSpace(raw[:start])
	emptyName := strings.TrimSpace(emptyRecipientEncodedWord.ReplaceAllString(prefix, "")) == ""
	sameMailbox := strings.EqualFold(prefix, explicit.Address)
	displayOnly := prefix != "" && !strings.ContainsAny(prefix, "@,;:<>")
	if !emptyName && !sameMailbox && !displayOnly {
		return nil, originalErr
	}
	return []*netmail.Address{{Address: explicit.Address}}, nil
}
func usableRecipientAddress(address string) bool {
	at := strings.LastIndexByte(address, '@')
	return at > 0 && at < len(address)-1
}

func recipientFieldFailure(field recipientField, source string, err error) {
	state := "unavailable"
	if len(*field.values) > 0 {
		state = "partial"
		if field.status.Source != "" {
			source = field.status.Source
		}
	}
	*field.status = RecipientFieldStatus{State: state, Source: source, Error: err.Error()}
}
func populateRecipientHeaderPrefix(m *Message, header netmail.Header, source string, headerErr error) {
	observed := Message{}
	populateRecipientHeaders(&observed, header, source)
	// A broken block may omit later repetitions or entire fields. Retain only
	// positive evidence and merge it with any prior successful observation.
	for _, field := range recipientFields(&observed) {
		recipientFieldFailure(field, source, headerErr)
	}
	mergeRecipientEvidence(m, observed)
}
func populateMailRecipientCollection(field recipientField, values []string) {
	parser := &netmail.AddressParser{WordDecoder: headerWordDecoder()}
	parsed := []string{}
	var invalid error
	for _, value := range values {
		address, err := parser.Parse(value)
		if err != nil || !usableRecipientAddress(address.Address) {
			invalid = fmt.Errorf("Mail returned an invalid %s recipient address", field.name)
			continue
		}
		parsed = append(parsed, address.Address)
	}
	if invalid != nil {
		appendRecipientFieldEvidence(field, parsed, "mail")
		recipientFieldFailure(field, "mail", invalid)
	} else {
		*field.values = parsed
		*field.status = RecipientFieldStatus{State: "available", Source: "mail"}
	}
}

// parseRecipientHeaderPrefix accepts only the already bounded prefix. The
// malformed line is excluded, and its text never enters a diagnostic error.
func parseRecipientHeaderPrefix(prefix []byte) (netmail.Header, error) {
	if len(prefix) == 0 {
		return nil, fmt.Errorf("message header block starts with a malformed line")
	}
	input := append(append([]byte(nil), prefix...), '\n')
	message, err := netmail.ReadMessage(bytes.NewReader(input))
	if err != nil {
		return nil, fmt.Errorf("message header prefix is malformed")
	}
	return message.Header, fmt.Errorf("message header block has a malformed line or missing body separator")
}
func recipientHeaderLine(line string, hasField bool) bool {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return hasField
	}
	colon := strings.IndexByte(line, ':')
	if colon <= 0 {
		return false
	}
	for _, b := range []byte(line[:colon]) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}

// Incomplete observations add proof; they cannot erase previously validated
// addresses from another partial observation of the same message field.
func appendRecipientEvidence(existing, observed []string) []string {
	values := append([]string{}, existing...)
	for _, address := range observed {
		found := false
		for _, known := range values {
			if strings.EqualFold(address, known) {
				found = true
				break
			}
		}
		if !found {
			values = append(values, address)
		}
	}
	return values
}

func appendRecipientFieldEvidence(field recipientField, values []string, source string) {
	if len(values) == 0 {
		return
	}
	previousSource, previousCount := field.status.Source, len(*field.values)
	*field.values = appendRecipientEvidence(*field.values, values)
	if previousCount == 0 || previousSource == "" {
		field.status.Source = source
	} else if previousSource != source {
		field.status.Source = "mixed"
	}
}
