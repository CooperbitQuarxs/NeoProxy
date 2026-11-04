package devin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/devinwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/oauthpage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// NeoPi's `auth "devin"` rule: an oauth-code login with PKCE and a uuid state
// against app.devin.ai, a JSON code exchange at api.devin.ai, and no refresh
// grant.
const (
	// DefaultAppBaseURL hosts the authorize page.
	DefaultAppBaseURL = "https://app.devin.ai"
	// DefaultAPIBaseURL hosts the token exchange.
	DefaultAPIBaseURL = "https://api.devin.ai"
	// DefaultServerURL is the Cascade backend serving seat management.
	DefaultServerURL = devinwire.DefaultBaseURL
	// CallbackPort is the loopback port the rule prefers; another free port
	// serves when it is taken.
	CallbackPort = 59653
	// expirySkew and expiryFallback are the rule's `expires "jwt"` projection.
	expirySkew     = 5 * time.Minute
	expiryFallback = 365 * 24 * time.Hour
)

// Credential is NeoPi's projected Devin credential: the session token and
// when it expires.
type Credential struct {
	Access  string
	Expires time.Time
}

// DevinAuthService coordinates Devin PKCE authorization and token exchange.
type DevinAuthService struct {
	client        *http.Client
	appBaseURL    string
	apiBaseURL    string
	serverBaseURL string
	now           func() time.Time
}

// NewDevinAuthService creates a new Devin authentication service instance.
func NewDevinAuthService(client *http.Client) *DevinAuthService {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &DevinAuthService{
		client:        client,
		appBaseURL:    DefaultAppBaseURL,
		apiBaseURL:    DefaultAPIBaseURL,
		serverBaseURL: DefaultServerURL,
		now:           time.Now,
	}
}

// SetServerBaseURL overrides the seat management base URL (used in tests).
func (s *DevinAuthService) SetServerBaseURL(url string) {
	if s != nil && strings.TrimSpace(url) != "" {
		s.serverBaseURL = strings.TrimRight(strings.TrimSpace(url), "/")
	}
}

// SetAPIBaseURL overrides the token exchange base URL (used in tests).
func (s *DevinAuthService) SetAPIBaseURL(url string) {
	if s != nil && strings.TrimSpace(url) != "" {
		s.apiBaseURL = strings.TrimRight(strings.TrimSpace(url), "/")
	}
}

// SetAppBaseURL overrides the user-facing OAuth authorization base URL (used in tests).
func (s *DevinAuthService) SetAppBaseURL(url string) {
	if s != nil && strings.TrimSpace(url) != "" {
		s.appBaseURL = strings.TrimRight(strings.TrimSpace(url), "/")
	}
}

// BuildAuthorizationURL is the rule's authorize URL: the standard PKCE
// parameters in NeoPi's order, then prompt=select_account.
func (s *DevinAuthService) BuildAuthorizationURL(redirectURI, codeChallenge, state string) string {
	parts := []string{
		"response_type=code",
		"redirect_uri=" + url.QueryEscape(redirectURI),
		"code_challenge=" + url.QueryEscape(codeChallenge),
		"code_challenge_method=S256",
	}
	if state != "" {
		parts = append(parts, "state="+url.QueryEscape(state))
	}
	parts = append(parts, "prompt=select_account")
	return s.appBaseURL + "/auth/cli/continue?" + strings.Join(parts, "&")
}

