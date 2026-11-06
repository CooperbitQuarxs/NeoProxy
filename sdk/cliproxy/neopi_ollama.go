package cliproxy

import (
	"context"
	"net/http"
	"os"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// fetchNeoPiOllamaModels is NeoPi's runtime discovery of a local Ollama
// server (coding-agent discoverOllamaModels): GET /api/tags with no headers
// of its own, then a POST /api/show per entry with only a JSON Content-Type.
// NeoPi's implicit provider configures no headers, so neither request carries
// the account's key. NeoPi bounds both with probe deadlines; NeoProxy sets
// none after the connection is established.
func fetchNeoPiOllamaModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, base string) ([]*ModelInfo, error) {
	return registry.NeoPiOllamaListing(base, os.Getenv, func(endpoint string, body []byte) (int, []byte, error) {
		method := http.MethodGet
		header := http.Header{}
		if body != nil {
			method = http.MethodPost
			header.Set("Content-Type", "application/json")
		}
		return neoPiDiscoveryDo(ctx, cfg, auth, registry.NeoPiOllamaProvider, method, endpoint, header, body)
	})
}
