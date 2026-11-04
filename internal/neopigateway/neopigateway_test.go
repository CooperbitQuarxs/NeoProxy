package neopigateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseLink(t *testing.T) {
	for _, tc := range []struct{ link, base, token, name string }{
		{"neopi-gateway://192.168.1.31:4000?token=Zq3_-x8&name=blackswan", "http://192.168.1.31:4000", "Zq3_-x8", "blackswan"},
		// The vector NeoPi's formatAuthGatewayLink produces for an IPv6 host behind a TLS proxy.
		{"neopi-gateway://[fd7a::1]:443/npi?token=a%2Bb%2Fc%3D&name=office+box&tls=1", "https://[fd7a::1]:443/npi", "a+b/c=", "office box"},
		{" neopi-gateway://gateway.lan?token=t&future=x ", "http://gateway.lan:4000", "t", "gateway.lan"},
	} {
		got, err := ParseLink(tc.link)
		if err != nil {
			t.Fatalf("%s: %v", tc.link, err)
		}
		if got.BaseURL != tc.base || got.Token != tc.token || got.Name != tc.name {
			t.Fatalf("%s: got %+v", tc.link, got)
		}
	}
	for _, bad := range []string{"http://h:4000?token=t", "neopi-gateway://h:4000?name=n", "neopi-gateway://u:p@h:4000?token=t", "neopi-gateway://h:99999?token=t", "neopi-gateway://?token=t", ""} {
		if _, err := ParseLink(bad); !errors.Is(err, ErrInvalidLink) {
			t.Fatalf("%q: want ErrInvalidLink, got %v", bad, err)
		}
	}
}

// fakeGateway answers like `npi auth-gateway serve` with bearer auth.
func fakeGateway(t *testing.T, token string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"ok":true,"version":"test"}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v1/neoproxy/accounts" {
			_, _ = w.Write([]byte(`{"data":[{"id":"1"},{"id":"2"}]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func writeProfile(t *testing.T, root string, gatewayURL, auth, token string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if gatewayURL != "" {
		u, _ := url.Parse(gatewayURL)
		port, _ := strconv.Atoi(u.Port())
		raw, _ := json.Marshal(map[string]any{"pid": 1, "hostname": u.Hostname(), "port": port, "auth": auth, "startedAt": 0, "version": "test"})
		if err := os.WriteFile(filepath.Join(root, RuntimeFileName), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if token != "" {
		if err := os.WriteFile(filepath.Join(root, TokenFileName), []byte(token+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetect(t *testing.T) {
	ctx := t.Context()
	gateway := fakeGateway(t, "gateway-secret")

	missing := Detect(ctx, filepath.Join(t.TempDir(), "absent"), nil)
	if missing.Found || missing.Reason == "" {
		t.Fatalf("missing profile: %+v", missing)
	}

	root := filepath.Join(t.TempDir(), ".omp")
	writeProfile(t, root, gateway.URL, "token", "gateway-secret")
	got := Detect(ctx, root, nil)
	if !got.Found || !got.Running || got.URL != gateway.URL || got.AccountCount == nil || *got.AccountCount != 2 || !got.Connectable() || got.Token() != "gateway-secret" || got.Reason != "" {
		t.Fatalf("running gateway: %+v", got)
	}
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), "gateway-secret") {
		t.Fatalf("detection JSON leaked the token: %s", raw)
	}

	rotated := filepath.Join(t.TempDir(), ".omp")
	writeProfile(t, rotated, gateway.URL, "token", "regenerated")
	if got = Detect(ctx, rotated, nil); !got.Running || got.Connectable() || got.AccountCount != nil || got.Reason == "" {
		t.Fatalf("rejected token: %+v", got)
	}

	noAuth := filepath.Join(t.TempDir(), ".omp")
	writeProfile(t, noAuth, gateway.URL, "none", "")
	if got = Detect(ctx, noAuth, nil); !got.Running || got.Connectable() || got.Reason == "" {
		t.Fatalf("--no-auth gateway: %+v", got)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	closedURL := closed.URL
	closed.Close()
	stopped := filepath.Join(t.TempDir(), ".omp")
	writeProfile(t, stopped, closedURL, "token", "gateway-secret")
	if got = Detect(ctx, stopped, nil); !got.Found || got.Running || got.StartCommand != StartCommand || got.Connectable() {
		t.Fatalf("stopped gateway: %+v", got)
	}
}
