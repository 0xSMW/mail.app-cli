package mailax

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFocusDiagnosticAllowlistAndDeduplication(t *testing.T) {
	raw := json.RawMessage(`{"operation":"ownedfocus","expectedWindowID":"Mail.messageViewer.window.42","focusedIdentifier":"Private subject content","focusedRole":"Private account name","windowIDs":["Mail.messageViewer.window.42","Private window title"],"workspaceFrontPID":100,"axFrontPID":200,"mailPID":200,"ownedIdentifierMatches":true,"sameWindow":true,"subject":"Private email subject","title":"Private window title"}`)
	var output bytes.Buffer
	previous := ""
	emitFocusDiagnostic(&output, raw, &previous)
	emitFocusDiagnostic(&output, raw, &previous)
	text := output.String()
	if strings.Contains(text, "Private") || strings.Count(text, "\n") != 1 {
		t.Fatalf("diagnostic leaked content or duplicated output: %s", text)
	}
	if !strings.Contains(text, `"workspaceFrontPID":100`) || !strings.Contains(text, `"axFrontPID":200`) || !strings.Contains(text, `"sameWindow":true`) {
		t.Fatalf("diagnostic lost focus evidence: %s", text)
	}
	if !strings.Contains(text, `"focusedIdentifier":"other"`) || !strings.Contains(text, `"windowIDs":["Mail.messageViewer.window.42","other"]`) {
		t.Fatalf("unsafe window identifiers were retained: %s", text)
	}
}

func TestFocusDiagnosticRejectsUnrelatedReplies(t *testing.T) {
	for _, raw := range []json.RawMessage{json.RawMessage(`{"operation":"select","subject":"private"}`), json.RawMessage(`{"operation":"private"}`), json.RawMessage(`not-json`)} {
		var output bytes.Buffer
		emitFocusDiagnostic(&output, raw, nil)
		if output.Len() != 0 {
			t.Fatalf("unexpected diagnostic: %s", output.String())
		}
	}
}

func TestFocusDiagnosticShowsNarrowForegroundFallback(t *testing.T) {
	var output bytes.Buffer
	emitFocusDiagnostic(&output, json.RawMessage(`{"operation":"front","workspaceFrontPID":100,"frontPID":200,"frontSource":"process-manager","frontAXStatus":-25204,"axFrontPID":0}`), nil)
	text := output.String()
	if !strings.Contains(text, `"frontPID":200`) || !strings.Contains(text, `"frontSource":"process-manager"`) || !strings.Contains(text, `"frontAXStatus":-25204`) {
		t.Fatalf("fallback evidence missing: %s", text)
	}
}
