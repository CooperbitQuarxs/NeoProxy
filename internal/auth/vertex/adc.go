package vertex

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// This file ports NeoPi's Vertex AI credential resolution (pi-ai
// providers/google-auth.ts, getVertexAccessToken): service account RS256 JWT
// exchange, authorized-user refresh, service account impersonation and the
// GCE/Cloud Run metadata server, with tokens cached per source and refreshed
// GOOGLE_VERTEX_REFRESH_SKEW_MS (default 60s) before they expire.
//
// An account's own credential stands in for the file NeoPi reads from
// GOOGLE_APPLICATION_CREDENTIALS. An account without one resolves the way
// NeoPi does from the process: GOOGLE_CLOUD_ACCESS_TOKEN or
// CLOUDSDK_AUTH_ACCESS_TOKEN, then GOOGLE_APPLICATION_CREDENTIALS, gcloud's
// user ADC, and the metadata server.

const (
	oauthTokenURL      = "https://oauth2.googleapis.com/token"
	metadataTokenURL   = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"
	jwtBearerGrant     = "urn:ietf:params:oauth:grant-type:jwt-bearer"
	// sharedResolveTimeout bounds one shared token resolution (credential
	// acquisition), so a hung exchange never pins the in-flight slot.
	sharedResolveTimeout = 30 * time.Second
	metadataTimeout      = 2 * time.Second
)

// CredentialError is a credential resolution failure. Configuration marks
// NeoPi's ConfigurationError (a setup the caller must fix); the others are its
// OAuthError and MissingApiKeyError.
type CredentialError struct {
	Message       string
	Configuration bool
}

func (e *CredentialError) Error() string { return e.Message }

// adcCredentials is one ADC file: a service account, an authorized user, or
// an impersonated service account with either as its source.
type adcCredentials struct {
	Type                           string          `json:"type"`
	ClientEmail                    string          `json:"client_email"`
	PrivateKey                     string          `json:"private_key"`
	PrivateKeyID                   string          `json:"private_key_id"`
	ClientID                       string          `json:"client_id"`
	ClientSecret                   string          `json:"client_secret"`
	RefreshToken                   string          `json:"refresh_token"`
	ServiceAccountImpersonationURL string          `json:"service_account_impersonation_url"`
	SourceCredentials              *adcCredentials `json:"source_credentials"`
	Delegates                      []string        `json:"delegates"`
}

type tokenResponse struct {
	AccessToken string  `json:"access_token"`
	ExpiresIn   float64 `json:"expires_in"`
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

var (
	nowFunc  = time.Now
	cacheMu  sync.Mutex
	cache    = map[string]cachedToken{}
	inflight singleflight.Group
)

// AccessToken returns a bearer token for Vertex AI. credential is the
// account's own ADC JSON; empty resolves from the process environment as
// NeoPi does. client carries the token requests.
func AccessToken(ctx context.Context, client *http.Client, credential []byte) (string, error) {
	if len(bytes.TrimSpace(credential)) == 0 {
		if explicit := firstEnv("GOOGLE_CLOUD_ACCESS_TOKEN", "CLOUDSDK_AUTH_ACCESS_TOKEN"); explicit != "" {
			return explicit, nil
		}
	}
	skew := refreshSkew()
	now := nowFunc()
	key := "vertex-adc"
	if len(credential) > 0 {
		sum := sha256.Sum256(credential)
		key = "account:" + hex.EncodeToString(sum[:])
	}
	if token, ok := cachedFor(key, len(credential) > 0, now, skew); ok {
		return token, nil
	}
	result := inflight.DoChan(key, func() (any, error) {
		resolveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sharedResolveTimeout)
		defer cancel()
		source, token, err := resolveUncached(resolveCtx, client, credential, key)
		if err != nil {
			return "", err
		}
		expiresIn := max(token.ExpiresIn, 0)
		cacheMu.Lock()
		cache[source] = cachedToken{token: token.AccessToken, expiresAt: nowFunc().Add(time.Duration(expiresIn * float64(time.Second)))}
		cacheMu.Unlock()
		return token.AccessToken, nil
	})
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case outcome := <-result:
		if outcome.Err != nil {
			return "", outcome.Err
		}
		return outcome.Val.(string), nil
	}
}

// cachedFor is NeoPi's cache probe. The process sources share one probe:
// any unexpired process-source token is served, and expired entries are
// dropped. An account's credential is looked up under its own key.
func cachedFor(key string, account bool, now time.Time, skew time.Duration) (string, bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if account {
		if cached, ok := cache[key]; ok {
			if cached.expiresAt.Add(-skew).After(now) {
				return cached.token, true
			}
			delete(cache, key)
		}
		return "", false
	}
	for source, cached := range cache {
		if strings.HasPrefix(source, "account:") {
			continue
		}
		if cached.expiresAt.Add(-skew).After(now) {
			return cached.token, true
		}
		delete(cache, source)
	}
	return "", false
}

