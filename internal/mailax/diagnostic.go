package mailax

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Keep diagnostic output as an allowlisted structure, independent of arbitrary
// bridge replies. Window titles, subject labels, row values, and account/message
// contents are never included, even when diagnostics are enabled.
type focusDiagnostic struct {
	Operation              string   `json:"operation"`
	ExpectedWindowID       string   `json:"expectedWindowID"`
	AXFrontPID             int      `json:"axFrontPID"`
	FrontPID               int      `json:"frontPID"`
	FrontSource            string   `json:"frontSource"`
	FrontAXStatus          int      `json:"frontAXStatus"`
	WorkspaceFrontPID      int      `json:"workspaceFrontPID"`
	MailPID                int      `json:"mailPID"`
	FocusedIdentifier      string   `json:"focusedIdentifier"`
	FocusedRole            string   `json:"focusedRole"`
	OwnedIdentifierMatches bool     `json:"ownedIdentifierMatches"`
	SameWindow             bool     `json:"sameWindow"`
	MailFrontmostAX        bool     `json:"mailFrontmostAX"`
	WindowIDs              []string `json:"windowIDs"`
	Incomplete             bool     `json:"incomplete,omitempty"`
}

func safeWindowIdentifier(value string) string {
	if value == "" {
		return ""
	}
	const prefix = "Mail.messageViewer.window."
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return "other"
	}
	for _, r := range value[len(prefix):] {
		if r < '0' || r > '9' {
			return "other"
		}
	}
	return value
}

func emitFocusDiagnostic(writer io.Writer, raw json.RawMessage, previous *string) {
	if len(raw) == 0 {
		return
	}
	var diagnostic focusDiagnostic
	if json.Unmarshal(raw, &diagnostic) != nil {
		return
	}
	switch diagnostic.Operation {
	case "prepare", "guard":
	default:
		return
	}
	diagnostic.ExpectedWindowID = safeWindowIdentifier(diagnostic.ExpectedWindowID)
	diagnostic.FocusedIdentifier = safeWindowIdentifier(diagnostic.FocusedIdentifier)
	switch diagnostic.FrontSource {
	case "", "ax", "process-manager":
	default:
		diagnostic.FrontSource = "other"
	}
	switch diagnostic.FocusedRole {
	case "", "AXWindow", "AXSheet", "AXDialog":
	default:
		diagnostic.FocusedRole = "other"
	}
	if len(diagnostic.WindowIDs) > 32 {
		diagnostic.WindowIDs = diagnostic.WindowIDs[:32]
	}
	for i, id := range diagnostic.WindowIDs {
		diagnostic.WindowIDs[i] = safeWindowIdentifier(id)
	}
	encoded, err := json.Marshal(diagnostic)
	if err != nil {
		return
	}
	line := string(encoded)
	if previous != nil {
		if line == *previous {
			return
		}
		*previous = line
	}
	_, _ = fmt.Fprintf(writer, "mail-app-cli-native-archive-focus %s\n", line)
}
