package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testClient(server *httptest.Server) *Client {
	return NewClient(server.Client(), TokenSourceFunc(func(context.Context) (string, error) { return "secret-bearer", nil }), WithBaseURL(server.URL))
}

func TestLabelsAndMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-bearer" {
			t.Error("missing authorization")
		}
		switch r.URL.Path {
		case "/labels":
			if r.Method != http.MethodGet {
				t.Error("labels method")
			}
			io.WriteString(w, `{"labels":[{"id":"Label_1","name":"Customers","type":"user"},{"id":"INBOX","name":"INBOX","type":"system"}]}`)
		case "/messages/abc":
			if r.URL.Query().Get("format") != "metadata" || len(r.URL.Query()["metadataHeaders"]) != 4 {
				t.Error("metadata projection")
			}
			io.WriteString(w, `{"id":"abc","threadId":"thread","labelIds":["INBOX"],"payload":{"headers":[{"name":"Message-ID","value":"<a@b>"}]},"sizeEstimate":42}`)
		default:
			t.Error("unexpected path")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := testClient(server)
	labels, err := client.ListLabels(context.Background())
	if err != nil || len(labels) != 2 || labels[0].Name != "Customers" {
		t.Fatalf("labels: %+v %v", labels, err)
	}
	message, err := client.GetMessage(context.Background(), "abc")
	if err != nil || message.ID != "abc" || message.Payload.Headers[0].Value != "<a@b>" || message.SizeEstimate != 42 {
		t.Fatalf("message: %+v %v", message, err)
	}
}

func TestFindMessagesAllPagesAndSpam(t *testing.T) {
	var count int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		if r.URL.Query().Get("q") != "rfc822msgid:a@b" || r.URL.Query().Get("includeSpamTrash") != "true" {
			t.Error("identity query")
		}
		if r.URL.Query().Get("pageToken") == "" {
			io.WriteString(w, `{"messages":[{"id":"one"}],"nextPageToken":"second"}`)
		} else {
			io.WriteString(w, `{"messages":[{"id":"one"},{"id":"two"}]}`)
		}
	}))
	defer server.Close()
	ids, err := testClient(server).FindMessages(context.Background(), "<a@b>")
	if err != nil || len(ids) != 2 || count != 2 {
		t.Fatalf("ids=%v calls=%d err=%v", ids, count, err)
	}
}

func TestFindMessagesFailsClosedOnPaginationLoopAndLimit(t *testing.T) {
	for _, mode := range []string{"loop", "limit"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "loop" {
					io.WriteString(w, `{"messages":[{"id":"one"}],"nextPageToken":"same"}`)
					return
				}
				messages := make([]map[string]string, 1001)
				for i := range messages {
					messages[i] = map[string]string{"id": fmt.Sprint(i)}
				}
				json.NewEncoder(w).Encode(map[string]any{"messages": messages})
			}))
			defer server.Close()
			ids, err := testClient(server).FindMessages(context.Background(), "a@b")
			if err == nil || ids != nil {
				t.Fatalf("partial results escaped: %v %v", ids, err)
			}
		})
	}
}

func TestModifyAcceptanceDoesNotClaimLabelsChanged(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/messages/abc/modify" {
			t.Error("mutation method/path")
		}
		var body struct {
			Add    []string `json:"addLabelIds"`
			Remove []string `json:"removeLabelIds"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Add) != 1 || body.Add[0] != "Label_1" {
			t.Error("mutation body")
		}
		// A successful API reply can still contain the previous label set.
		io.WriteString(w, `{"id":"abc","labelIds":["INBOX"]}`)
	}))
	defer server.Close()
	if err := testClient(server).ModifyLabels(context.Background(), "abc", []string{"Label_1"}, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected read/retry: %d", calls.Load())
	}
}

func TestMutationIsNotRetriedAndFailureIsUncertain(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		connection.Close()
	}))
	defer server.Close()
	err := testClient(server).ModifyLabels(context.Background(), "abc", []string{"Label_1"}, nil)
	var failure *RequestError
	if !errors.As(err, &failure) || !failure.Uncertain || calls.Load() != 1 {
		t.Fatalf("calls=%d err=%v", calls.Load(), err)
	}
}

func TestRedirectsDoNotForwardBearerOrReplayMutation(t *testing.T) {
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	err := testClient(server).ModifyLabels(context.Background(), "abc", []string{"Label_1"}, nil)
	if err == nil || leaked.Load() {
		t.Fatalf("redirect followed: %v", err)
	}
}

func TestErrorsRedactBodiesAndEnforceResponseBounds(t *testing.T) {
	for _, mode := range []string{"status", "oversize", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "status":
					w.WriteHeader(401)
					io.WriteString(w, "secret-bearer private body")
				case "oversize":
					io.WriteString(w, strings.Repeat("x", maxResponseBytes+1))
				default:
					io.WriteString(w, "secret-bearer invalid-json")
				}
			}))
			defer server.Close()
			_, err := testClient(server).ListLabels(context.Background())
			if err == nil || strings.Contains(err.Error(), "secret-bearer") || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}

func TestMetadataIDMismatchAndCanceledMutation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{"id":"other"}`) }))
	defer server.Close()
	if _, err := testClient(server).GetMessage(context.Background(), "abc"); err == nil {
		t.Fatal("mismatched metadata accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := testClient(server).ModifyLabels(ctx, "abc", []string{"Label_1"}, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("canceled mutation dispatched")
	}
}
