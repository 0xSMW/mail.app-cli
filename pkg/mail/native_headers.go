package mail

import (
	"fmt"
	netmail "net/mail"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

// messageHeaders reads Mail's public header text without requesting a rendered
// body. Unsupported header properties remain optional; access and transport
// errors cannot establish that reply headers are absent.
func (n *nativeMail) messageHeaders(ref appleevents.Value, message *Message) error {
	headers, err := n.stringProperty("alhe", ref)
	if err != nil {
		if nativeOptionalProperty(err) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(headers) == "" {
		return nil
	}
	parsed, err := netmail.ReadMessage(strings.NewReader(headers + "\r\n\r\n"))
	if err != nil {
		return fmt.Errorf("parse Mail message headers: %w", err)
	}
	message.InReplyTo = strings.TrimSpace(parsed.Header.Get("In-Reply-To"))
	message.References = strings.TrimSpace(parsed.Header.Get("References"))
	return nil
}
