package gmail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const ModifyScope = "https://www.googleapis.com/auth/gmail.modify"
const fullMailScope = "https://mail.google.com/"
const defaultTokenURL = "https://oauth2.googleapis.com/token"
const keychainService = "mail-app-cli.gmail"

var ErrKeychainUnavailable = errors.New("Gmail credential storage requires macOS with cgo and the Security framework")
var ErrCredentialsNotFound = errors.New("Gmail credentials were not found in macOS Keychain; connect or explicitly import credentials")

// Credentials is the explicit JSON import format. Expiry is RFC3339.
// Secret fields are persisted only in macOS Keychain, never a plaintext cache.
type Credentials struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret,omitempty"`
	RefreshToken string    `json:"refresh_token"`
	AccessToken  string    `json:"access_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
	Scope        string    `json:"scope"`
}

func (c Credentials) String() string   { return "[Gmail credentials redacted]" }
func (c Credentials) GoString() string { return c.String() }

func HasModifyScope(scope string) bool {
	for _, value := range strings.Fields(scope) {
		if value == ModifyScope || value == fullMailScope {
			return true
		}
	}
	return false
}

func normalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email || !strings.Contains(email, "@") || strings.ContainsAny(email, "\r\n\x00") {
		return "", errors.New("expected Gmail account email is invalid")
	}
	return email, nil
}

func validateCredentials(c Credentials) error {
	if err := validateCredentialFields(c); err != nil {
		return err
	}
	if !HasModifyScope(c.Scope) {
		return errors.New("Gmail credentials require gmail.modify or full-mail OAuth scope")
	}
	return nil
}

func validateCredentialFields(c Credentials) error {
	if c.ClientID == "" || c.RefreshToken == "" {
		return errors.New("Gmail credentials require client_id and refresh_token")
	}
	for _, field := range []string{c.ClientID, c.ClientSecret, c.RefreshToken, c.AccessToken} {
		if len(field) > 16384 || strings.ContainsAny(field, "\r\n\x00") {
			return errors.New("Gmail credentials contain an invalid field")
		}
	}
	return nil
}

func LoadCredentials(email string) (Credentials, error) {
	email, err := normalizeEmail(email)
	if err != nil {
		return Credentials{}, err
	}
	data, err := keychainLoad(email)
	if err != nil {
		return Credentials{}, err
	}
	var credentials Credentials
	if len(data) > 65536 || json.Unmarshal(data, &credentials) != nil {
		return Credentials{}, errors.New("Gmail Keychain credentials are invalid")
	}
	if err := validateCredentials(credentials); err != nil {
		return Credentials{}, err
	}
	return credentials, nil
}

func StoreCredentials(email string, credentials Credentials) error {
	email, err := normalizeEmail(email)
	if err != nil {
		return err
	}
	if err := validateCredentials(credentials); err != nil {
		return err
	}
	data, err := json.Marshal(credentials)
	if err != nil {
		return errors.New("Gmail credentials cannot be encoded")
	}
	return keychainStore(email, data)
}

type CredentialTokenSource struct {
	mu             sync.Mutex
	credentials    Credentials
	http           *http.Client
	tokenURL       string
	persist        func(Credentials) error
	persistPending bool
}

func NewCredentialTokenSource(credentials Credentials, client *http.Client) *CredentialTokenSource {
	return &CredentialTokenSource{credentials: credentials, http: secureHTTPClient(client), tokenURL: defaultTokenURL}
}

func NewStoredClient(email string, client *http.Client) (*Client, error) {
	credentials, err := LoadCredentials(email)
	if err != nil {
		return nil, err
	}
	source := NewCredentialTokenSource(credentials, client)
	source.persist = func(updated Credentials) error { return StoreCredentials(email, updated) }
	return NewClient(client, source), nil
}

func (s *CredentialTokenSource) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if err := validateCredentialFields(s.credentials); err != nil {
		return "", err
	}
	if s.persistPending && s.persist != nil {
		if err := s.persist(s.credentials); err != nil {
			return "", errors.New("Gmail refreshed credentials could not be saved to macOS Keychain")
		}
		s.persistPending = false
	}
	if HasModifyScope(s.credentials.Scope) && s.credentials.AccessToken != "" && time.Until(s.credentials.Expiry) > time.Minute {
		return s.credentials.AccessToken, nil
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s.credentials.RefreshToken}, "client_id": {s.credentials.ClientID}}
	if s.credentials.ClientSecret != "" {
		form.Set("client_secret", s.credentials.ClientSecret)
	}
	response, err := exchangeToken(ctx, s.http, s.tokenURL, form)
	if err != nil {
		return "", err
	}
	updated := s.credentials
	updated.AccessToken = response.AccessToken
	updated.Expiry = time.Now().Add(time.Duration(response.ExpiresIn) * time.Second)
	if response.RefreshToken != "" {
		updated.RefreshToken = response.RefreshToken
	}
	if response.Scope != "" {
		updated.Scope = response.Scope
	}
	if err := validateCredentials(updated); err != nil {
		return "", err
	}
	// Keep rotated refresh tokens in memory even if persistence fails. Never send
	// a Gmail mutation using credentials that could not be persisted reliably.
	s.credentials = updated
	if s.persist != nil {
		s.persistPending = true
		if err := s.persist(updated); err != nil {
			return "", errors.New("Gmail refreshed credentials could not be saved to macOS Keychain")
		}
		s.persistPending = false
	}
	return updated.AccessToken, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

func exchangeToken(ctx context.Context, client *http.Client, endpoint string, form url.Values) (tokenResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResponse{}, errors.New("Gmail OAuth request is invalid")
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := secureHTTPClient(client).Do(req)
	if err != nil {
		return tokenResponse{}, errors.New("Gmail OAuth token exchange failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return tokenResponse{}, fmt.Errorf("Gmail OAuth token exchange failed (HTTP %d)", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(data) > 65536 {
		return tokenResponse{}, errors.New("Gmail OAuth response exceeded its limit or was incomplete")
	}
	var token tokenResponse
	if json.Unmarshal(data, &token) != nil || token.AccessToken == "" || token.ExpiresIn <= 0 || token.ExpiresIn > 365*24*3600 || (token.TokenType != "" && !strings.EqualFold(token.TokenType, "Bearer")) || strings.ContainsAny(token.AccessToken, "\r\n\x00") {
		return tokenResponse{}, errors.New("Gmail OAuth response is invalid")
	}
	return token, nil
}

func readExplicitJSON(path string, target any) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("an explicit Gmail OAuth JSON path is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return errors.New("Gmail OAuth JSON file could not be opened")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("Gmail OAuth JSON path must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(data) > 65536 || json.Unmarshal(data, target) != nil {
		return errors.New("Gmail OAuth JSON file is invalid or exceeds 64 KiB")
	}
	return nil
}

func validateAndStore(ctx context.Context, credentials Credentials, email string, api API, store func(string, Credentials) error) (Profile, error) {
	expected, err := normalizeEmail(email)
	if err != nil {
		return Profile{}, err
	}
	if err := validateCredentials(credentials); err != nil {
		return Profile{}, err
	}
	profile, err := api.Profile(ctx)
	if err != nil {
		return Profile{}, err
	}
	actual, err := normalizeEmail(profile.EmailAddress)
	if err != nil || actual != expected {
		return Profile{}, errors.New("Gmail OAuth account does not match the requested account email; credentials were not saved")
	}
	if err := store(expected, credentials); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func ImportCredentials(ctx context.Context, path, expectedEmail string, client *http.Client) (Profile, error) {
	if _, err := normalizeEmail(expectedEmail); err != nil {
		return Profile{}, err
	}
	var credentials Credentials
	if err := readExplicitJSON(path, &credentials); err != nil {
		return Profile{}, err
	}
	if err := validateCredentialFields(credentials); err != nil {
		return Profile{}, err
	}
	source := NewCredentialTokenSource(credentials, client)
	api := NewClient(client, source)
	// Profile may refresh the access token; persist the resulting credential set.
	profile, err := api.Profile(ctx)
	if err != nil {
		return Profile{}, err
	}
	actual, err := normalizeEmail(profile.EmailAddress)
	expected, _ := normalizeEmail(expectedEmail)
	if err != nil || actual != expected {
		return Profile{}, errors.New("Gmail OAuth account does not match the requested account email; credentials were not saved")
	}
	if err := StoreCredentials(expected, source.credentials); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// Connect runs installed-client OAuth using a loopback listener, state and PKCE.
// openURL is supplied by the CLI and receives the consent URL, without secrets.
func Connect(ctx context.Context, clientJSONPath, expectedEmail string, openURL func(string) error, client *http.Client) (Profile, error) {
	return connect(ctx, clientJSONPath, expectedEmail, openURL, client, StoreCredentials)
}

func connect(ctx context.Context, clientJSONPath, expectedEmail string, openURL func(string) error, client *http.Client, store func(string, Credentials) error) (Profile, error) {
	expected, err := normalizeEmail(expectedEmail)
	if err != nil {
		return Profile{}, err
	}
	if openURL == nil {
		return Profile{}, errors.New("Gmail OAuth requires a browser opener")
	}
	var config struct {
		Installed struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		} `json:"installed"`
	}
	if err := readExplicitJSON(clientJSONPath, &config); err != nil {
		return Profile{}, err
	}
	if config.Installed.ClientID == "" || strings.ContainsAny(config.Installed.ClientID, "\r\n\x00") {
		return Profile{}, errors.New("Gmail OAuth requires an installed-client JSON file")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return Profile{}, errors.New("Gmail OAuth loopback listener could not start")
	}
	defer listener.Close()
	random := make([]byte, 64)
	if _, err := rand.Read(random); err != nil {
		return Profile{}, errors.New("Gmail OAuth randomness failed")
	}
	state := base64.RawURLEncoding.EncodeToString(random[:32])
	verifier := base64.RawURLEncoding.EncodeToString(random[32:])
	digest := sha256.Sum256([]byte(verifier))
	redirect := "http://" + listener.Addr().String() + "/oauth/callback"
	type callback struct {
		code string
		err  error
	}
	results := make(chan callback, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet || len(r.URL.RawQuery) > 16384 || subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("state")), []byte(state)) != 1 {
			http.Error(w, "Invalid OAuth callback", http.StatusBadRequest)
			return
		}
		result := callback{code: r.URL.Query().Get("code")}
		if result.code == "" || r.URL.Query().Get("error") != "" {
			result.err = errors.New("Gmail OAuth consent was declined or did not return a code")
		}
		select {
		case results <- result:
		default:
		}
		_, _ = io.WriteString(w, "Gmail authorization received. You can close this window.")
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 16384}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	query := url.Values{"client_id": {config.Installed.ClientID}, "redirect_uri": {redirect}, "response_type": {"code"}, "scope": {ModifyScope}, "access_type": {"offline"}, "prompt": {"consent"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(digest[:])}, "code_challenge_method": {"S256"}, "login_hint": {expected}}
	if err := openURL("https://accounts.google.com/o/oauth2/v2/auth?" + query.Encode()); err != nil {
		return Profile{}, errors.New("Gmail OAuth consent page could not be opened")
	}
	var received callback
	select {
	case received = <-results:
	case <-ctx.Done():
		return Profile{}, errors.New("Gmail OAuth authorization timed out or was canceled")
	}
	if received.err != nil {
		return Profile{}, received.err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {received.code}, "client_id": {config.Installed.ClientID}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
	if config.Installed.ClientSecret != "" {
		form.Set("client_secret", config.Installed.ClientSecret)
	}
	token, err := exchangeToken(ctx, client, defaultTokenURL, form)
	if err != nil {
		return Profile{}, err
	}
	credentials := Credentials{ClientID: config.Installed.ClientID, ClientSecret: config.Installed.ClientSecret, RefreshToken: token.RefreshToken, AccessToken: token.AccessToken, Expiry: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second), Scope: token.Scope}
	// Google returns granted scope in token responses. Absence is fail-closed.
	return validateAndStore(ctx, credentials, expected, NewClient(client, NewCredentialTokenSource(credentials, client)), store)
}
