package mail

import "testing"

func TestParseSenderExtractsEmailAndDomain(t *testing.T) {
	got := ParseSender(`Cursor Bot <cursor[bot]@users.noreply.github.com>`)
	if got.Email != "cursor[bot]@users.noreply.github.com" {
		t.Fatalf("Email = %q", got.Email)
	}
	if got.Domain != "users.noreply.github.com" {
		t.Fatalf("Domain = %q", got.Domain)
	}
	if got.Name != "Cursor Bot" {
		t.Fatalf("Name = %q", got.Name)
	}
	if got := ParseSender("plain@example.com"); got.Name != "plain@example.com" {
		t.Fatalf("bare address name = %q", got.Name)
	}
}

func TestFilterBySender(t *testing.T) {
	messages := []Message{
		{ID: "1", Sender: "LinkedIn <jobs-noreply@linkedin.com>"},
		{ID: "2", Sender: "Nextdoor <news@rs.email.nextdoor.com>"},
		{ID: "3", Sender: "plain@example.com"},
	}
	if got := FilterBySender(messages, "", "linkedin.com"); len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("domain filter = %+v", got)
	}
	if got := FilterBySender(messages, "", "nextdoor.com"); len(got) != 1 || got[0].ID != "2" {
		t.Fatalf("subdomain filter = %+v", got)
	}
	if got := FilterBySender(messages, "plain@example.com", ""); len(got) != 1 || got[0].ID != "3" {
		t.Fatalf("sender filter = %+v", got)
	}
	if got := FilterBySender(messages, "jobs-noreply@linkedin.com", "linkedin.com"); len(got) != 1 || got[0].ID != "1" {
		t.Fatalf("combined filter = %+v", got)
	}
}

func TestNormalizedChunkSize(t *testing.T) {
	if got := normalizedChunkSize(12, 5); got != 5 {
		t.Fatalf("chunk size = %d, want 5", got)
	}
	if got := normalizedChunkSize(12, 0); got != 12 {
		t.Fatalf("default chunk size = %d, want 12", got)
	}
	if got := normalizedChunkSize(3, 10); got != 3 {
		t.Fatalf("large chunk size = %d, want 3", got)
	}
}

func TestNormalizeThreadSubject(t *testing.T) {
	tests := map[string]string{
		"Re: Invoice":       "invoice",
		"Fwd: RE:  Invoice": "invoice",
		"  Project   Plan ": "project plan",
		"":                  "",
	}
	for input, want := range tests {
		if got := NormalizeThreadSubject(input); got != want {
			t.Fatalf("NormalizeThreadSubject(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestGroupThreadsMarksSubjectGroupsSynthetic(t *testing.T) {
	threads := GroupThreads([]Message{
		{ID: "1", Subject: "message-release"},
		{ID: "2", Subject: "Re: message-release"},
		{ID: "3", Subject: ""},
	})
	for _, thread := range threads {
		switch thread.ID {
		case "message-release":
			if !thread.Synthetic || ThreadArchiveAllowed(thread) {
				t.Fatalf("subject group starting with message- must stay synthetic: %+v", thread)
			}
		case "message-3":
			if thread.Synthetic {
				t.Fatalf("subjectless message should not be synthetic: %+v", thread)
			}
		}
	}
}

func TestThreadArchiveAllowed(t *testing.T) {
	if ThreadArchiveAllowed(ThreadSummary{ID: "invoice", Synthetic: true, Count: 2}) {
		t.Fatal("subject-only group should not be archivable")
	}
	if !ThreadArchiveAllowed(ThreadSummary{ID: "invoice", Synthetic: true, Count: 1}) {
		t.Fatal("single synthetic message should be archivable")
	}
	if !ThreadArchiveAllowed(ThreadSummary{ID: "message-1", Count: 2}) {
		t.Fatal("non-synthetic group should be archivable")
	}
}
