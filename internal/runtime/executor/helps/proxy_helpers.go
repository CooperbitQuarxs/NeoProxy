package helps

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/httptransport"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// NewProxyAwareHTTPClient creates an HTTP client with proper proxy configuration priority:
// 1. Use the execution-scoped request proxy if configured (highest priority)
// 2. Use auth.ProxyURL if configured
// 3. Use cfg.ProxyURL if auth proxy is not configured
// 4. Use RoundTripper from context if none are configured
//
// Parameters:
//   - ctx: The context containing optional RoundTripper
//   - cfg: The application configuration
//   - auth: The authentication information
//   - timeout: The client timeout (0 means no timeout)
//
// Returns:
//   - *http.Client: An HTTP client with configured proxy or transport
func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	return SelectTransport(ctx, cfg, auth).Client(ctx, timeout)
}

// TransportSelection is the network NewProxyAwareHTTPClient chooses for a
// request context.
type TransportSelection struct {
	// ProxyURL is the configured proxy in use; empty when none is configured
	// or its transport could not be built.
	ProxyURL string
	// Transport is the proxy transport, else the context's RoundTripper;
	// nil selects the default transport.
	Transport http.RoundTripper
}

// SelectTransport makes NewProxyAwareHTTPClient's choice: the execution-scoped
// request proxy, then auth.ProxyURL, then cfg.ProxyURL, and when none is set
// or its transport cannot be built, the context's RoundTripper.
func SelectTransport(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) TransportSelection {
	if proxyURL := EffectiveProxyURL(ctx, cfg, auth); proxyURL != "" {
		if transport := buildProxyTransport(proxyURL); transport != nil {
			return TransportSelection{ProxyURL: proxyURL, Transport: transport}
		}
		// If proxy setup failed, log and fall through to context RoundTripper
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyutil.Redact(proxyURL))
	}
	return TransportSelection{Transport: ContextRoundTripper(ctx)}
}

// Client is an HTTP client over the selected transport (timeout 0 means no
// timeout), decorated for the context.
func (s TransportSelection) Client(ctx context.Context, timeout time.Duration) *http.Client {
	httpClient := &http.Client{Transport: s.Transport}
	if timeout > 0 {
		httpClient.Timeout = timeout
	}
	return httptransport.Decorate(ctx, httpClient)
}

// ContextRoundTripper is the RoundTripper a context carries for clients
// without a configured proxy (typically from RoundTripperFor), nil when none.
func ContextRoundTripper(ctx context.Context) http.RoundTripper {
	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		return rt
	}
	return nil
}

func EffectiveProxyURL(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) string {
	if proxyURL := cliproxyexecutor.RequestProxyURL(ctx); proxyURL != "" {
		return proxyURL
	}
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if cfg != nil {
		return strings.TrimSpace(cfg.ProxyURL)
	}
	return ""
}

// buildProxyTransport creates an HTTP transport configured for the given proxy URL.
// It supports SOCKS5, HTTP, and HTTPS proxy protocols.
//
// Parameters:
//   - proxyURL: The proxy URL string (e.g., "socks5://user:pass@host:port", "http://host:port")
//
// Returns:
//   - *http.Transport: A configured transport, or nil if the proxy URL is invalid
func buildProxyTransport(proxyURL string) *http.Transport {
	transport, _, errBuild := proxyutil.BuildHTTPTransport(proxyURL)
	if errBuild != nil {
		log.Errorf("%v", errBuild)
		return nil
	}
	return transport
}
