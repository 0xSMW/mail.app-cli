// Package gmail provides a bounded Gmail metadata and label transport.
package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultBaseURL       = "https://gmail.googleapis.com/gmail/v1/users/me"
	maxResponseBytes     = 4 << 20
	maxCandidateMessages = 1000
	requestTimeout       = 30 * time.Second
)

type Label struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type Payload struct {
	Headers []Header `json:"headers"`
}
type Message struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	LabelIDs     []string `json:"labelIds"`
	Payload      Payload  `json:"payload"`
	SizeEstimate int64    `json:"sizeEstimate"`
}
type Profile struct {
	EmailAddress string `json:"emailAddress"`
}

type API interface {
	Profile(context.Context) (Profile, error)
	ListLabels(context.Context) ([]Label, error)
	FindMessages(context.Context, string) ([]string, error)
	GetMessage(context.Context, string) (Message, error)
	ModifyLabels(context.Context, string, []string, []string) error
}

type TokenSource interface {
	Token(context.Context) (string, error)
}
type TokenSourceFunc func(context.Context) (string, error)

func (f TokenSourceFunc) Token(ctx context.Context) (string, error) { return f(ctx) }

// RequestError never includes response bodies, bearer tokens, or request URLs.
// Uncertain means a mutation was dispatched and its final outcome needs a read.
type RequestError struct {
	Operation  string
	StatusCode int
	Uncertain  bool
}

func (e *RequestError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("Gmail %s failed (HTTP %d)", e.Operation, e.StatusCode)
	}
	return "Gmail " + e.Operation + " failed"
}

type Client struct {
	http    *http.Client
	tokens  TokenSource
	baseURL string
}
type ClientOption func(*Client)

func WithBaseURL(baseURL string) ClientOption {
	return func(c *Client) { c.baseURL = strings.TrimRight(baseURL, "/") }
}
func NewClient(client *http.Client, tokens TokenSource, options ...ClientOption) *Client {
	c := &Client{http: secureHTTPClient(client), tokens: tokens, baseURL: defaultBaseURL}
	for _, option := range options {
		option(c)
	}
	return c
}

// Copy the client so caller-owned redirect policy and timeouts are untouched.
func secureHTTPClient(client *http.Client) *http.Client {
	var result http.Client
	if client != nil {
		result = *client
	}
	result.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if result.Timeout <= 0 || result.Timeout > requestTimeout {
		result.Timeout = requestTimeout
	}
	return &result
}

