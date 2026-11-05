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
	neoPiModelsDevSession.payload, neoPiModelsDevSession.etag = nil, ""
	defer func() {
		neoPiModelsDevURL = previous
		neoPiModelsDevSession.payload, neoPiModelsDevSession.etag = nil, ""
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