func refreshSkew() time.Duration {
	// $envpos: a positive integer, else the default.
	if value, err := strconv.Atoi(strings.TrimSpace(os.Getenv("GOOGLE_VERTEX_REFRESH_SKEW_MS"))); err == nil && value > 0 {
		return time.Duration(value) * time.Millisecond
	}
	return 60 * time.Second
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

// resolveUncached is resolveAccessTokenUncached; source is the cache key the
// token is stored under.
func resolveUncached(ctx context.Context, client *http.Client, credential []byte, accountKey string) (string, tokenResponse, error) {
	creds, source, err := loadCredentials(credential, accountKey)
	if err != nil {
		return "", tokenResponse{}, err
	}
	if creds != nil {
		token, err := exchange(ctx, client, creds)
		return source, token, err
	}
	if token, ok := fetchMetadataToken(ctx, client); ok {
		return "metadata", token, nil
	}
	return "", tokenResponse{}, &CredentialError{Message: "Vertex AI requires Application Default Credentials. Set GOOGLE_APPLICATION_CREDENTIALS, run `gcloud auth application-default login`, or run on a GCE/Cloud Run instance with a service account."}
}

// loadCredentials is loadAdcCredentials, with the account's credential first.
func loadCredentials(credential []byte, accountKey string) (*adcCredentials, string, error) {
	if len(credential) > 0 {
		creds := &adcCredentials{}
		if err := json.Unmarshal(credential, creds); err != nil {
			return nil, "", &CredentialError{Message: "Vertex AI credential is not valid JSON: " + err.Error(), Configuration: true}
		}
		return creds, accountKey, nil
	}
	if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
		creds, err := readCredentialsFile(path)
		if err != nil {
			return nil, "", err
		}
		if creds == nil {
			return nil, "", &CredentialError{Message: "GOOGLE_APPLICATION_CREDENTIALS points to a missing file: " + path, Configuration: true}
		}
		return creds, "gac:" + path, nil
	}
	path := userADCPath()
	creds, err := readCredentialsFile(path)
	if err != nil || creds == nil {
		return nil, "", err
	}
	return creds, "user:" + path, nil
}

