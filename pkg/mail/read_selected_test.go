package mail

import (
	"context"
	"testing"
)

func TestSelectedReadRetainsSuccessesAndEveryFailure(t *testing.T) {
	refs := []MessageRef{{MessageID: "1"}, {MessageID: "2"}, {MessageID: "3"}, {MessageID: "4"}, {MessageID: "5"}}
	result := readSelectedMessages(refs, func(ref MessageRef, i int) (*Message, error) {
		switch ref.MessageID {
		case "2":
			return nil, context.DeadlineExceeded
		case "4":
			return nil, nil
		case "5":
			return &Message{ID: ref.MessageID, ContentError: "not downloaded"}, nil
		default:
			return &Message{ID: ref.MessageID, Content: "body"}, nil
		}
	})
	if result.Complete || len(result.Items) != 5 || result.Items[0].Message.Content != "body" || result.Items[2].Message.Content != "body" {
		t.Fatalf("result = %+v", result)
	}
	for _, i := range []int{1, 3, 4} {
		if result.Items[i].Error == "" {
			t.Fatalf("missing error at %d", i)
		}
	}
}
