package cliproxy

import (
	"context"
	"net/http"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// neoPiOllamaShows holds one /api/show cache per Ollama server (keyed by its
// normalized base URL), kept for the process as NeoPi's model manager keeps
// its own for its lifetime.
var neoPiOllamaShows sync.Map

// fetchNeoPiOllamaModels is ollamaModelManagerOptions' fetchDynamicModels:
// GET <base>/models (Accept: application/json, the key as a bearer token when
// there is one), a POST /api/show per listed model (Content-Type and Accept,
// never the key), and GET /api/tags (Accept only) when the listing fails or
// names nothing. NeoPi bounds the listing with a deadline; NeoProxy sets none
// after the connection is established.
func fetchNeoPiOllamaModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, key, base string) ([]*ModelInfo, error) {
	server := registry.NeoPiOllamaBaseURL(base)
	cache, ok := neoPiOllamaShows.Load(server)
	if !ok {
		cache, _ = neoPiOllamaShows.LoadOrStore(server, registry.NewNeoPiOllamaShows())
	}
	return registry.NeoPiOllamaListing(base, cache.(*registry.NeoPiOllamaShows), func(endpoint string, body []byte, authorized bool) (int, []byte, error) {
		method := http.MethodGet
		header := http.Header{}
		if body != nil {
			method = http.MethodPost
			header.Set("Content-Type", "application/json")
		}
		header.Set("Accept", "application/json")
		if authorized && key != "" {
			header.Set("Authorization", "Bearer "+key)
		}
		return neoPiDiscoveryDo(ctx, cfg, auth, registry.NeoPiOllamaProvider, method, endpoint, header, body)
	})
}
