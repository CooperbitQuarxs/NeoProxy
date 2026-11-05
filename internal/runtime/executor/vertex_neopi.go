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
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// vertexGlobalHost is Vertex AI's global endpoint host.
const vertexGlobalHost = "aiplatform.googleapis.com"

// VertexExecutor serves NeoPi's google-vertex provider: a Gemini row over
// NeoPi's Vertex Gemini port (pi-ai providers/google-vertex.ts), a Model
// Garden row (OpenAI-compatible endpoint, or Claude over rawPredict) over
// NeoPi's completions and Anthropic Messages ports with the Vertex
// authenticated transport (stream.ts createVertexAuthenticatedFetch).
type VertexExecutor struct {
	*NeoPiGoogleExecutor
	garden *NeoPiCompletionsExecutor
}

// NewVertexExecutor builds the Vertex AI executor. The Gemini port sends the
// shared Google request, NeoPi's default safety settings, the priority tier
// as a header, and the endpoint and credential its environment resolves.
func NewVertexExecutor(cfg *config.Config) *VertexExecutor {
	return &VertexExecutor{
		NeoPiGoogleExecutor: NewNeoPiGoogleExecutor("vertex", cfg, NeoPiGoogleHooks{
			Row: vertexNeoPiRow,
			Endpoint: func(ctx context.Context, auth *cliproxyauth.Auth, row *registry.NeoPiModel, acquire bool) (NeoPiGoogleEndpoint, error) {
				return vertexNeoPiEndpoint(ctx, cfg, auth, row, acquire)
			},
		}),
		garden: NewNeoPiCompletionsExecutor("vertex", cfg, NeoPiCompletionsProfile{
			Provider: vertexNeoPiProvider,
			// The same suffix-free id modelGarden dispatched on.
			Row: func(_, model string) *registry.NeoPiModel {
				return registry.VertexNeoPiModelFor(thinking.ParseSuffix(model).ModelName)
			},
		}),
	}
}

// modelGarden reports whether NeoPi serves the request's model over Model
// Garden rather than Gemini's generateContent.
func (e *VertexExecutor) modelGarden(req cliproxyexecutor.Request) bool {
	row := vertexNeoPiRow(req, thinking.ParseSuffix(req.Model).ModelName)
	return row != nil && (row.API == "openai-completions" || row.API == "anthropic-messages")
}

// RequestToFormat reports the format the row's port takes.
func (e *VertexExecutor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if e.modelGarden(req) {
		return e.garden.RequestToFormat(req, opts)
	}
	return e.NeoPiGoogleExecutor.RequestToFormat(req, opts)
}

// Execute runs the request on the row's port.
func (e *VertexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.modelGarden(req) {
		return e.garden.Execute(ctx, auth, req, opts)
	}
	return e.NeoPiGoogleExecutor.Execute(ctx, auth, req, opts)
}

// ExecuteStream streams the request on the row's port.
func (e *VertexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.modelGarden(req) {
		return e.garden.ExecuteStream(ctx, auth, req, opts)
	}
	return e.NeoPiGoogleExecutor.ExecuteStream(ctx, auth, req, opts)
}

// CountTokens counts on the row's port: Gemini's countTokens, or the local
// estimate the completions port makes.
func (e *VertexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.modelGarden(req) {
		return e.garden.CountTokens(ctx, auth, req, opts)
	}
	return e.NeoPiGoogleExecutor.CountTokens(ctx, auth, req, opts)
}

// vertexAuthenticatedURL is isGoogleVertexAuthenticatedModel over a request
// URL: a Model Garden endpoint (isVertexExpressOpenAIUrl) or a Claude
// rawPredict one (isVertexRawPredictUrl), which always carry the ADC bearer.
func vertexAuthenticatedURL(target string) bool {
	return strings.Contains(target, "/endpoints/openapi") || neoPiVertexRawPredictURL(target)
}

