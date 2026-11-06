package cliproxy

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// fetchNeoPiLlamaCppModels is NeoPi's runtime discovery of a llama.cpp
// server (coding-agent discoverLlamaCppModels): GET /models beside GET
// /props at the account's configured base, else the implicit provider's
// (LLAMA_CPP_BASE_URL, else 127.0.0.1:8080 with 127.0.0.1:9931 as its
// fallback). Both carry the key as a bearer when the account holds a real
// one (key arrives with the keyless marker already stripped). NeoPi bounds
// the probes with deadlines; NeoProxy sets none after the connection is
// established.
func fetchNeoPiLlamaCppModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, key string) ([]*ModelInfo, error) {
	var configured string
	if auth != nil {
		configured = strings.TrimSpace(auth.Attributes["base_url"])
	}
	bases := registry.NeoPiLlamaCppBases(configured, os.Getenv)
	return registry.NeoPiLlamaCppListing(ctx, bases, func(ctx context.Context, endpoint string) (int, []byte, error) {
		header := http.Header{}
		if key != "" {
			header.Set("Authorization", "Bearer "+key)
		}
		return neoPiDiscoveryDo(ctx, cfg, auth, registry.NeoPiLlamaCppProvider, http.MethodGet, endpoint, header, nil, "Authorization")
	})
}
