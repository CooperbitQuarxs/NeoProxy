// Package neopigateway pairs NeoProxy with a NeoPi auth gateway: it parses the
// one-paste connection links NeoPi prints (`npi auth-gateway link`) and detects
// a gateway run by the same user on the same machine, reading NeoPi's files
// read-only.
package neopigateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// LinkScheme is the scheme of a NeoPi gateway connection link.
	LinkScheme = "neopi-gateway"
	// DefaultPort is the port NeoPi's gateway listens on without --bind.
	DefaultPort = 4000
	// TokenFileName holds the gateway bearer inside NeoPi's config directory.
	TokenFileName = "auth-gateway.token"
	// RuntimeFileName is written by a running `npi auth-gateway serve` next to
	// the token and removed on clean shutdown.
	RuntimeFileName = "auth-gateway.runtime.json"
	// StartCommand starts NeoPi's gateway on its default loopback bind.
	StartCommand = "npi auth-gateway serve"
)

// ErrInvalidLink reports a connection link that cannot be used to pair.
var ErrInvalidLink = errors.New("invalid connection link")

// Link is a parsed connection link:
//
//	neopi-gateway://<host>:<port>[/<base-path>]?token=<bearer>&name=<label>[&tls=1]
//
// tls=1 selects HTTPS; unknown parameters are ignored.
type Link struct {
	// BaseURL is the gateway's HTTP(S) base URL without a trailing slash.
	BaseURL string
	Token   string
	// Name is the gateway label; the link host when the link carries none.
	Name string
}

func linkError(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidLink, reason) }

// ParseLink validates a connection link and returns the gateway it names.
func ParseLink(raw string) (Link, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(u.Scheme, LinkScheme) {
		return Link{}, linkError("expected neopi-gateway://host:port?token=… (run `npi auth-gateway link` on the NeoPi machine)")
	}
	if u.User != nil {
		return Link{}, linkError("the link must not carry user info")
	}
	host := u.Hostname()
	if host == "" {
		return Link{}, linkError("the link has no host")
	}
	port := DefaultPort
	if p := u.Port(); p != "" {
		if port, err = strconv.Atoi(p); err != nil || port < 1 || port > 65535 {
			return Link{}, linkError("the port must be 1-65535")
		}
	}
	q := u.Query()
	token := q.Get("token")
	if token == "" {
		return Link{}, linkError("the link has no token")
	}
	scheme := "http"
	if q.Get("tls") == "1" {
		scheme = "https"
	}
	name := strings.TrimSpace(q.Get("name"))
	if name == "" {
		name = host
	}
	return Link{
		BaseURL: scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + strings.TrimRight(u.EscapedPath(), "/"),
		Token:   token,
		Name:    name,
	}, nil
}

// DefaultConfigRoot is NeoPi's config directory for the user running
// NeoProxy, resolved the way NeoPi resolves it: $HOME/$PI_CONFIG_DIR (default
// .omp), under profiles/$PI_PROFILE when a profile is selected.
func DefaultConfigRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	dir := strings.TrimSpace(os.Getenv("PI_CONFIG_DIR"))
	if dir == "" {
		dir = ".omp"
	}
	root := filepath.Join(home, dir)
	if profile := strings.TrimSpace(os.Getenv("PI_PROFILE")); profile != "" {
		root = filepath.Join(root, "profiles", profile)
	}
	return root, nil
}

type runtimeFile struct {
	PID      int    `json:"pid"`
	Hostname string `json:"hostname"`
	Port     int    `json:"port"`
	Auth     string `json:"auth"`
}

// Local describes a NeoPi gateway on this machine. It never carries the
// token in its JSON form.
type Local struct {
	Found   bool   `json:"found"`
	Running bool   `json:"running"`
	URL     string `json:"url,omitempty"`
	Name    string `json:"name,omitempty"`
	// AccountCount is set once the gateway accepted the local token.
	AccountCount *int   `json:"accountCount,omitempty"`
	Reason       string `json:"reason,omitempty"`
	// StartCommand is the NeoPi command that starts the gateway when it is not running.
	StartCommand string `json:"startCommand,omitempty"`
	token        string
}

// Token is the gateway bearer read from NeoPi's config directory.
func (l Local) Token() string { return l.token }

// Connectable reports whether NeoProxy can add the gateway as a source now.
func (l Local) Connectable() bool { return l.Running && l.token != "" && l.AccountCount != nil }

const probeTimeout = 3 * time.Second

// Detect looks for a NeoPi gateway under configRoot (NeoPi's config
// directory, e.g. ~/.omp) and probes it. NeoPi's files are only read.
func Detect(ctx context.Context, configRoot string, client *http.Client) Local {
	var out Local
	if info, err := os.Stat(configRoot); err != nil || !info.IsDir() {
		out.Reason = "NeoPi is not set up for the account NeoProxy runs as (no " + configRoot + ")."
		return out
	}
	if out.Name, _ = os.Hostname(); out.Name == "" {
		out.Name = "NeoPi"
	}
	out.Found = true
	host, port, auth := "127.0.0.1", DefaultPort, ""
	if raw, err := os.ReadFile(filepath.Join(configRoot, RuntimeFileName)); err == nil {
		var rt runtimeFile
		if json.Unmarshal(raw, &rt) == nil && rt.Port > 0 && rt.Port <= 65535 {
			port, auth = rt.Port, rt.Auth
			switch h := strings.Trim(rt.Hostname, "[]"); h {
			case "", "0.0.0.0":
			case "::":
				host = "::1"
			default:
				host = h
			}
		}
	}
	out.URL = "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	if raw, err := os.ReadFile(filepath.Join(configRoot, TokenFileName)); err == nil {
		out.token = strings.TrimSpace(string(raw))
	}
	if client == nil {
		client = &http.Client{}
	}
	// Never follow redirects: the bearer must only reach the gateway itself.
	probe := *client
	probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if !healthy(ctx, &probe, out.URL) {
		out.Reason = "NeoPi's gateway is not running. Start it on this machine, then check again."
		out.StartCommand = StartCommand
		return out
	}
	out.Running = true
	switch {
	case auth == "none":
		out.Reason = "NeoPi's gateway runs with --no-auth. Restart it without --no-auth so NeoProxy can sign its requests."
		return out
	case out.token == "":
		out.Reason = "NeoPi's gateway has no token file. Restart `" + StartCommand + "` to create one."
		return out
	}
	count, status, err := accountCount(ctx, &probe, out.URL, out.token)
	switch {
	case err == nil:
		out.AccountCount = &count
	case status == http.StatusUnauthorized:
		out.Reason = "NeoPi's gateway rejected its token file; the token was probably regenerated. Restart the gateway, then check again."
	default:
		out.Reason = "NeoPi's gateway is running but did not list its accounts (" + err.Error() + ")."
	}
	return out
}

func healthy(ctx context.Context, client *http.Client, base string) bool {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var body struct {
		OK bool `json:"ok"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&body) == nil && body.OK
}

func accountCount(ctx context.Context, client *http.Client, base, token string) (int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/neoproxy/accounts", nil)
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, 0, errors.New("connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var body struct {
		Data []json.RawMessage `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 8*1024*1024)).Decode(&body) != nil || body.Data == nil {
		return 0, resp.StatusCode, errors.New("invalid account list")
	}
	return len(body.Data), resp.StatusCode, nil
}
