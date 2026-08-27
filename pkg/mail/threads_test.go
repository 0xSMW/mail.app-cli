package mail

import "testing"

func TestGroupThreadsKeepsSubjectlessMessagesApart(t *testing.T) {
	messages := []Message{
		{ID: "123", Subject: "", Sender: "a@example.com"},
		{ID: "7", Subject: "Message-123", Sender: "b@example.com"},
		{ID: "8", Subject: "Re: message-123", Sender: "c@example.com"},
	}
	threads := GroupThreads(messages)
	if len(threads) != 2 {
		t.Fatalf("expected 2 threads, got %d: %+v", len(threads), threads)
	}
	byID := map[string]ThreadSummary{}
	for _, thread := range threads {
		byID[thread.ID] = thread
	}
	alone, ok := byID["Message-123"]
	if !ok || alone.Count != 1 || alone.Synthetic {
		t.Fatalf("subjectless message not kept alone: %+v", byID)
	}
	subject, ok := byID["message-123"]
	if !ok || subject.Count != 2 || !subject.Synthetic {
		t.Fatalf("subject thread not grouped as synthetic: %+v", byID)
	}
	if ThreadArchiveAllowed(subject) {
		t.Fatal("multi-message subject thread must not be archivable as a unit")
	}
}
