package gmail

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testCredentials() Credentials {
	return Credentials{ClientID: "client-id", ClientSecret: "client-secret", RefreshToken: "refresh-secret", Scope: ModifyScope}
}

func TestRefreshEstablishesScopeAndCachesToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		r.ParseForm()
		if r.Method != http.MethodPost || r.Form.Get("refresh_token") != "refresh-secret" || r.Form.Get("client_id") != "client-id" || r.Form.Get("client_secret") != "client-secret" {
			t.Error("refresh request")
		}
		fmt.Fprintf(w, `{"access_token":"new-access","refresh_token":"rotated-refresh","expires_in":3600,"scope":%q,"token_type":"Bearer"}`, ModifyScope)
	}))
	defer server.Close()
	credentials := testCredentials()
	credentials.Scope = ""
	source := NewCredentialTokenSource(credentials, server.Client())
	source.tokenURL = server.URL
	for range 2 {
		token, err := source.Token(context.Background())
		if err != nil || token != "new-access" {
			t.Fatalf("refresh: %q %v", token, err)
		}
	}
	if calls.Load() != 1 || source.credentials.RefreshToken != "rotated-refresh" || !HasModifyScope(source.credentials.Scope) {
		t.Fatal("refresh not cached or rotated")
	}
}

func TestRefreshRejectsInsufficientScopeAndRedactsFailure(t *testing.T) {
	for _, mode := range []string{"scope", "missing-scope", "failure"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "failure" {
					w.WriteHeader(400)
					io.WriteString(w, "refresh-secret client-secret private response")
					return
				}
				scope := "gmail.readonly"
				if mode == "missing-scope" {
					scope = ""
				}
				fmt.Fprintf(w, `{"access_token":"new-access","expires_in":3600,"scope":%q}`, scope)
			}))
			defer server.Close()
			credentials := testCredentials()
			credentials.Scope = ""
			source := NewCredentialTokenSource(credentials, server.Client())
			source.tokenURL = server.URL
			token, err := source.Token(context.Background())
			if err == nil || token != "" || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe scope/error: %q %v", token, err)
			}
		})
	}
}

func TestRefreshPersistenceFailureBlocksCachedToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"access_token":"new-access","expires_in":3600,"scope":%q}`, ModifyScope)
	}))
	defer server.Close()
	source := NewCredentialTokenSource(testCredentials(), server.Client())
	source.tokenURL = server.URL
	var saves int
	source.persist = func(Credentials) error { saves++; return errors.New("private persistence detail") }
	for range 2 {
		if token, err := source.Token(context.Background()); err == nil || token != "" {
			t.Fatal("unpersisted token escaped")
		}
	}
	if saves != 2 {
		t.Fatalf("persistence not retried before cache use: %d", saves)
	}
}

type profileAPI struct {
	API
	email string
}

func (p profileAPI) Profile(context.Context) (Profile, error) {
	return Profile{EmailAddress: p.email}, nil
}

func TestAccountBindingBeforeCredentialStorage(t *testing.T) {
	for _, actual := range []string{"other@example.com", "  OWNER@EXAMPLE.COM  "} {
		t.Run(actual, func(t *testing.T) {
			stored := false
			_, err := validateAndStore(context.Background(), testCredentials(), "Owner@example.com", profileAPI{email: actual}, func(email string, c Credentials) error {
				stored = true
				if email != "owner@example.com" {
					t.Error("unnormalized account")
				}
				return nil
			})
			if actual == "other@example.com" {
				if err == nil || stored {
					t.Fatal("mismatched account stored")
				}
			} else if err != nil || !stored {
				t.Fatalf("matching account: %v", err)
			}
		})
	}
}

func TestCredentialSecretsAreRedacted(t *testing.T) {
	credentials := testCredentials()
	credentials.AccessToken = "access-secret"
	for _, rendered := range []string{fmt.Sprint(credentials), fmt.Sprintf("%+v", credentials), fmt.Sprintf("%#v", credentials)} {
		if strings.Contains(rendered, "secret") || strings.Contains(rendered, "client-id") {
			t.Fatalf("secret formatted: %s", rendered)
		}
	}
}

func TestProductionKeychainNamespaceAndInteractionErrors(t *testing.T) {
	if keychainService != "mail-app-cli.gmail.oauth.v1" {
		t.Fatalf("production Keychain namespace changed: %q", keychainService)
	}
	for _, status := range []int{-25308, -128} {
		message := keychainStatusError("read", status).Error()
		for _, want := range []string{"without user interaction", fmt.Sprint(status), "unlock the login Keychain", "labels auth import", "installed signed app"} {
			if !strings.Contains(message, want) {
				t.Errorf("status %d error omitted %q: %s", status, want, message)
			}
		}
	}
	if message := keychainStatusError("write", -50).Error(); message != "Gmail Keychain write failed (Security status -50)" {
		t.Fatalf("unexpected generic status error: %s", message)
	}
}

func TestCredentialJSONIsExplicitBoundedAndValidated(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "import.json")
	if err := os.WriteFile(path, []byte(`{"client_id":"client","refresh_token":"refresh","type":"authorized_user"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var credentials Credentials
	if err := readExplicitJSON(path, &credentials); err != nil || credentials.RefreshToken != "refresh" {
		t.Fatalf("standard import: %v", err)
	}
	if err := validateCredentialFields(credentials); err != nil {
		t.Fatal(err)
	}
	if err := validateCredentials(credentials); err == nil {
		t.Fatal("missing grant accepted for storage")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 65537)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := readExplicitJSON(path, &credentials); err == nil {
		t.Fatal("oversized import accepted")
	}
	if err := readExplicitJSON(directory, &credentials); err == nil {
		t.Fatal("non-file accepted")
	}
}

