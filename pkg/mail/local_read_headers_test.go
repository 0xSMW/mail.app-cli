package mail

import (
	"net/mail"
	"testing"
)

func TestLocalMIMEReplyHeaders(t *testing.T) {
	m := Message{}
	header := mail.Header{"Subject": {"Example"}, "To": {"reader@example.test"}, "In-Reply-To": {"<original@example.test>"}, "References": {"<parent@example.test> <original@example.test>"}}
	if err := populateMIMEEnvelope(&m, header); err != nil {
		t.Fatal(err)
	}
	if m.InReplyTo != "<original@example.test>" || m.References != "<parent@example.test> <original@example.test>" {
		t.Fatalf("reply headers: %+v", m)
	}
}
