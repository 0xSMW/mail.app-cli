package mail

import (
	"os"
	"strings"
	"sync"
)

// Mail's Envelope Index holds no summary for many messages — the summaries
// row is simply absent — so index-backed listings show an empty snippet for
// them. The message's own file on disk still carries the body; derive the
// missing one-line summary from it, the way Mail's indexer would.

const (
	// snippetBackfillRunes bounds the snippet derived from a message body.
	snippetBackfillRunes = 200
	// snippetBackfillWindow bounds how much of an extracted body is
	// inspected, keeping the derivation flat for large messages.
	snippetBackfillWindow = 1000
	// snippetBackfillMax bounds how many messages one listing backfills.
	// Listing pages stay far below it; the bound keeps an unbounded listing
	// from becoming an unbounded walk over message files.
	snippetBackfillMax = 1000
	// snippetBackfillWorkers bounds simultaneous message file reads,
	// matching body enrichment.
	snippetBackfillWorkers = 4
)

// backfillSnippetsFromDisk fills the Snippet of listed messages whose Envelope
// Index summary is missing, deriving one line from the message's body file on
// disk. The indexed summary always wins, and the pass is strictly
// best-effort: a missing, partial, or undecodable file leaves the snippet
// empty, and no failure ever fails the listing. BodySourceMail and
// MAIL_APP_CLI_DISABLE_SNIPPET_BACKFILL turn it off.
func (c *Client) backfillSnippetsFromDisk(accountName, mailboxName string, messages []Message) {
	if os.Getenv("MAIL_APP_CLI_DISABLE_SNIPPET_BACKFILL") != "" || c.shared.bodyFromMail {
		return
	}
	pending := make([]int, 0, len(messages))
	for i := range messages {
		if messages[i].Snippet == "" {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return
	}
	if len(pending) > snippetBackfillMax {
		pending = pending[:snippetBackfillMax]
	}
	targets := make([]Message, len(pending))
	for j, i := range pending {
		targets[j] = messages[i]
	}
	paths, err := c.messageFilePathsForMessages(accountName, mailboxName, targets)
	if err != nil {
		return
	}
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < snippetBackfillWorkers && worker < len(pending); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				if c.Done() != nil {
					continue
				}
				path := paths[messages[i].ID]
				if path == "" {
					continue
				}
				body, _, err := readEMLXBodyWithDiagnostics(path)
				if err != nil {
					continue
				}
				if snippet := snippetFromBody(body); snippet != "" {
					messages[i].Snippet = snippet
				}
			}
		}()
	}
	for _, i := range pending {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
}

// snippetFromBody collapses an extracted body into the one-line summary the
// index lacks: whitespace runs become single spaces, and the line is cut to a
// word boundary within snippetBackfillRunes. A body whose opening run is all
// whitespace yields an empty snippet.
func snippetFromBody(body string) string {
	end, seen := len(body), 0
	for i := range body {
		if seen == snippetBackfillWindow {
			end = i
			break
		}
		seen++
	}
	runes := []rune(strings.Join(strings.Fields(body[:end]), " "))
	if len(runes) > snippetBackfillRunes {
		runes = runes[:snippetBackfillRunes]
		if cut := strings.LastIndexByte(string(runes), ' '); cut > snippetBackfillRunes/2 {
			runes = runes[:cut]
		}
	}
	return strings.TrimSpace(string(runes))
}