func TestOAuthRedirectDoesNotLeakCredentials(t *testing.T) {
	var reached atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Store(true) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer server.Close()
	source := NewCredentialTokenSource(testCredentials(), server.Client())
	source.tokenURL = server.URL
	if _, err := source.Token(context.Background()); err == nil || reached.Load() {
		t.Fatalf("OAuth redirect replayed credentials: %v", err)
	}
}

func TestAccessTokenWithoutExpiryIsRefreshed(t *testing.T) {
	credentials := testCredentials()
	credentials.AccessToken = "stale-access"
	credentials.Expiry = time.Time{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"fresh-access","expires_in":3600}`)
	}))
	defer server.Close()
	source := NewCredentialTokenSource(credentials, server.Client())
	source.tokenURL = server.URL
	if token, err := source.Token(context.Background()); err != nil || token != "fresh-access" {
		t.Fatalf("stale token: %q %v", token, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConnectLoopbackPKCEStateAndAccountBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client.json")
	if err := os.WriteFile(path, []byte(`{"installed":{"client_id":"installed-client","client_secret":"installed-secret"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var challenge, redirect string
	var stored bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := ""
		switch r.URL.Host {
		case "oauth2.googleapis.com":
			r.ParseForm()
			digest := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if base64.RawURLEncoding.EncodeToString(digest[:]) != challenge || r.Form.Get("redirect_uri") != redirect || r.Form.Get("code") != "single-use-code" || r.Form.Get("client_secret") != "installed-secret" {
				t.Error("PKCE exchange mismatch")
			}
			body = fmt.Sprintf(`{"access_token":"access","refresh_token":"refresh","expires_in":3600,"scope":%q}`, ModifyScope)
		case "gmail.googleapis.com":
			if r.URL.Path != "/gmail/v1/users/me/profile" {
				t.Error("unexpected Gmail call")
			}
			body = `{"emailAddress":"owner@example.com"}`
		default:
			t.Errorf("unexpected external request: %s", r.URL.Host)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})}
	opener := func(consent string) error {
		u, err := url.Parse(consent)
		if err != nil {
			return err
		}
		q := u.Query()
		challenge = q.Get("code_challenge")
		redirect = q.Get("redirect_uri")
		if u.Host != "accounts.google.com" || q.Get("code_challenge_method") != "S256" || q.Get("scope") != ModifyScope || q.Get("state") == "" || q.Get("client_secret") != "" {
			t.Error("consent parameters")
		}
		response, err := http.Get(redirect + "?state=wrong&code=bad")
		if err != nil {
			return err
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Error("state mismatch accepted")
		}
		callback := url.Values{"state": {q.Get("state")}, "code": {"single-use-code"}}
		response, err = http.Get(redirect + "?" + callback.Encode())
		if err != nil {
			return err
		}
		response.Body.Close()
		return nil
	}
	profile, err := connect(context.Background(), path, "owner@example.com", opener, client, func(email string, c Credentials) error {
		stored = true
		if email != "owner@example.com" || c.RefreshToken != "refresh" || !HasModifyScope(c.Scope) {
			t.Error("stored credentials")
		}
		return nil
	})
	if err != nil || profile.EmailAddress != "owner@example.com" || !stored {
		t.Fatalf("connect profile=%+v stored=%v err=%v", profile, stored, err)
	}
}
