package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fakeKeychain replaces Keychain reads and writes for one test.
type fakeKeychain struct {
	items   map[string][]byte
	loadErr error
	stored  map[string]Credentials
}

func useFakeKeychain(t *testing.T, keychain *fakeKeychain) {
	t.Helper()
	load, store := loadKeychainItem, storeKeychainItem
	t.Cleanup(func() { loadKeychainItem, storeKeychainItem = load, store })
	keychain.stored = map[string]Credentials{}
	loadKeychainItem = func(email string) ([]byte, error) {
		if keychain.loadErr != nil {
			return nil, keychain.loadErr
		}
		data, ok := keychain.items[email]
		if !ok {
			return nil, ErrCredentialsNotFound
		}
		return data, nil
	}
	storeKeychainItem = func(email string, data []byte) error {
		var credentials Credentials
		if err := json.Unmarshal(data, &credentials); err != nil {
			return err
		}
		keychain.stored[email] = credentials
		return nil
	}
}

func storedJSON(t *testing.T, credentials Credentials) []byte {
	t.Helper()
	data, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestTokenErrorKeepsOnlyWellFormedOAuthCode(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		code   string
		text   string
	}{
		{"invalid grant", 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`, "invalid_grant", "Gmail OAuth token exchange failed (HTTP 400, invalid_grant)"},
		{"invalid client", 401, `{"error":"invalid_client","error_description":"The OAuth client was not found."}`, "invalid_client", "Gmail OAuth token exchange failed (HTTP 401, invalid_client)"},
		{"server error", 500, `internal failure`, "", "Gmail OAuth token exchange failed (HTTP 500)"},
		{"uppercase code", 400, `{"error":"Invalid_Grant"}`, "", "Gmail OAuth token exchange failed (HTTP 400)"},
		{"object code", 403, `{"error":{"code":403,"message":"private"}}`, "", "Gmail OAuth token exchange failed (HTTP 403)"},
		{"long code", 400, `{"error":"` + strings.Repeat("a", 65) + `"}`, "", "Gmail OAuth token exchange failed (HTTP 400)"},
		{"oversized body", 400, `{"padding":"` + strings.Repeat("x", 70000) + `","error":"invalid_grant"}`, "", "Gmail OAuth token exchange failed (HTTP 400)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			}))
			defer server.Close()
			_, err := exchangeToken(context.Background(), server.Client(), server.URL, url.Values{"refresh_token": {"refresh-secret"}})
			var token *TokenError
			if !errors.As(err, &token) || token.StatusCode != tt.status || token.Code != tt.code || token.Transport {
				t.Fatalf("error = %#v", err)
			}
			if err.Error() != tt.text {
				t.Fatalf("text = %q, want %q", err.Error(), tt.text)
			}
			for _, leaked := range []string{"expired", "revoked", "not found", "private", "internal failure", "secret"} {
				if strings.Contains(err.Error(), leaked) {
					t.Fatalf("error leaked %q: %s", leaked, err)
				}
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	endpoint := server.URL
	server.Close()
	_, err := exchangeToken(context.Background(), nil, endpoint, url.Values{})
	var token *TokenError
	if !errors.As(err, &token) || !token.Transport || err.Error() != "Gmail OAuth token exchange failed" {
		t.Fatalf("transport error = %#v", err)
	}
}

func TestKeychainErrorTypeKeepsMessages(t *testing.T) {
	err := keychainStatusError("read", -25308)
	var keychain *KeychainError
	if !errors.As(err, &keychain) || keychain.Operation != "read" || keychain.Status != -25308 || !keychain.InteractionRequired() {
		t.Fatalf("keychain error = %#v", err)
	}
	if ClassifyAuthError(fmt.Errorf("load: %w", err)) != AuthKeychainAccess {
		t.Fatal("wrapped Keychain error was not classified")
	}
}

func TestClassifyAuthError(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want AuthStatus
	}{
		{nil, AuthConnected},
		{fmt.Errorf("lookup: %w", ErrCredentialsNotFound), AuthCredentialsMissing},
		{ErrKeychainUnavailable, AuthKeychainUnavailable},
		{keychainStatusError("read", -128), AuthKeychainAccess},
		{keychainStatusError("write", -50), AuthKeychainAccess},
		{persistError(keychainStatusError("write", -25308)), AuthKeychainAccess},
		{credentialsInvalid("Gmail Keychain credentials are invalid"), AuthCredentialsInvalid},
		{ErrScopeInsufficient, AuthScopeInsufficient},
		{&TokenError{StatusCode: 400, Code: "invalid_grant"}, AuthTokenRejected},
		{&TokenError{StatusCode: 400, Code: "invalid_request"}, AuthTokenRejected},
		{&TokenError{StatusCode: 401}, AuthTokenRejected},
		{&TokenError{StatusCode: 401, Code: "invalid_client"}, AuthClientRejected},
		{&TokenError{StatusCode: 400, Code: "unauthorized_client"}, AuthClientRejected},
		{&TokenError{Transport: true}, AuthNetwork},
		{&TokenError{StatusCode: 500}, AuthProviderError},
		{&TokenError{StatusCode: 429}, AuthProviderError},
		{&TokenError{StatusCode: 403}, AuthProviderError},
		{errProviderResponse, AuthProviderError},
		{&RequestError{Operation: "profile", StatusCode: 401}, AuthTokenRejected},
		{&RequestError{Operation: "profile", StatusCode: 403}, AuthAccessDenied},
		{&RequestError{Operation: "profile", StatusCode: 503}, AuthProviderError},
		{&RequestError{Operation: "profile", StatusCode: 429}, AuthProviderError},
		{&RequestError{Operation: "profile", Transport: true}, AuthNetwork},
		{&RequestError{Operation: "profile"}, AuthProviderError},
		{fmt.Errorf("wait: %w", context.DeadlineExceeded), AuthNetwork},
		{errors.New("Gmail credentials require gmail.modify or full-mail OAuth scope"), AuthUnknown},
		{context.Canceled, AuthUnknown},
	} {
		t.Run(fmt.Sprint(tt.err), func(t *testing.T) {
			if got := ClassifyAuthError(tt.err); got != tt.want {
				t.Fatalf("ClassifyAuthError(%v) = %s, want %s", tt.err, got, tt.want)
			}
		})
	}
}

func TestAuthStatusHints(t *testing.T) {
	if AuthConnected.Hint() != "" {
		t.Fatal("connected has a hint")
	}
	for _, status := range AuthStatuses()[1:] {
		hint := status.Hint()
		if hint == "" || strings.Contains(hint, "\n") || strings.HasSuffix(hint, ".") {
			t.Fatalf("%s hint = %q", status, hint)
		}
	}
	for status, want := range map[AuthStatus]string{
		AuthCredentialsMissing: "labels auth connect --client-secret <path> -a <account>",
		AuthKeychainAccess:     "unlock the login Keychain",
		AuthTokenRejected:      "rejected the stored refresh token",
		AuthScopeInsufficient:  "gmail.modify",
		AuthAccountMismatch:    "address configured on the selected Mail account",
	} {
		if !strings.Contains(status.Hint(), want) {
			t.Fatalf("%s hint %q omits %q", status, status.Hint(), want)
		}
	}
}

// probeServers answers the token endpoint and the Gmail profile endpoint.
type probeServers struct {
	token, api       *httptest.Server
	tokenCalls       int
	tokenStatus      int
	tokenBody        string
	apiStatus        int
	profileEmail     string
	rotatedRefresh   string
	sawRefreshSecret bool
}

func newProbeServers(t *testing.T) *probeServers {
	t.Helper()
	p := &probeServers{tokenStatus: 200, apiStatus: 200, profileEmail: "primary@example.com"}
	p.token = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.tokenCalls++
		r.ParseForm()
		p.sawRefreshSecret = r.Form.Get("refresh_token") == "refresh-secret"
		w.WriteHeader(p.tokenStatus)
		if p.tokenBody != "" {
			io.WriteString(w, p.tokenBody)
			return
		}
		fmt.Fprintf(w, `{"access_token":"fresh-access","refresh_token":%q,"expires_in":3600,"scope":%q}`, p.rotatedRefresh, ModifyScope)
	}))
	p.api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/profile" || r.Header.Get("Authorization") != "Bearer fresh-access" {
			t.Errorf("unexpected Gmail request %s; fresh token sent: %v", r.URL.Path, r.Header.Get("Authorization") == "Bearer fresh-access")
		}
		w.WriteHeader(p.apiStatus)
		if p.profileEmail != "" {
			fmt.Fprintf(w, `{"emailAddress":%q}`, p.profileEmail)
		} else {
			io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(p.token.Close)
	t.Cleanup(p.api.Close)
	return p
}

func (p *probeServers) probe(email string) AddressStatus {
	return probeStoredCredentials(context.Background(), email, p.token.Client(), p.token.URL, WithBaseURL(p.api.URL))
}

func TestProbeStoredCredentialsStatuses(t *testing.T) {
	valid := testCredentials()
	valid.AccessToken = "cached-access"
	valid.Expiry = time.Now().Add(time.Hour)
	readonly := testCredentials()
	readonly.Scope = "https://www.googleapis.com/auth/gmail.readonly"
	for _, tt := range []struct {
		name     string
		items    map[string][]byte
		loadErr  error
		setup    func(*probeServers)
		want     AuthStatus
		provider string
		detail   string
	}{
		{name: "connected", want: AuthConnected, provider: "primary@example.com"},
		{name: "other profile", setup: func(p *probeServers) { p.profileEmail = "Other@Example.com" }, want: AuthConnected, provider: "other@example.com"},
		{name: "missing", items: map[string][]byte{}, want: AuthCredentialsMissing, detail: ErrCredentialsNotFound.Error()},
		{name: "keychain unavailable", loadErr: ErrKeychainUnavailable, want: AuthKeychainUnavailable},
		{name: "keychain access", loadErr: keychainStatusError("read", -25308), want: AuthKeychainAccess, detail: "without user interaction"},
		{name: "invalid JSON", items: map[string][]byte{"primary@example.com": []byte("{")}, want: AuthCredentialsInvalid, detail: "Gmail Keychain credentials are invalid"},
		{name: "invalid fields", items: map[string][]byte{"primary@example.com": []byte(`{"client_id":"client-id","scope":"https://mail.google.com/"}`)}, want: AuthCredentialsInvalid, detail: "client_id and refresh_token"},
		{name: "scope", items: map[string][]byte{"primary@example.com": storedJSON(t, readonly)}, want: AuthScopeInsufficient},
		{name: "refreshed scope", setup: func(p *probeServers) {
			p.tokenBody = `{"access_token":"fresh-access","expires_in":3600,"scope":"https://www.googleapis.com/auth/gmail.readonly"}`
		}, want: AuthScopeInsufficient},
		{name: "invalid grant", setup: func(p *probeServers) {
			p.tokenStatus, p.tokenBody = 400, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`
		}, want: AuthTokenRejected, detail: "Gmail OAuth token exchange failed (HTTP 400, invalid_grant)"},
		{name: "invalid client", setup: func(p *probeServers) {
			p.tokenStatus, p.tokenBody = 401, `{"error":"invalid_client","error_description":"The OAuth client was not found."}`
		}, want: AuthClientRejected},
		{name: "token server error", setup: func(p *probeServers) { p.tokenStatus, p.tokenBody = 500, "busy" }, want: AuthProviderError},
		{name: "token rate limit", setup: func(p *probeServers) { p.tokenStatus, p.tokenBody = 429, "slow down" }, want: AuthProviderError},
		{name: "token response invalid", setup: func(p *probeServers) { p.tokenBody = `{"access_token":""}` }, want: AuthProviderError},
		{name: "token network", setup: func(p *probeServers) { p.token.Close() }, want: AuthNetwork, detail: "Gmail OAuth token exchange failed"},
		{name: "api unauthorized", setup: func(p *probeServers) { p.apiStatus = 401 }, want: AuthTokenRejected, detail: "Gmail profile failed (HTTP 401)"},
		{name: "api forbidden", setup: func(p *probeServers) { p.apiStatus = 403 }, want: AuthAccessDenied},
		{name: "api server error", setup: func(p *probeServers) { p.apiStatus = 503 }, want: AuthProviderError},
		{name: "api network", setup: func(p *probeServers) { p.api.Close() }, want: AuthNetwork},
		{name: "profile without email", setup: func(p *probeServers) { p.profileEmail = "" }, want: AuthUnknown, detail: "omitted email address"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			items := tt.items
			if items == nil {
				items = map[string][]byte{"primary@example.com": storedJSON(t, valid)}
			}
			keychain := &fakeKeychain{items: items, loadErr: tt.loadErr}
			useFakeKeychain(t, keychain)
			servers := newProbeServers(t)
			if tt.setup != nil {
				tt.setup(servers)
			}
			got := servers.probe("Primary@example.com")
			if got.Email != "Primary@example.com" || got.Status != tt.want || got.ProviderEmail != tt.provider {
				t.Fatalf("probe = %+v, want status %s provider %q", got, tt.want, tt.provider)
			}
			if tt.want == AuthConnected {
				if got.Detail != "" || got.Hint != "" {
					t.Fatalf("connected probe carried detail: %+v", got)
				}
			} else if got.Hint != tt.want.Hint() || got.Detail == "" || !strings.Contains(got.Detail, tt.detail) {
				t.Fatalf("probe detail/hint = %+v, want detail containing %q", got, tt.detail)
			}
			for _, field := range []string{got.Detail, got.Hint} {
				for _, secret := range []string{"refresh-secret", "client-id", "cached-access", "fresh-access", "expired", "client was not found"} {
					if strings.Contains(field, secret) {
						t.Fatalf("probe leaked %q: %+v", secret, got)
					}
				}
			}
		})
	}
}

