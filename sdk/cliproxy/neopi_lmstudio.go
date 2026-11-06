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

// fetchNeoPiLmStudioModels is NeoPi's runtime discovery of an LM Studio
// server (coding-agent discoverOpenAIModelsList, discovery type lm-studio):
// GET <base>/models with no headers of its own beside LM Studio's native
// GET /api/v0/models and /api/v1/models (Accept: application/json), all
// three with the key as a bearer when the account holds a real one (key
// arrives with the keyless marker already stripped), at the account's
// configured base, else LM_STUDIO_BASE_URL, else 127.0.0.1:1234. NeoPi bounds
// the probes with deadlines; NeoProxy sets none after the connection is
// established.
func fetchNeoPiLmStudioModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, key string) ([]*ModelInfo, error) {
	// A failed listing returns without awaiting the native probes; the
	// deferred cancel abandons them.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var configured string
	if auth != nil {
		configured = strings.TrimSpace(auth.Attributes["base_url"])
	}
	base := registry.NeoPiLmStudioBaseURL(configured, os.Getenv)
	return registry.NeoPiLmStudioRuntimeListing(base, func(endpoint string, native bool) (int, []byte, error) {
		header := http.Header{}
		if native {
			header.Set("Accept", "application/json")
		}
		if key != "" {
			header.Set("Authorization", "Bearer "+key)
		}
		return neoPiDiscoveryDo(ctx, cfg, auth, "lm-studio", http.MethodGet, endpoint, header, nil, "Authorization")
	})
}