// readCredentialsFile reads an ADC file; nil without error when it is missing.
func readCredentialsFile(path string) (*adcCredentials, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	creds := &adcCredentials{}
	if err = json.Unmarshal(raw, creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// userADCPath is gcloud's user ADC file: %APPDATA%\gcloud\… on Windows,
// ~/.config/gcloud/… elsewhere.
func userADCPath() string {
	if appData := os.Getenv("APPDATA"); appData != "" && filepath.Separator == '\\' {
		return filepath.Join(appData, "gcloud", "application_default_credentials.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
}

var impersonationTarget = regexp.MustCompile(`([^/]+):(generateAccessToken|generateIdToken)$`)

// exchange obtains a token for one ADC credential.
func exchange(ctx context.Context, client *http.Client, creds *adcCredentials) (tokenResponse, error) {
	if creds.Type != "impersonated_service_account" {
		return exchangeDirect(ctx, client, creds)
	}
	match := impersonationTarget.FindStringSubmatch(creds.ServiceAccountImpersonationURL)
	if match == nil || match[1] == "" {
		return tokenResponse{}, &CredentialError{Message: "Cannot extract target principal from " + creds.ServiceAccountImpersonationURL, Configuration: true}
	}
	if creds.SourceCredentials == nil {
		return tokenResponse{}, &CredentialError{Message: "Vertex AI impersonated credential has no source_credentials", Configuration: true}
	}
	source, err := exchangeDirect(ctx, client, creds.SourceCredentials)
	if err != nil {
		return tokenResponse{}, err
	}
	delegates := creds.Delegates
	if delegates == nil {
		delegates = []string{}
	}
	body, err := marshalJSON(struct {
		Delegates []string `json:"delegates"`
		Scope     []string `json:"scope"`
		Lifetime  string   `json:"lifetime"`
	}{delegates, []string{cloudPlatformScope}, "3600s"})
	if err != nil {
		return tokenResponse{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/"+match[1]+":generateAccessToken", bytes.NewReader(body))
	if err != nil {
		return tokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+source.AccessToken)
	status, data, err := do(client, request)
	if err != nil {
		return tokenResponse{}, err
	}
	if status < 200 || status > 299 {
		return tokenResponse{}, &CredentialError{Message: fmt.Sprintf("Google Impersonation token exchange failed (%d): %s", status, data)}
	}
	var granted struct {
		AccessToken string `json:"accessToken"`
		ExpireTime  string `json:"expireTime"`
	}
	if err = json.Unmarshal(data, &granted); err != nil {
		return tokenResponse{}, err
	}
	expires, _ := time.Parse(time.RFC3339Nano, granted.ExpireTime)
	expiresIn := max(0, expires.Sub(nowFunc()).Milliseconds()/1000)
	return tokenResponse{AccessToken: granted.AccessToken, ExpiresIn: float64(expiresIn)}, nil
}

// exchangeDirect is a service account's JWT exchange, else an authorized
// user's refresh-token exchange.
func exchangeDirect(ctx context.Context, client *http.Client, creds *adcCredentials) (tokenResponse, error) {
	var form string
	if creds.Type == "service_account" {
		assertion, err := serviceAccountAssertion(creds, nowFunc())
		if err != nil {
			return tokenResponse{}, err
		}
		form = formEncode([][2]string{{"grant_type", jwtBearerGrant}, {"assertion", assertion}})
	} else {
		form = formEncode([][2]string{{"client_id", creds.ClientID}, {"client_secret", creds.ClientSecret},
			{"refresh_token", creds.RefreshToken}, {"grant_type", "refresh_token"}})
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthTokenURL, strings.NewReader(form))
	if err != nil {
		return tokenResponse{}, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	status, data, err := do(client, request)
	if err != nil {
		return tokenResponse{}, err
	}
	if status < 200 || status > 299 {
		return tokenResponse{}, &CredentialError{Message: fmt.Sprintf("Google OAuth token exchange failed (%d): %s", status, data)}
	}
	var token tokenResponse
	if err = json.Unmarshal(data, &token); err != nil {
		return tokenResponse{}, err
	}
	return token, nil
}

// serviceAccountAssertion is signJwtRs256 over NeoPi's claims: issued now,
// valid for an hour, for the cloud-platform scope at the token endpoint.
func serviceAccountAssertion(creds *adcCredentials, now time.Time) (string, error) {
	key, err := parsePrivateKey(creds.PrivateKey)
	if err != nil {
		return "", err
	}
	headerJSON, err := marshalJSON(struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
		Kid string `json:"kid,omitempty"`
	}{"RS256", "JWT", creds.PrivateKeyID})
	if err != nil {
		return "", err
	}
	issued := now.Unix()
	claimsJSON, err := marshalJSON(struct {
		Iss   string `json:"iss"`
		Scope string `json:"scope"`
		Aud   string `json:"aud"`
		Exp   int64  `json:"exp"`
		Iat   int64  `json:"iat"`
	}{creds.ClientEmail, cloudPlatformScope, oauthTokenURL, issued + 3600, issued})
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(payload))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return payload + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

var pemArmor = regexp.MustCompile(`-----(BEGIN|END) [^-]+-----`)

// parsePrivateKey is pemToPkcs8 plus the key import: the PEM body with its
// armor and whitespace removed. NeoProxy stores service account keys
// re-encoded as PKCS#1, so that form is read too; the RSA key, and so the
// signature, is the same either way.
func parsePrivateKey(pem string) (*rsa.PrivateKey, error) {
	body := strings.Join(strings.Fields(pemArmor.ReplaceAllString(pem, "")), "")
	if body == "" {
		return nil, &CredentialError{Message: "Invalid PEM: empty body", Configuration: true}
	}
	der, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return nil, &CredentialError{Message: "Invalid PEM: " + err.Error(), Configuration: true}
	}
	if parsed, errPKCS8 := x509.ParsePKCS8PrivateKey(der); errPKCS8 == nil {
		if key, ok := parsed.(*rsa.PrivateKey); ok {
			return key, nil
		}
		return nil, &CredentialError{Message: "Vertex AI service account key is not an RSA key", Configuration: true}
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		return nil, &CredentialError{Message: "Vertex AI service account key is not a PKCS#8 or PKCS#1 RSA key", Configuration: true}
	}
	return key, nil
}

// fetchMetadataToken asks the metadata server, bounded at two seconds; any
// failure is "no token".
func fetchMetadataToken(ctx context.Context, client *http.Client) (tokenResponse, bool) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataTokenURL, nil)
	if err != nil {
		return tokenResponse{}, false
	}
	request.Header.Set("Metadata-Flavor", "Google")
	status, data, err := do(client, request)
	if err != nil || status < 200 || status > 299 {
		return tokenResponse{}, false
	}
	var token tokenResponse
	if json.Unmarshal(data, &token) != nil {
		return tokenResponse{}, false
	}
	return token, true
}

func do(client *http.Client, request *http.Request) (int, []byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	return response.StatusCode, data, err
}

// marshalJSON is JSON.stringify for these records: no HTML escaping.
func marshalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

// formEncode is URLSearchParams.toString: pairs in order, with the
// application/x-www-form-urlencoded byte set (alphanumerics and *-._ kept,
// space as +, everything else percent-encoded).
func formEncode(pairs [][2]string) string {
	var out strings.Builder
	for i, pair := range pairs {
		if i > 0 {
			out.WriteByte('&')
		}
		formEscape(&out, pair[0])
		out.WriteByte('=')
		formEscape(&out, pair[1])
	}
	return out.String()
}

func formEscape(out *strings.Builder, value string) {
	const hexDigits = "0123456789ABCDEF"
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '*', c == '-', c == '.', c == '_':
			out.WriteByte(c)
		case c == ' ':
			out.WriteByte('+')
		default:
			out.WriteByte('%')
			out.WriteByte(hexDigits[c>>4])
			out.WriteByte(hexDigits[c&15])
		}
	}
}

// resetTokenCache clears every cached token (a test seam, as NeoPi's).
func resetTokenCache() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	cache = map[string]cachedToken{}
}
