package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// SiliconFlow joins its listing with the models.dev catalog: a zstd catalog
// is decoded, a 304 reuses the last catalog, and a failed fetch falls back to
// it rather than dropping the hydration.
func TestNeoPiSiliconFlowModelsDevCatalog(t *testing.T) {
	catalog := []byte(`{"siliconflow":{"models":{"zai-org/GLM-5.1":{"name":"GLM From Catalog","reasoning":true,"modalities":{"input":["text","image"]},"limit":{"context":202752,"output":131072}}}}}`)
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(catalog, nil)
	if err = encoder.Close(); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var conditional []string
	status := http.StatusOK
	modelsDev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Accept") != "application/zstd, application/json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		conditional = append(conditional, r.Header.Get("If-None-Match"))
		switch {
		case status != http.StatusOK:
			w.WriteHeader(status)
		case r.Header.Get("If-None-Match") == `"v1"`:
			w.WriteHeader(http.StatusNotModified)
		default:
			w.Header().Set("ETag", `"v1"`)
			_, _ = w.Write(compressed)
		}
	}))
	defer modelsDev.Close()
	listing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"zai-org/GLM-5.1"},{"id":"BAAI/bge-m3"}]}`))
	}))
	defer listing.Close()
	previous := neoPiModelsDevURL
	neoPiModelsDevURL = modelsDev.URL
	resetNeoPiModelsDevSessions()
	defer func() {
		neoPiModelsDevURL = previous
		resetNeoPiModelsDevSessions()
	}()

	auth := &coreauth.Auth{ID: "s", Provider: "siliconflow", Attributes: map[string]string{"api_key": "sk-silicon", "base_url": listing.URL + "/v1"}}
	discover := func() {
		t.Helper()
		models, errFetch := fetchNeoPiCompletionsModels("siliconflow")(context.Background(), &config.Config{}, auth)
		if errFetch != nil {
			t.Fatal(errFetch)
		}
		if len(models) != 1 || models[0].ID != "zai-org/GLM-5.1" || models[0].DisplayName != "GLM From Catalog" || models[0].ContextLength != 202752 {
			t.Fatalf("roster = %+v", models)
		}
	}
	discover()
	discover() // revalidated: 304
	mu.Lock()
	status = http.StatusBadGateway
	mu.Unlock()
	discover() // failed fetch: the last catalog still hydrates

	mu.Lock()
	defer mu.Unlock()
	if len(conditional) != 3 || conditional[0] != "" || conditional[1] != `"v1"` || conditional[2] != `"v1"` {
		t.Fatalf("If-None-Match sequence = %q", conditional)
	}
}

func resetNeoPiModelsDevSessions() {
	neoPiModelsDevSessions.Range(func(key, _ any) bool {
		neoPiModelsDevSessions.Delete(key)
		return true
	})
}

// Callers on one route share a single catalog request, and a caller whose
// context ends stops waiting instead of holding up the others.
func TestNeoPiModelsDevCatalogSharedRequest(t *testing.T) {
	release, arrived := make(chan struct{}), make(chan struct{}, 4)
	var mu sync.Mutex
	requests := 0
	modelsDev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		arrived <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"siliconflow":{"models":{}}}`))
	}))
	defer modelsDev.Close()
	previous := neoPiModelsDevURL
	neoPiModelsDevURL = modelsDev.URL
	resetNeoPiModelsDevSessions()
	defer func() {
		neoPiModelsDevURL = previous
		resetNeoPiModelsDevSessions()
	}()
	auth := &coreauth.Auth{ID: "s", Provider: "siliconflow"}

	// The first caller's request is in flight before the second arrives,
	// so the second can only join it.
	waited := make(chan []byte)
	go func() { waited <- neoPiFetchModelsDev(context.Background(), &config.Config{}, auth) }()
	<-arrived
	cancelled, cancel := context.WithCancel(context.Background())
	gaveUp := make(chan []byte)
	go func() { gaveUp <- neoPiFetchModelsDev(cancelled, &config.Config{}, auth) }()
	cancel()
	if payload := <-gaveUp; payload != nil {
		t.Fatalf("cancelled caller got %q before any catalog arrived", payload)
	}
	close(release)
	if payload := <-waited; string(payload) != `{"siliconflow":{"models":{}}}` {
		t.Fatalf("waiting caller got %q", payload)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 1 {
		t.Fatalf("catalog requests = %d, want one shared request", requests)
	}
}