// ExchangeCodeForToken posts the code and verifier as JSON and projects the
// response's token into a credential. A code echoed as `code#state` is
// exchanged without its fragment.
func (s *DevinAuthService) ExchangeCodeForToken(ctx context.Context, code, codeVerifier string) (Credential, error) {
	if cut := strings.IndexByte(code, '#'); cut >= 0 {
		code = code[:cut]
	}
	body, errMarshal := json.Marshal(struct {
		Code         string `json:"code"`
		CodeVerifier string `json:"code_verifier"`
	}{code, codeVerifier})
	if errMarshal != nil {
		return Credential{}, errMarshal
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBaseURL+"/auth/cli/token", bytes.NewReader(body))
	if errReq != nil {
		return Credential{}, errReq
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", devinwire.BunUserAgent)
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return Credential{}, fmt.Errorf("devin request to %s failed: %w", req.URL, errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	decoded, errDecode := util.DecodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if errDecode != nil {
		return Credential{}, fmt.Errorf("devin token exchange: %w", errDecode)
	}
	defer func() { _ = decoded.Close() }()
	text, errRead := io.ReadAll(io.LimitReader(decoded, 1<<20))
	if errRead != nil {
		return Credential{}, fmt.Errorf("devin token exchange: %w", errRead)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Credential{}, fmt.Errorf("devin token exchange failed: %d %s", resp.StatusCode, excerpt(string(text)))
	}
	access := ""
	if token := gjson.GetBytes(text, "token"); gjson.ValidBytes(text) {
		switch token.Type {
		case gjson.String:
			access = token.Str
		case gjson.Number:
			if !math.IsInf(token.Num, 0) && !math.IsNaN(token.Num) {
				access = token.Raw
			}
		}
	}
	if access == "" {
		shown := string(text)
		var compact bytes.Buffer
		if json.Compact(&compact, text) == nil {
			shown = compact.String()
		}
		return Credential{}, fmt.Errorf("devin token response missing access token: %s", excerpt(shown))
	}
	return Credential{Access: access, Expires: s.credentialExpiry(access)}, nil
}

// credentialExpiry is the JWT's exp less the skew, else a year from now.
func (s *DevinAuthService) credentialExpiry(access string) time.Time {
	if parts := strings.Split(access, "."); len(parts) == 3 && parts[1] != "" {
		if payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "=")); err == nil {
			if exp := gjson.GetBytes(payload, "exp"); gjson.ValidBytes(payload) && exp.Type == gjson.Number {
				return time.UnixMilli(int64(exp.Num * 1000)).Add(-expirySkew)
			}
		}
	}
	return s.now().Add(expiryFallback)
}

func excerpt(text string) string {
	if len(text) > 500 {
		return text[:500]
	}
	return text
}

// OAuthServer handles local loopback HTTP callbacks for Devin authentication.
type OAuthServer struct {
	server     *http.Server
	listener   net.Listener
	port       int
	resultChan chan *OAuthResult
	errorChan  chan error
	mu         sync.Mutex
	running    bool
}

// OAuthResult carries the authorization code from the browser callback.
type OAuthResult struct {
	Code  string
	State string
	Error string
}

// NewOAuthServer creates a local loopback server for Devin OAuth.
func NewOAuthServer(port int) *OAuthServer {
	return &OAuthServer{
		port:       port,
		resultChan: make(chan *OAuthResult, 1),
		errorChan:  make(chan error, 1),
	}
}

// Start initiates the local HTTP server. If port is 0, an available ephemeral port is chosen.
func (s *OAuthServer) Start() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return s.port, nil
	}

	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, errLn := net.Listen("tcp", addr)
	if errLn != nil {
		return 0, fmt.Errorf("failed to bind local OAuth server to %s: %w", addr, errLn)
	}

	s.listener = ln
	s.port = ln.Addr().(*net.TCPAddr).Port

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", s.handleCallback)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/callback" {
			s.handleCallback(w, r)
			return
		}
		http.NotFound(w, r)
	})

	s.server = &http.Server{
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	s.running = true
	go func() {
		if errServe := s.server.Serve(ln); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
			log.Debugf("devin oauth server error: %v", errServe)
			select {
			case s.errorChan <- errServe:
			default:
			}
		}
	}()

	return s.port, nil
}

// Stop gracefully stops the server.
func (s *OAuthServer) Stop(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}
	s.running = false
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// WaitForCallback waits for the browser redirect or times out.
func (s *OAuthServer) WaitForCallback(timeout time.Duration) (*OAuthResult, error) {
	return s.WaitForCallbackWithContext(context.Background(), timeout)
}

// WaitForCallbackWithContext waits for the browser redirect, context cancellation, or times out.
func (s *OAuthServer) WaitForCallbackWithContext(ctx context.Context, timeout time.Duration) (*OAuthResult, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case res := <-s.resultChan:
		return res, nil
	case err := <-s.errorChan:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		return nil, errors.New("devin authentication timed out")
	}
}

func (s *OAuthServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := strings.TrimSpace(q.Get("code"))
	state := strings.TrimSpace(q.Get("state"))
	errStr := strings.TrimSpace(q.Get("error"))
	errDesc := strings.TrimSpace(q.Get("error_description"))

	if errStr != "" || code == "" {
		errMsg := errStr
		if errDesc != "" {
			errMsg = fmt.Sprintf("%s: %s", errStr, errDesc)
		}
		if errMsg == "" {
			errMsg = "missing authorization code"
		}
		oauthpage.Write(w, http.StatusBadRequest, oauthpage.Page{Provider: "devin", Detail: errMsg})
		select {
		case s.resultChan <- &OAuthResult{Error: errMsg}:
		default:
		}
		return
	}

	oauthpage.Write(w, http.StatusOK, oauthpage.Page{Provider: "devin", Success: true})

	select {
	case s.resultChan <- &OAuthResult{Code: code, State: state}:
	default:
	}
}
