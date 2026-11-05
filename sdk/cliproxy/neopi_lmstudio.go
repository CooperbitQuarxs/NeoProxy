package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// fetchNeoPiLmStudioModels is lmStudioModelManagerOptions' fetchDynamicModels:
// the OpenAI /models listing, with LM Studio's native metadata (GET
// /api/v0/models and /api/v1/models beside the /v1 base) fetched alongside
// it. Native answers that fail add no metadata. NeoPi bounds the native
// fetch with a deadline; NeoProxy sets none after the connection is
// established.
func fetchNeoPiLmStudioModels(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, key, base string) ([]*ModelInfo, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	native := registry.NeoPiLmStudioNativeBaseURL(base)
	var v0, v1 []byte
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); v0 = neoPiLmStudioNative(ctx, cfg, auth, native+"/api/v0/models", key) }()
	go func() { defer wg.Done(); v1 = neoPiLmStudioNative(ctx, cfg, auth, native+"/api/v1/models", key) }()
	endpoint := base + "/models"
	body, err := neoPiDiscoveryGet(ctx, cfg, auth, "lm-studio", endpoint, key)
	if err != nil {
		return nil, err
	}
	wg.Wait()
	models, err := registry.NeoPiLmStudioListing(base, body, v0, v1)
	if err != nil {
		return nil, fmt.Errorf("lm-studio models: %s: %w", endpoint, err)
	}
	return models, nil
}

// neoPiLmStudioNative is fetchLmStudioNativeModelMetadata's fetchJson: the
// body of a 2xx answer, nil otherwise.
func neoPiLmStudioNative(ctx context.Context, cfg *config.Config, auth *coreauth.Auth, endpoint, key string) []byte {
	header := http.Header{}
	header.Set("Accept", "application/json")
	if key != "" {
		header.Set("Authorization", "Bearer "+key)
	}
	body, err := neoPiDiscoveryFetch(ctx, cfg, auth, "lm-studio", endpoint, header)
	if err != nil {
		return nil
	}
	return body
}