func (c *Client) request(ctx context.Context, operation, method, path string, query url.Values, body, output any) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.tokens == nil {
		return errors.New("Gmail credentials are required")
	}
	token, err := c.tokens.Token(ctx)
	if err != nil {
		return err
	}
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return errors.New("Gmail access token is invalid")
	}
	u, err := url.Parse(c.baseURL + path)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("Gmail endpoint is invalid")
	}
	u.RawQuery = query.Encode()
	var input io.Reader
	if body != nil {
		data, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return errors.New("Gmail request is invalid")
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), input)
	if err != nil {
		return errors.New("Gmail request is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// A mutation must never be replayed after an ambiguous transport failure.
	if method != http.MethodGet {
		req.GetBody = nil
	}
	response, err := c.http.Do(req)
	if err != nil {
		return &RequestError{Operation: operation, Uncertain: method != http.MethodGet}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &RequestError{Operation: operation, StatusCode: response.StatusCode, Uncertain: method != http.MethodGet}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes {
		return &RequestError{Operation: operation, Uncertain: method != http.MethodGet}
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return &RequestError{Operation: operation, Uncertain: method != http.MethodGet}
	}
	return nil
}

func (c *Client) Profile(ctx context.Context) (Profile, error) {
	var result Profile
	err := c.request(ctx, "profile", http.MethodGet, "/profile", nil, nil, &result)
	if err == nil && result.EmailAddress == "" {
		err = errors.New("Gmail profile omitted email address")
	}
	return result, err
}
func (c *Client) ListLabels(ctx context.Context) ([]Label, error) {
	var result struct {
		Labels []Label `json:"labels"`
	}
	if err := c.request(ctx, "list labels", http.MethodGet, "/labels", nil, nil, &result); err != nil {
		return nil, err
	}
	return result.Labels, nil
}

// FindMessages retrieves every page or returns no candidates and an error.
// A result is only a candidate: callers must verify the Message-ID header.
func (c *Client) FindMessages(ctx context.Context, messageID string) ([]string, error) {
	messageID = strings.TrimSpace(messageID)
	if len(messageID) == 0 || len(messageID) > 998 || strings.ContainsAny(messageID, "\r\n\t \"\\") {
		return nil, errors.New("RFC Message-ID is invalid")
	}
	messageID = strings.TrimPrefix(strings.TrimSuffix(messageID, ">"), "<")
	if !strings.Contains(messageID, "@") || strings.ContainsAny(messageID, "<>") {
		return nil, errors.New("RFC Message-ID is invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	var result []string
	seenIDs, seenPages := map[string]bool{}, map[string]bool{}
	page := ""
	for count := 0; count < 100; count++ {
		query := url.Values{"q": {"rfc822msgid:" + messageID}, "includeSpamTrash": {"true"}, "maxResults": {"500"}}
		if page != "" {
			query.Set("pageToken", page)
		}
		var response struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			NextPageToken      string `json:"nextPageToken"`
			ResultSizeEstimate int    `json:"resultSizeEstimate"`
		}
		if err := c.request(ctx, "find messages", http.MethodGet, "/messages", query, nil, &response); err != nil {
			return nil, err
		}
		for _, message := range response.Messages {
			if message.ID == "" {
				return nil, errors.New("Gmail candidate has no ID")
			}
			if !seenIDs[message.ID] {
				seenIDs[message.ID] = true
				result = append(result, message.ID)
			}
		}
		if len(result) > maxCandidateMessages {
			return nil, errors.New("Gmail candidate limit exceeded; results are incomplete")
		}
		page = response.NextPageToken
		if page == "" {
			return result, nil
		}
		if seenPages[page] {
			return nil, errors.New("Gmail pagination repeated; results are incomplete")
		}
		seenPages[page] = true
	}
	return nil, errors.New("Gmail page limit exceeded; results are incomplete")
}

func validID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsAny(id, "/\\?#\r\n\t ") && id != "." && id != ".."
}
func (c *Client) GetMessage(ctx context.Context, id string) (Message, error) {
	if !validID(id) {
		return Message{}, errors.New("Gmail message ID is invalid")
	}
	query := url.Values{"format": {"metadata"}, "metadataHeaders": {"Message-ID", "From", "Subject", "Date"}}
	var result Message
	err := c.request(ctx, "get message", http.MethodGet, "/messages/"+url.PathEscape(id), query, nil, &result)
	if err == nil && result.ID != id {
		return Message{}, errors.New("Gmail message response ID mismatch")
	}
	return result, err
}

// ModifyLabels reports HTTP acceptance only. Read metadata to verify final labels.
func (c *Client) ModifyLabels(ctx context.Context, id string, add, remove []string) error {
	if !validID(id) {
		return errors.New("Gmail message ID is invalid")
	}
	if len(add)+len(remove) == 0 {
		return errors.New("Gmail label modification is empty")
	}
	if len(add) > 100 || len(remove) > 100 {
		return errors.New("Gmail label modification exceeds the 100-label limit")
	}
	seen := map[string]bool{}
	for _, labels := range [][]string{add, remove} {
		for _, label := range labels {
			if !validID(label) || seen[label] {
				return errors.New("Gmail label modification is invalid")
			}
			seen[label] = true
		}
	}
	body := struct {
		Add    []string `json:"addLabelIds"`
		Remove []string `json:"removeLabelIds"`
	}{add, remove}
	return c.request(ctx, "modify labels", http.MethodPost, "/messages/"+url.PathEscape(id)+"/modify", nil, body, nil)
}
