package mail

import "strings"

// FilterBySender keeps messages whose sender matches an exact address or a
// domain (including subdomains). Empty filters keep everything.
func FilterBySender(messages []Message, sender, senderDomain string) []Message {
	if strings.TrimSpace(sender) == "" && strings.TrimSpace(senderDomain) == "" {
		return messages
	}
	filtered := make([]Message, 0, len(messages))
	for _, message := range messages {
		if SenderMatches(message.Sender, sender, senderDomain) {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

// SenderMatches reports whether a raw sender header satisfies the filters.
func SenderMatches(rawSender, sender, senderDomain string) bool {
	normalized := ParseSender(rawSender)
	if expected := strings.ToLower(strings.TrimSpace(sender)); expected != "" {
		if normalized.Email != expected && normalized.Raw != expected {
			return false
		}
	}
	if expectedDomain := normalizeSenderDomain(senderDomain); expectedDomain != "" {
		if normalized.Domain != expectedDomain && !strings.HasSuffix(normalized.Domain, "."+expectedDomain) {
			return false
		}
	}
	return true
}

// Sender is a parsed From header.
type Sender struct {
	Raw    string
	Name   string
	Email  string
	Domain string
}

// ParseSender splits "Name <addr@host>" into its parts, lower-casing the
// address. Name keeps its case for display.
func ParseSender(raw string) Sender {
	trimmed := strings.TrimSpace(raw)
	parsed := Sender{Raw: strings.ToLower(trimmed)}
	email := trimmed
	if start := strings.LastIndex(trimmed, "<"); start >= 0 {
		if end := strings.LastIndex(trimmed, ">"); end > start {
			email = trimmed[start+1 : end]
			parsed.Name = strings.Trim(strings.TrimSpace(trimmed[:start]), `"'`)
		}
	}
	parsed.Email = strings.ToLower(strings.Trim(strings.TrimSpace(email), `"'`))
	if at := strings.LastIndex(parsed.Email, "@"); at >= 0 && at+1 < len(parsed.Email) {
		parsed.Domain = normalizeSenderDomain(parsed.Email[at+1:])
	}
	if parsed.Name == "" {
		parsed.Name = parsed.Email
	}
	return parsed
}

func normalizeSenderDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	return strings.TrimPrefix(domain, "@")
}