func TestProbeExercisesRefreshTokenAndPersistsRotation(t *testing.T) {
	valid := testCredentials()
	valid.AccessToken = "cached-access"
	valid.Expiry = time.Now().Add(time.Hour)
	keychain := &fakeKeychain{items: map[string][]byte{"primary@example.com": storedJSON(t, valid)}}
	useFakeKeychain(t, keychain)
	servers := newProbeServers(t)
	servers.rotatedRefresh = "rotated-refresh"
	got := servers.probe("primary@example.com")
	if got.Status != AuthConnected || servers.tokenCalls != 1 || !servers.sawRefreshSecret {
		t.Fatalf("probe = %+v, token calls = %d, refresh sent = %v", got, servers.tokenCalls, servers.sawRefreshSecret)
	}
	stored, ok := keychain.stored["primary@example.com"]
	if !ok || stored.RefreshToken != "rotated-refresh" || stored.AccessToken != "fresh-access" {
		t.Fatalf("rotated credentials were not persisted: ok=%v refresh rotated=%v", ok, stored.RefreshToken == "rotated-refresh")
	}
}

func TestProbeStoredCredentialsToleratesNilClientAndContext(t *testing.T) {
	useFakeKeychain(t, &fakeKeychain{items: map[string][]byte{}})
	var ctx context.Context
	got := ProbeStoredCredentials(ctx, "primary@example.com", nil)
	if got.Status != AuthCredentialsMissing || got.Hint == "" {
		t.Fatalf("probe = %+v", got)
	}
	got = ProbeStoredCredentials(context.Background(), "not an address", nil)
	if got.Status != AuthUnknown || got.Detail == "" {
		t.Fatalf("invalid address probe = %+v", got)
	}
}

func TestInvalidateAccessTokenForcesRefresh(t *testing.T) {
	credentials := testCredentials()
	credentials.AccessToken = "cached-access"
	credentials.Expiry = time.Now().Add(time.Hour)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		fmt.Fprintf(w, `{"access_token":"fresh-access","expires_in":3600,"scope":%q}`, ModifyScope)
	}))
	defer server.Close()
	source := NewCredentialTokenSource(credentials, server.Client())
	source.tokenURL = server.URL
	if token, err := source.Token(context.Background()); err != nil || token != "cached-access" || calls != 0 {
		t.Fatalf("cached token: %q %v calls=%d", token, err, calls)
	}
	source.InvalidateAccessToken()
	if token, err := source.Token(context.Background()); err != nil || token != "fresh-access" || calls != 1 {
		t.Fatalf("invalidated token: %q %v calls=%d", token, err, calls)
	}
}