// PrepareRequest injects the credential the target's transport carries: the
// ADC bearer for a Model Garden or rawPredict URL, else the Gemini
// credential. The URL is already placed, so only the bearer is acquired.
func (e *VertexExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil || req.URL == nil || !vertexAuthenticatedURL(req.URL.String()) {
		return e.NeoPiGoogleExecutor.PrepareRequest(req, auth)
	}
	env, err := vertexAccountEnvironment(auth)
	if err != nil {
		return err
	}
	token, err := vertexBearer(req.Context(), e.cfg, auth, env.credential)
	if err != nil {
		return err
	}
	req.Header.Del("x-goog-api-key")
	if auth != nil {
		util.ApplyCustomHeadersFromAttrs(req, auth.Attributes)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// HttpRequest sends an arbitrary request with the credential its target's
// transport carries.
func (e *VertexExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || !vertexAuthenticatedURL(req.URL.String()) {
		return e.NeoPiGoogleExecutor.HttpRequest(ctx, auth, req)
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
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

// vertexNeoPiProvider is NeoPi's provider id for Vertex AI, whose Model
// Garden rows (OpenAI-compatible and Claude rawPredict) the completions port
// serves.
const vertexNeoPiProvider = "google-vertex"

// vertexAuthenticatedRequest is createVertexAuthenticatedFetch for a Model
// Garden row: the request carries an ADC access token whatever key the
// account holds, and its templated endpoint is placed on the account's
// project and location (resolveVertexRequest). NeoPi leaves the template in
// place without a project and location, a host no request can reach, so that
// configuration error is raised here instead.
func vertexAuthenticatedRequest(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (string, func(string) string, error) {
	env, err := vertexAccountEnvironment(auth)
	if err != nil {
		return "", nil, err
	}
	if env.project == "" {
		return "", nil, statusErr{code: http.StatusBadRequest, msg: "Vertex AI requires a project ID. Set GOOGLE_CLOUD_PROJECT/GCP_PROJECT/GCLOUD_PROJECT or pass project in options."}
	}
	if env.location == "" {
		return "", nil, statusErr{code: http.StatusBadRequest, msg: "Vertex AI requires a location. Set GOOGLE_VERTEX_LOCATION/GOOGLE_CLOUD_LOCATION/VERTEX_LOCATION or pass location in options."}
	}
	token, err := vertexBearer(ctx, cfg, auth, env.credential)
	if err != nil {
		return "", nil, err
	}
	return token, func(target string) string { return vertexRequestURL(target, env.project, env.location) }, nil
}

// vertexRequestURL is resolveVertexRequest's rewriteUrl: a templated
// endpoint moves to the location's host with the project and location
// path-escaped, and a rawPredict URL drops the /v1/messages the Anthropic
// client appended.
func vertexRequestURL(target, project, location string) string {
	if strings.Contains(target, "{project}") || strings.Contains(target, "{location}") ||
		strings.Contains(target, "%7Bproject%7D") || strings.Contains(target, "%7Blocation%7D") {
		host := "https://" + vertexEndpointHost(location)
		target = strings.Replace(target, "https://{location}-aiplatform.googleapis.com", host, 1)
		target = strings.Replace(target, "https://%7Blocation%7D-aiplatform.googleapis.com", host, 1)
		project, location = vertexURIComponent(project), vertexURIComponent(location)
		target = strings.NewReplacer("{project}", project, "%7Bproject%7D", project).Replace(target)
		target = strings.NewReplacer("{location}", location, "%7Blocation%7D", location).Replace(target)
	}
	return strings.Replace(target, ":streamRawPredict/v1/messages", ":streamRawPredict", 1)
}

// vertexURIComponent is encodeURIComponent: everything but unreserved
// characters and !'()* is percent-encoded.
func vertexURIComponent(value string) string {
	var out strings.Builder
	for _, b := range []byte(value) {
		if 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || '0' <= b && b <= '9' || strings.IndexByte("-_.!~*'()", b) >= 0 {
			out.WriteByte(b)
			continue
		}
		fmt.Fprintf(&out, "%%%02X", b)
	}
	return out.String()
}
