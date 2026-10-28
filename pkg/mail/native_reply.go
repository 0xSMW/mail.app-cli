package mail

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/0xSMW/mail.app-cli/v3/internal/appleevents"
)

// ReplyMode selects Mail's public reply or forward command.
type ReplyMode string

const (
	ReplyToSender  ReplyMode = "reply"
	ReplyToAll     ReplyMode = "reply-all"
	ForwardMessage ReplyMode = "forward"
)

// ThreadedOutgoingInput retains the exact original message independently of
// the editable outgoing fields. Mail creates the conversation relationship;
// the outgoing pipeline then applies the user's edited message.
type ThreadedOutgoingInput struct {
	OutgoingInput
	OriginalAccount string    `json:"originalAccount"`
	OriginalMailbox string    `json:"originalMailbox"`
	OriginalID      string    `json:"originalId"`
	Mode            ReplyMode `json:"mode"`
}

// SendThreadedOutgoing submits a reply or forward to Mail. Its receipt records
// Mail's acceptance, with the same guarantees as SendOutgoing.
func (c *Client) SendThreadedOutgoing(input ThreadedOutgoingInput) (*OutgoingResult, error) {
	return c.composeThreadedOutgoing(input, "send")
}

// SaveThreadedOutgoing saves a reply or forward in the originating account's
// Drafts mailbox and verifies the resulting draft identity.
func (c *Client) SaveThreadedOutgoing(input ThreadedOutgoingInput) (*OutgoingResult, error) {
	return c.composeThreadedOutgoing(input, "save")
}

func (c *Client) composeThreadedOutgoing(input ThreadedOutgoingInput, action string) (*OutgoingResult, error) {
	if action == "send" {
		if err := c.checkSendingAllowed(); err != nil {
			return nil, err
		}
	}
	switch input.Mode {
	case ReplyToSender, ReplyToAll, ForwardMessage:
	default:
		return nil, fmt.Errorf("invalid reply mode %q", input.Mode)
	}
	if strings.TrimSpace(input.OriginalAccount) == "" || strings.TrimSpace(input.OriginalMailbox) == "" {
		return nil, fmt.Errorf("original account and mailbox are required")
	}
	id, err := strconv.ParseInt(input.OriginalID, 10, 64)
	if err != nil || id <= 0 {
		return nil, fmt.Errorf("invalid original message ID: %q", input.OriginalID)
	}
	if input.Account != input.OriginalAccount {
		return nil, fmt.Errorf("reply account must match the original message account")
	}
	if !c.nativeEnabled() {
		// The explicitly selected legacy backend retains its existing new-message
		// composition behavior. A native failure never reaches this path.
		return c.composeOutgoing(input.OutgoingInput, action)
	}
	input.OutgoingInput, err = prepareOutgoing(input.OutgoingInput)
	if err != nil {
		return nil, err
	}
	return c.composeNativeOutgoingOwned(input.OutgoingInput, action, func(n *nativeMail, _ OutgoingInput, _, _ string) (appleevents.Value, error) {
		return n.threadedOutgoing(input)
	})
}

func (n *nativeMail) threadedOutgoing(input ThreadedOutgoingInput) (appleevents.Value, error) {
	var original appleevents.Value
	err := n.phase("threaded_lookup", func() error {
		var lookupErr error
		original, lookupErr = n.scopedMessage(input.OriginalAccount, input.OriginalMailbox, input.OriginalID)
		return lookupErr
	})
	if err != nil {
		return appleevents.Value{}, err
	}
	// Codes are defined by Mail.sdef: emal/rpms with rpal, and emal/fwms.
	params := map[string]appleevents.Value{"----": original, "ropw": appleevents.Bool(false)}
	command := "rpms"
	if input.Mode == ForwardMessage {
		command = "fwms"
	} else {
		params["rpal"] = appleevents.Bool(input.Mode == ReplyToAll)
	}
	var outgoing appleevents.Value
	err = n.phase("threaded_create", func() error {
		var createErr error
		outgoing, createErr = n.command("emal", command, params)
		return createErr
	})
	return outgoing, err
}