// blockingTransport holds every request until released.
type blockingTransport struct {
	entered chan struct{}
	release chan struct{}
}

func (b *blockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	b.entered <- struct{}{}
	<-b.release
	return http.DefaultTransport.RoundTrip(req)
}

// directTransport passes requests straight through.
type directTransport struct{}

func (*directTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return http.DefaultTransport.RoundTrip(req)
}

// Callers on different context transports keep separate sessions: one
// stalled transport does not hold up another.
func TestNeoPiModelsDevCatalogSessionPerTransport(t *testing.T) {
	modelsDev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"siliconflow":{"models":{}}}`))
	}))
	defer modelsDev.Close()
	previous := neoPiModelsDevURL
	neoPiModelsDevURL = modelsDev.URL
	defer func() {
		neoPiModelsDevURL = previous
		resetNeoPiModelsDevSessions()
	}()
	blockingFunc := func(entered, release chan struct{}) http.RoundTripper {
		return claudeModelsTransport(func(r *http.Request) (*http.Response, error) {
			entered <- struct{}{}
			<-release
			return http.DefaultTransport.RoundTrip(r)
		})
	}
	for _, tc := range []struct {
		name    string
		cfg     *config.Config
		stalled func(entered, release chan struct{}) http.RoundTripper
		healthy http.RoundTripper
	}{
		{"pointer transports", &config.Config{}, func(entered, release chan struct{}) http.RoundTripper {
			return &blockingTransport{entered: entered, release: release}
		}, &directTransport{}},
		{"closures of one type", &config.Config{}, blockingFunc, claudeModelsTransport(http.DefaultTransport.RoundTrip)},
		{"non-comparable values", &config.Config{}, func(entered, release chan struct{}) http.RoundTripper {
			return sliceTransport{hops: []string{"stalled"}, entered: entered, release: release}
		}, sliceTransport{hops: []string{"healthy"}}},
		// An unbuildable proxy falls back to each context's own transport.
		{"rejected proxy", &config.Config{SDKConfig: config.SDKConfig{ProxyURL: "ftp://proxy.example"}}, func(entered, release chan struct{}) http.RoundTripper {
			return &blockingTransport{entered: entered, release: release}
		}, &directTransport{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetNeoPiModelsDevSessions()
			auth := &coreauth.Auth{ID: "s", Provider: "siliconflow"}
			entered, release := make(chan struct{}, 1), make(chan struct{})
			stalledCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", tc.stalled(entered, release)) //nolint:staticcheck // the engine's context key
			healthyCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", tc.healthy)                   //nolint:staticcheck // the engine's context key
			stalledDone := make(chan []byte)
			go func() { stalledDone <- neoPiFetchModelsDev(stalledCtx, tc.cfg, auth) }()
			<-entered
			if payload := neoPiFetchModelsDev(healthyCtx, tc.cfg, auth); string(payload) != `{"siliconflow":{"models":{}}}` {
				t.Fatalf("healthy transport got %q while another transport stalled", payload)
			}
			close(release)
			if payload := <-stalledDone; string(payload) != `{"siliconflow":{"models":{}}}` {
				t.Fatalf("stalled transport got %q once released", payload)
			}
		})
	}
}

// sliceTransport is a non-comparable value transport; it blocks while its
// release channel is open.
type sliceTransport struct {
	hops    []string
	entered chan struct{}
	release chan struct{}
}

func (s sliceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if s.release != nil {
		s.entered <- struct{}{}
		<-s.release
	}
	return http.DefaultTransport.RoundTrip(req)
}
