package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// An unusable /models answer fails discovery without awaiting LM Studio's
// native probes, which here never answer until their request is cancelled.
func TestLmStudioUnusableListingSkipsNativeProbes(t *testing.T) {
	for name, listing := range map[string]string{"malformed": `{"data":[`, "null": `null`, "no entries": `{"object":"list"}`} {
		t.Run(name, func(t *testing.T) {
			// release frees a still-blocked probe so a failing run ends at
			// its guard instead of in server.Close.
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v0/models" || r.URL.Path == "/api/v1/models" {
					select {
					case <-r.Context().Done():
					case <-release:
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(listing))
			}))
			defer server.Close()
			defer close(release)
			auth := &coreauth.Auth{ID: "l", Provider: "lm-studio", Attributes: map[string]string{"base_url": server.URL + "/v1"}}
			done := make(chan error, 1)
			go func() {
				_, err := fetchNeoPiCompletionsModels("lm-studio")(context.Background(), &config.Config{}, auth)
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, registry.ErrNeoPiListing) {
					t.Fatalf("discovery error = %v, want ErrNeoPiListing", err)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("discovery waited on the native probes")
			}
		})
	}
}
