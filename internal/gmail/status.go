package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// errProviderResponse marks a token endpoint answer that could not be used.
var errProviderResponse = errors.New("Gmail OAuth response is invalid")

// TokenError is a failed OAuth token exchange. Code is Google's OAuth error
// code when it is a short lowercase identifier. The response description,
// body, tokens and client secrets are never kept.
type TokenError struct {
	StatusCode int
	Code       string
	Transport  bool
}

func (e *TokenError) Error() string {
	switch {
	case e.Transport || e.StatusCode == 0:
		return "Gmail OAuth token exchange failed"
	case e.Code != "":
		return fmt.Sprintf("Gmail OAuth token exchange failed (HTTP %d, %s)", e.StatusCode, e.Code)
	default:
		return fmt.Sprintf("Gmail OAuth token exchange failed (HTTP %d)", e.StatusCode)
	}
}

var oauthErrorCode = regexp.MustCompile(`^[a-z_]{1,64}$`)

// tokenErrorCode reads at most 64 KiB and returns only a well-formed JSON
// "error" string field.
func tokenErrorCode(body io.Reader) string {
	data, err := io.ReadAll(io.LimitReader(body, 65536))
	if err != nil {
		return ""
	}
	var parsed struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &parsed) != nil {
		return ""
	}
	var code string
	if json.Unmarshal(parsed.Error, &code) != nil || !oauthErrorCode.MatchString(code) {
		return ""
	}
	return code
}

// AuthStatus is the Gmail authorization state of one address.
type AuthStatus string

const (
	AuthConnected           AuthStatus = "connected"
	AuthCredentialsMissing  AuthStatus = "credentials_missing"
	AuthKeychainUnavailable AuthStatus = "keychain_unavailable"
	AuthKeychainAccess      AuthStatus = "keychain_access"
	AuthCredentialsInvalid  AuthStatus = "credentials_invalid"
	AuthScopeInsufficient   AuthStatus = "scope_insufficient"
	AuthTokenRejected       AuthStatus = "token_rejected"
	AuthClientRejected      AuthStatus = "client_rejected"
	AuthAccessDenied        AuthStatus = "access_denied"
	AuthNetwork             AuthStatus = "network"
	AuthProviderError       AuthStatus = "provider_error"
	AuthAccountMismatch     AuthStatus = "account_mismatch"
	AuthUnknown             AuthStatus = "unknown"
)

// AuthStatuses lists every status in documentation order.
func AuthStatuses() []AuthStatus {
	return []AuthStatus{AuthConnected, AuthCredentialsMissing, AuthKeychainUnavailable, AuthKeychainAccess, AuthCredentialsInvalid, AuthScopeInsufficient, AuthTokenRejected, AuthClientRejected, AuthAccessDenied, AuthNetwork, AuthProviderError, AuthAccountMismatch, AuthUnknown}
}

// ClassifyAuthError maps an error from credential loading, token exchange or
// a Gmail API call to a status. A nil error is connected.
func ClassifyAuthError(err error) AuthStatus {
	if err == nil {
		return AuthConnected
	}
	var keychain *KeychainError
	var token *TokenError
	var request *RequestError
	switch {
	case errors.Is(err, ErrCredentialsNotFound):
		return AuthCredentialsMissing
	case errors.Is(err, ErrKeychainUnavailable):
		return AuthKeychainUnavailable
	case errors.As(err, &keychain):
		return AuthKeychainAccess
	case errors.Is(err, ErrScopeInsufficient):
		return AuthScopeInsufficient
	case errors.Is(err, ErrCredentialsInvalid):
		return AuthCredentialsInvalid
	case errors.As(err, &token):
		return classifyTokenError(token)
	case errors.As(err, &request):
		return classifyRequestError(request)
	case errors.Is(err, errProviderResponse):
		return AuthProviderError
	case errors.Is(err, context.DeadlineExceeded):
		return AuthNetwork
	}
	return AuthUnknown
}

