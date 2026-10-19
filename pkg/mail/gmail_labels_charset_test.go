package mail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/0xSMW/mail.app-cli/v3/internal/gmail"
)

func TestGmailLabelLegacyCharsetSubject(t *testing.T) {
	for _, operation := range []string{"show", "add", "remove"} {
		t.Run(operation, func(t *testing.T) {
			w, p, r := labelWorkflowFixture(t)
			p.message.Payload.Headers[2].Value = "=?windows-1252?Q?Caf=E9_=96_updates?="
			localMessage := w.message
			w.message = func(ctx context.Context, r GmailLabelRequest) (*Message, error) {
				m, err := localMessage(ctx, r)
				m.Subject = "Café – updates"
				return m, err
			}
			var out GmailLabelResult
			var err error
			if operation == "show" {
				out, err = w.Show(context.Background(), r)
			} else {
				r.Add = operation == "add"
				if r.Add {
					r.Label = "Projects/One"
				}
				out, err = w.Change(context.Background(), r)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !out.ProviderConfirmed || out.Identity.Subject != "Café – updates" {
				t.Fatalf("unverified subject: %+v", out)
			}
			if operation == "show" {
				if out.Status != "observed" || p.modifies != 0 || p.gets != 1 {
					t.Fatalf("show: %+v, modifies %d gets %d", out, p.modifies, p.gets)
				}
			} else if out.Status != "changed_verified" || out.MailStatus != "verified" || p.modifies != 1 || p.gets != 2 || hasLabel(out.AfterLabelIDs, out.Label.ID) != r.Add {
				t.Fatalf("%s: %+v, modifies %d gets %d", operation, out, p.modifies, p.gets)
			}
		})
	}
}

func TestGmailLabelLegacyCharsetHTTPWorkflow(t *testing.T) {
	w, p, r := labelWorkflowFixture(t)
	p.message.Payload.Headers[2].Value = "=?windows-1252?Q?Caf=E9_=96_updates?="
	localMessage := w.message
	w.message = func(ctx context.Context, r GmailLabelRequest) (*Message, error) {
		m, err := localMessage(ctx, r)
		m.Subject = "Café – updates"
		return m, err
	}
	var mu sync.Mutex
	getRequests := 0
	localSnapshot := w.snapshot
	w.snapshot = func(ctx context.Context, account string, id StableIdentity) (GmailLabelSnapshot, error) {
		mu.Lock()
		defer mu.Unlock()
		return localSnapshot(ctx, account, id)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if request.Header.Get("Authorization") != "Bearer fake-label-token" {
			t.Error("missing fake authorization")
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		if request.Method == http.MethodGet {
			getRequests++
		}
		response.Header().Set("Content-Type", "application/json")
		var body any
		switch request.Method + " " + request.URL.Path {
		case "GET /profile":
			body = gmail.Profile{EmailAddress: p.email}
		case "GET /labels":
			body = map[string]any{"labels": p.labels}
		case "GET /messages":
			if request.URL.Query().Get("q") != "rfc822msgid:real@example.com" || request.URL.Query().Get("includeSpamTrash") != "true" {
				t.Error("incorrect identity query")
			}
			body = map[string]any{"messages": []map[string]string{{"id": "provider-1"}}}
		case "GET /messages/provider-1":
			if request.URL.Query().Get("format") != "metadata" || len(request.URL.Query()["metadataHeaders"]) != 4 {
				t.Error("incorrect metadata projection")
			}
			p.gets++
			body = p.message
		case "POST /messages/provider-1/modify":
			var delta struct {
				Add    []string `json:"addLabelIds"`
				Remove []string `json:"removeLabelIds"`
			}
			if err := json.NewDecoder(request.Body).Decode(&delta); err != nil {
				t.Error(err)
				response.WriteHeader(http.StatusBadRequest)
				return
			}
			if len(delta.Add)+len(delta.Remove) != 1 || !sameLabels(append(append([]string{}, delta.Add...), delta.Remove...), []string{"Label_1"}) {
				t.Errorf("unexpected mutation: %+v", delta)
			}
			if err := p.ModifyLabels(request.Context(), "provider-1", delta.Add, delta.Remove); err != nil {
				t.Error(err)
				response.WriteHeader(http.StatusInternalServerError)
				return
			}
			body = p.message
		default:
			t.Errorf("unexpected request: %s %s", request.Method, request.URL.Path)
			http.NotFound(response, request)
			return
		}
		if err := json.NewEncoder(response).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	w.Provider = gmail.NewClient(server.Client(), gmail.TokenSourceFunc(func(context.Context) (string, error) {
		return "fake-label-token", nil
	}), gmail.WithBaseURL(server.URL))
	checkCounts := func(gets, metadata, modifies int) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if getRequests != gets || p.gets != metadata || p.modifies != modifies {
			t.Fatalf("GETs %d metadata %d mutations %d, want %d/%d/%d", getRequests, p.gets, p.modifies, gets, metadata, modifies)
		}
	}
	out, err := w.Show(context.Background(), r)
	if err != nil || out.Status != "observed" || !out.ProviderConfirmed {
		t.Fatalf("show: %+v, %v", out, err)
	}
	checkCounts(4, 1, 0)
	r.Label, r.Add = "Projects/One", true
	out, err = w.Change(context.Background(), r)
	if err != nil || out.Status != "changed_verified" || !out.ProviderConfirmed || !hasLabel(out.AfterLabelIDs, "Label_1") {
		t.Fatalf("add: %+v, %v", out, err)
	}
	checkCounts(9, 3, 1)
	r.Add = false
	out, err = w.Change(context.Background(), r)
	if err != nil || out.Status != "changed_verified" || !out.ProviderConfirmed || hasLabel(out.AfterLabelIDs, "Label_1") {
		t.Fatalf("remove: %+v, %v", out, err)
	}
	checkCounts(14, 5, 2)
	receiptBefore, err := os.ReadFile(p.journal)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	p.message.Payload.Headers[2].Value = "=?x-unknown-charset?Q?subject?="
	mu.Unlock()
	r.Add = true
	out, err = w.Change(context.Background(), r)
	if err == nil || !strings.Contains(err.Error(), "Gmail Subject encoding is invalid") || out.ProviderConfirmed {
		t.Fatalf("unknown charset accepted: %+v, %v", out, err)
	}
	checkCounts(18, 6, 2)
	receiptAfter, err := os.ReadFile(p.journal)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(receiptAfter), "gmail_label_dispatch") != strings.Count(string(receiptBefore), "gmail_label_dispatch") {
		t.Fatal("invalid subject added a dispatch receipt")
	}
}

func TestGmailLabelSubjectCharsetFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name, subject string
	}{
		{"unknown charset", "=?x-unknown-charset?Q?subject?="},
		{"invalid encoding", "=?windows-1252?B?%%%?="},
		{"decoded mismatch", "=?windows-1252?Q?Caf=E9?="},
	} {
		for _, operation := range []string{"show", "add", "remove"} {
			t.Run(tc.name+"/"+operation, func(t *testing.T) {
				w, p, r := labelWorkflowFixture(t)
				p.message.Payload.Headers[2].Value = tc.subject
				var out GmailLabelResult
				var err error
				if operation == "show" {
					out, err = w.Show(context.Background(), r)
				} else {
					r.Add = operation == "add"
					if r.Add {
						r.Label = "Projects/One"
					}
					out, err = w.Change(context.Background(), r)
				}
				if err == nil || !strings.Contains(err.Error(), "Gmail Subject") || out.ProviderConfirmed || p.modifies != 0 || p.gets != 1 {
					t.Fatalf("unsafe subject accepted: %+v, err %v, modifies %d gets %d", out, err, p.modifies, p.gets)
				}
				receipt, err := os.ReadFile(p.journal)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(receipt), "gmail_label_dispatch") {
					t.Fatal("invalid subject wrote dispatch receipt")
				}
			})
		}
	}
}
