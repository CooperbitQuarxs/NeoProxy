package api

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
)

func TestMuxSessionOriginAndCookieTransport(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		scheme := "http"
		if encrypted {
			scheme = "https"
		}
		t.Run(scheme, func(t *testing.T) {
			store, err := neo.Open(filepath.Join(t.TempDir(), "identity.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if errClose := store.Close(); errClose != nil {
					t.Errorf("close identity store: %v", errClose)
				}
			})
			if _, _, err = store.Bootstrap(context.Background(), "mux-admin", "Mux admin", "mux-test-password-123"); err != nil {
				t.Fatal(err)
			}
			engine := gin.New()
			neo.NewHTTP(store).RegisterRoutes(engine, func(c *gin.Context) { c.AbortWithStatus(http.StatusUnauthorized) })
			srv := httptest.NewUnstartedServer(engine)
			var source net.Listener = srv.Listener
			client := &http.Client{Transport: &http.Transport{}}
			if encrypted {
				certificateFixture := httptest.NewTLSServer(http.NotFoundHandler())
				serverTLS := certificateFixture.TLS.Clone()
				serverTLS.NextProtos = nil // Exercise buffered TLS without the ALPN fast path.
				client = certificateFixture.Client()
				transport := client.Transport.(*http.Transport).Clone()
				transport.TLSClientConfig = transport.TLSClientConfig.Clone()
				transport.TLSClientConfig.NextProtos = nil
				transport.ForceAttemptHTTP2 = false
				client.Transport = transport
				certificateFixture.Close()
				source = tls.NewListener(source, serverTLS)
			}
			mux := newMuxListener(source.Addr(), 16)
			srv.Listener = mux
			srv.Start()
			acceptResult := make(chan error, 1)
			go func() { acceptResult <- (&Server{}).acceptMuxConnections(source, mux) }()
			t.Cleanup(func() {
				client.CloseIdleConnections()
				if errClose := source.Close(); errClose != nil {
					t.Errorf("close source listener: %v", errClose)
				}
				srv.Close()
				if errAccept := normalizeListenerError(<-acceptResult); errAccept != nil {
					t.Errorf("accept multiplexed connections: %v", errAccept)
				}
			})
			origin := scheme + "://" + source.Addr().String()
			login := func(requestOrigin string) *http.Response {
				t.Helper()
				req, errRequest := http.NewRequest(http.MethodPost, origin+"/v8/session", strings.NewReader(`{"handle":"mux-admin","password":"mux-test-password-123"}`))
				if errRequest != nil {
					t.Fatal(errRequest)
				}
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Origin", requestOrigin)
				req.Header.Set("Sec-Fetch-Site", "same-origin")
				response, errDo := client.Do(req)
				if errDo != nil {
					t.Fatal(errDo)
				}
				if _, errRead := io.Copy(io.Discard, response.Body); errRead != nil {
					t.Errorf("read session response: %v", errRead)
				}
				if errClose := response.Body.Close(); errClose != nil {
					t.Errorf("close session response: %v", errClose)
				}
				return response
			}
			wrongScheme := "https"
			if encrypted {
				wrongScheme = "http"
			}
			if response := login(wrongScheme + "://" + source.Addr().String()); response.StatusCode != http.StatusForbidden {
				t.Fatalf("cross-scheme origin accepted: status %d", response.StatusCode)
			}
			response := login(origin)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("actual same-origin login rejected: status %d", response.StatusCode)
			}
			for _, cookie := range response.Cookies() {
				if cookie.Name == neo.SessionCookie {
					if cookie.Secure != encrypted || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
						t.Fatalf("session cookie does not match transport security: secure=%t httpOnly=%t sameSite=%v", cookie.Secure, cookie.HttpOnly, cookie.SameSite)
					}
					return
				}
			}
			t.Fatal("successful login omitted its session cookie")
		})
	}
}
