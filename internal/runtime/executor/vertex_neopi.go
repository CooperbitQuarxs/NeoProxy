package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	vertexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/vertex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// vertexGlobalHost is Vertex AI's global endpoint host.
const vertexGlobalHost = "aiplatform.googleapis.com"

// NewVertexExecutor serves Vertex AI the way NeoPi's google-vertex provider
// does (pi-ai providers/google-vertex.ts, streamGoogleVertex): the shared
// Google request, NeoPi's default safety settings, the priority tier as a
// header, and the endpoint and credential its environment resolves.
func NewVertexExecutor(cfg *config.Config) *NeoPiGoogleExecutor {
	return NewNeoPiGoogleExecutor("vertex", cfg, NeoPiGoogleHooks{
		Row: vertexNeoPiRow,
		Endpoint: func(ctx context.Context, auth *cliproxyauth.Auth, row *registry.NeoPiModel, acquire bool) (NeoPiGoogleEndpoint, error) {
			return vertexNeoPiEndpoint(ctx, cfg, auth, row, acquire)
		},
	})
}

// vertexNeoPiRow is the NeoPi record the account's registration produced for
// model, else NeoPi's bundled google-vertex row, else the row NeoPi's rules
// build for the id.
func vertexNeoPiRow(req cliproxyexecutor.Request, model string) *registry.NeoPiModel {
	if info, ok := cliproxyauth.ResolvedModelInfo(req); ok && info != nil && info.NeoPi != nil {
		return info.NeoPi
	}
	return registry.VertexNeoPiModelFor(model)
}

// vertexEnvironment is the Vertex environment one account resolves against:
// its own settings, each standing in for the process variable NeoPi reads,
// else that variable.
type vertexEnvironment struct {
	apiKey string
	// credential is the account's ADC JSON (its service account); empty
	// resolves NeoPi's process sources.
	credential []byte
	project    string
	location   string
}

// vertexAccountEnvironment reads the account's API key, credential, project
// and location. An account's own key or credential is its whole credential:
// the process key (GOOGLE_CLOUD_API_KEY) and ADC apply only to an account
// with neither.
func vertexAccountEnvironment(auth *cliproxyauth.Auth) (vertexEnvironment, error) {
	var env vertexEnvironment
	if auth != nil {
		if auth.Attributes != nil {
			env.apiKey = vertexRealKey(auth.Attributes["api_key"])
			env.location = strings.TrimSpace(auth.Attributes["location"])
			env.project = strings.TrimSpace(auth.Attributes["project_id"])
		}
		if auth.Metadata != nil {
			if account, ok := auth.Metadata["service_account"].(map[string]any); ok && len(account) > 0 {
				raw, err := json.Marshal(account)
				if err != nil {
					return env, statusErr{code: http.StatusBadRequest, msg: "vertex: service_account is not valid JSON"}
				}
				env.credential = raw
			}
			for _, key := range []string{"project_id", "project"} {
				if value, ok := auth.Metadata[key].(string); ok && env.project == "" {
					env.project = strings.TrimSpace(value)
				}
			}
			if value, ok := auth.Metadata["location"].(string); ok && env.location == "" {
				env.location = strings.TrimSpace(value)
			}
		}
	}
	if env.apiKey == "" && len(env.credential) == 0 {
		env.apiKey = os.Getenv("GOOGLE_CLOUD_API_KEY")
	}
	if env.project == "" {
		env.project = vertexFirstEnv("GOOGLE_CLOUD_PROJECT", "GCP_PROJECT", "GCLOUD_PROJECT")
	}
	if env.location == "" {
		env.location = vertexFirstEnv("GOOGLE_VERTEX_LOCATION", "GOOGLE_CLOUD_LOCATION", "VERTEX_LOCATION")
	}
	return env, nil
}

// vertexRealKey is resolveApiKey's sentinel filter: "<…>" placeholders and
// "N/A" are not keys.
func vertexRealKey(value string) string {
	if value == "" || strings.HasPrefix(value, "<") || value == "N/A" {
		return ""
	}
	return value
}

func vertexFirstEnv(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

// vertexEndpointHost is resolveVertexEndpointHost.
func vertexEndpointHost(location string) string {
	switch location {
	case "global":
		return vertexGlobalHost
	case "eu", "us":
		return "aiplatform." + location + ".rep.googleapis.com"
	}
	return location + "-aiplatform.googleapis.com"
}

// vertexNeoPiEndpoint is streamGoogleVertex's endpoint. With an API key the
// publisher path on the location's host (global when none); NeoPi's location
// always comes from the environment, so a regional host falls back to the
// global one on a 404. Without one, the project and location are required and
// the request carries an ADC bearer token on the project path.
func vertexNeoPiEndpoint(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, row *registry.NeoPiModel, acquire bool) (NeoPiGoogleEndpoint, error) {
	env, err := vertexAccountEnvironment(auth)
	if err != nil {
		return NeoPiGoogleEndpoint{}, err
	}
	if env.apiKey != "" {
		endpoint := NeoPiGoogleEndpoint{Credential: http.Header{"X-Goog-Api-Key": {env.apiKey}}}
		if row != nil {
			location := env.location
			if location == "" {
				location = "global"
			}
			host := vertexEndpointHost(location)
			path := "v1/publishers/google/models/" + row.ID + ":streamGenerateContent?alt=sse"
			endpoint.URL = "https://" + host + "/" + path
			if host != vertexGlobalHost {
				endpoint.FallbackURL = "https://" + vertexGlobalHost + "/" + path
			}
		}
		return endpoint, nil
	}
	if env.project == "" {
		return NeoPiGoogleEndpoint{}, statusErr{code: http.StatusBadRequest, msg: "Vertex AI requires a project ID. Set GOOGLE_CLOUD_PROJECT/GCP_PROJECT/GCLOUD_PROJECT or pass project in options."}
	}
	if env.location == "" {
		return NeoPiGoogleEndpoint{}, statusErr{code: http.StatusBadRequest, msg: "Vertex AI requires a location. Set GOOGLE_VERTEX_LOCATION/GOOGLE_CLOUD_LOCATION/VERTEX_LOCATION or pass location in options."}
	}
	endpoint := NeoPiGoogleEndpoint{}
	if row != nil {
		endpoint.URL = fmt.Sprintf("https://%s/v1/projects/%s/locations/%s/publishers/google/models/%s:streamGenerateContent?alt=sse",
			vertexEndpointHost(env.location), env.project, env.location, row.ID)
	}
	if !acquire {
		return endpoint, nil
	}
	token, err := vertexBearer(ctx, cfg, auth, env.credential)
	if err != nil {
		return NeoPiGoogleEndpoint{}, err
	}
	endpoint.Credential = http.Header{"Authorization": {"Bearer " + token}}
	return endpoint, nil
}

// vertexBearer acquires the account's ADC access token. Token exchange stays
// on the credential or global proxy; the execution proxy applies only to the
// model request that follows.
func vertexBearer(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, credential []byte) (string, error) {
	tokenCtx := cliproxyexecutor.WithoutRequestProxyURL(ctx)
	token, err := vertexauth.AccessToken(tokenCtx, helps.NewProxyAwareHTTPClient(tokenCtx, cfg, auth, 0), credential)
	if err != nil {
		var credentialErr *vertexauth.CredentialError
		if errors.As(err, &credentialErr) {
			code := http.StatusUnauthorized
			if credentialErr.Configuration {
				code = http.StatusBadRequest
			}
			return "", statusErr{code: code, msg: credentialErr.Message}
		}
		return "", fmt.Errorf("vertex: access token: %w", err)
	}
	return token, nil
}