func classifyTokenError(e *TokenError) AuthStatus {
	switch {
	case e.Transport || e.StatusCode == 0:
		return AuthNetwork
	case e.Code == "invalid_client" || e.Code == "unauthorized_client":
		return AuthClientRejected
	case e.Code == "invalid_grant", e.StatusCode == http.StatusBadRequest, e.StatusCode == http.StatusUnauthorized:
		return AuthTokenRejected
	}
	return AuthProviderError
}

func classifyRequestError(e *RequestError) AuthStatus {
	switch {
	case e.Transport:
		return AuthNetwork
	case e.StatusCode == http.StatusUnauthorized:
		return AuthTokenRejected
	case e.StatusCode == http.StatusForbidden:
		return AuthAccessDenied
	}
	return AuthProviderError
}

// Hint returns one recovery step for the status. Connected has none.
func (s AuthStatus) Hint() string {
	switch s {
	case AuthConnected:
		return ""
	case AuthCredentialsMissing:
		return "connect this account with 'mail-app-cli labels auth connect --client-secret <path> -a <account>' or import credentials with 'labels auth import'"
	case AuthKeychainUnavailable:
		return "this build cannot read macOS Keychain; use a macOS build of mail-app-cli made with cgo"
	case AuthKeychainAccess:
		return "unlock the login Keychain and use the installed signed build of mail-app-cli; if access is still refused, reconnect with 'labels auth connect' or 'labels auth import' from that build"
	case AuthCredentialsInvalid:
		return "the stored credentials cannot be used; reconnect with 'mail-app-cli labels auth connect' or import a valid file with 'labels auth import'"
	case AuthScopeInsufficient:
		return "reconnect with 'mail-app-cli labels auth connect' and grant the gmail.modify scope"
	case AuthTokenRejected:
		return "Google rejected the stored refresh token; reconnect with 'mail-app-cli labels auth connect' or import new credentials with 'labels auth import'"
	case AuthClientRejected:
		return "Google rejected the OAuth client; check that the desktop OAuth client still exists, then reconnect with 'labels auth connect' using its client secret file"
	case AuthAccessDenied:
		return "Gmail refused access with these credentials; check that the Gmail API is enabled for the OAuth client's project, or connect the correct account with 'labels auth connect'"
	case AuthNetwork:
		return "Google could not be reached; check the network connection and retry"
	case AuthProviderError:
		return "Google returned an error; retry later"
	case AuthAccountMismatch:
		return "the stored credentials belong to a different Google account; connect credentials for an address configured on the selected Mail account with 'labels auth connect --email <address>'"
	}
	return "check the detail; reconnect with 'mail-app-cli labels auth connect' if the problem continues"
}

// AddressStatus is the result of probing the stored credentials for one address.
type AddressStatus struct {
	Email         string     `json:"email"`
	Status        AuthStatus `json:"status"`
	Detail        string     `json:"detail,omitempty"`
	Hint          string     `json:"hint,omitempty"`
	ProviderEmail string     `json:"providerEmail,omitempty"`
}

// ProbeStoredCredentials loads the Keychain credentials for email, exchanges
// the refresh token for a new access token and reads the Gmail profile.
// Rotated credentials are saved as in normal use. It never returns secrets.
func ProbeStoredCredentials(ctx context.Context, email string, client *http.Client) AddressStatus {
	return probeStoredCredentials(ctx, email, client, defaultTokenURL)
}

func probeStoredCredentials(ctx context.Context, email string, client *http.Client, tokenURL string, options ...ClientOption) AddressStatus {
	if ctx == nil {
		ctx = context.Background()
	}
	result := AddressStatus{Email: email}
	credentials, err := LoadCredentials(email)
	if err == nil {
		source, api := storedClient(strings.ToLower(strings.TrimSpace(email)), credentials, client, tokenURL, options...)
		source.InvalidateAccessToken()
		var profile Profile
		profile, err = api.Profile(ctx)
		if err == nil {
			result.ProviderEmail = strings.ToLower(strings.TrimSpace(profile.EmailAddress))
		}
	}
	result.Status = ClassifyAuthError(err)
	if err != nil {
		result.Detail = err.Error()
		result.Hint = result.Status.Hint()
	}
	return result
}
