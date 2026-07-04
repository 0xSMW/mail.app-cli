package cmd

import (
	"strings"

	"github.com/0xSMW/mail.app-cli/pkg/mail"
)

func filterMessagesBySender(messages []mail.Message, sender, senderDomain string) []mail.Message {
	if strings.TrimSpace(sender) == "" && strings.TrimSpace(senderDomain) == "" {
		return messages
	}
	filtered := make([]mail.Message, 0, len(messages))
	for _, message := range messages {
		if messageMatchesSender(message.Sender, sender, senderDomain) {
			filtered = append(filtered, message)
		}
	}
	return filtered
}

func messageMatchesSender(rawSender, sender, senderDomain string) bool {
	normalizedSender := normalizeSender(rawSender)
	if expected := strings.ToLower(strings.TrimSpace(sender)); expected != "" {
		if normalizedSender.Email != expected && normalizedSender.Raw != expected {
			return false
		}
	}
	if expectedDomain := normalizeSenderDomain(senderDomain); expectedDomain != "" {
		if normalizedSender.Domain != expectedDomain && !strings.HasSuffix(normalizedSender.Domain, "."+expectedDomain) {
			return false
		}
	}
	return true
}

type normalizedSender struct {
	Raw    string
	Email  string
	Domain string
}

func normalizeSender(raw string) normalizedSender {
	normalized := normalizedSender{Raw: strings.ToLower(strings.TrimSpace(raw))}
	email := normalized.Raw
	if start := strings.LastIndex(email, "<"); start >= 0 {
		if end := strings.LastIndex(email, ">"); end > start {
			email = email[start+1 : end]
		}
	}
	email = strings.Trim(strings.TrimSpace(email), `"'`)
	normalized.Email = strings.ToLower(email)
	if at := strings.LastIndex(normalized.Email, "@"); at >= 0 && at+1 < len(normalized.Email) {
		normalized.Domain = normalizeSenderDomain(normalized.Email[at+1:])
	}
	return normalized
}

func normalizeSenderDomain(domain string) string {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(domain, "@")
	return domain
}
