package cliproxy

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// fetchNeoPiLiteLLMModels is litellmModelManagerOptions' fetchDynamicModels:
// the models.dev catalog first (optional hydration), then the proxy's
// management endpoints and, failing those, its /models listing, each a GET
// with Accept: application/json and the key as a bearer token when there is
// one. NeoPi bounds the management walk with a deadline; NeoProxy sets none
// after the connection is established.
func fetchNeoPiLiteLLMModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, key, base string) ([]*ModelInfo, error) {
	modelsDev := neoPiFetchModelsDev(ctx, cfg, auth)
	header := http.Header{}
	header.Set("Accept", "application/json")
	if key != "" {
		header.Set("Authorization", "Bearer "+key)
	}
	return registry.NeoPiLiteLLMListing(base, modelsDev, func(endpoint string) (int, []byte, error) {
		return neoPiDiscoveryDo(ctx, cfg, auth, registry.NeoPiLiteLLMProvider, endpoint, header.Clone())
	})
}
