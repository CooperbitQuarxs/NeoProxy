package cliproxy

import (
	"context"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/nous"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// nousModelsForAuth returns the models a Nous Portal sign-in can use. The
// dashboard sign-in stores the portal's list in the auth file; a sign-in that
// arrived without one (a NeoPi import) lists them once with its access token.
func nousModelsForAuth(ctx context.Context, cfg *config.Config, a *coreauth.Auth) []*ModelInfo {
	var ids []string
	if raw, ok := a.Metadata["models"].([]any); ok {
		for _, v := range raw {
			if id, _ := v.(string); strings.TrimSpace(id) != "" {
				ids = append(ids, strings.TrimSpace(id))
			}
		}
	}
	if len(ids) == 0 {
		token, _ := a.Metadata["access_token"].(string)
		if strings.TrimSpace(token) == "" {
			return nil
		}
		listCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		listed, err := nous.NewClient(cfg, a.ProxyURL).ListModels(listCtx, token)
		if err != nil {
			log.Warnf("nous portal %s: listing models failed: %v", a.ID, err)
			return nil
		}
		ids = listed
	}
	entry := &config.OpenAICompatibility{Name: nous.Provider, Models: make([]config.OpenAICompatibilityModel, 0, len(ids))}
	for _, id := range ids {
		entry.Models = append(entry.Models, config.OpenAICompatibilityModel{Name: id})
	}
	return buildOpenAICompatibilityConfigModels(entry)
}
