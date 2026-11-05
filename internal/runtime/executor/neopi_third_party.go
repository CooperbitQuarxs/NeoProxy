package executor

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// applyThirdPartyIdentity applies NeoPi's client auth choice and conversation
// header: endpoints that validate X-Api-Key never see an Authorization header,
// a gateway that takes its own auth header sees neither, and the conversation
// id rides next to the protocol's own session header.
func applyThirdPartyIdentity(headers http.Header, provider *neoPiThirdPartyProvider, bearer, session string) {
	if provider == nil {
		return
	}
	switch {
	case provider.gatewayAuthHeader != "":
		headers.Del("Authorization")
		headers.Del("X-Api-Key")
		headers.Set(provider.gatewayAuthHeader, "Bearer "+bearer)
	case provider.apiKeyHeader:
		headers.Del("Authorization")
		if bearer != "" && bearer != "N/A" {
			headers.Set("X-Api-Key", bearer)
		}
	}
	if provider.sessionHeader != "" && session != "" {
		headers.Set(provider.sessionHeader, session)
	}
}

// neoPiAccountBearer is a credential hook for an endpoint whose bearer is
// the account's own key or token, on the row's own URL.
func neoPiAccountBearer(read func(*cliproxyauth.Auth) string) func(context.Context, *cliproxyauth.Auth) (string, func(string) string, error) {
	return func(_ context.Context, auth *cliproxyauth.Auth) (string, func(string) string, error) {
		bearer := strings.TrimSpace(read(auth))
		if bearer == "" {
			return "", nil, statusErr{code: http.StatusUnauthorized, msg: "account has no credential"}
		}
		return bearer, func(url string) string { return url }, nil
	}
}

// prepareThirdParty shapes an Anthropic Messages request for a model NeoPi
// serves over Anthropic Messages on another provider's endpoint, with that
// provider's bearer and identity headers.
func (e *ClaudeExecutor) prepareThirdParty(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*neoPiPrepared, error) {
	provider := e.thirdParty
	bearer, place, err := provider.credential(ctx, auth)
	if err != nil {
		return nil, err
	}
	model := thinking.ParseSuffix(req.Model).ModelName
	row := provider.catalog(auth, req, model)
	if row == nil || row.API != "anthropic-messages" {
		return nil, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("%s: model %s is not served over Anthropic Messages", e.Identifier(), model)}
	}
	original := req.Payload
	if len(opts.OriginalRequest) > 0 {
		original = opts.OriginalRequest
	}
	originalTranslated, translated, err := helps.TranslateRequestPairReturningError(ctx, opts.Headers, e.cfg, opts.SourceFormat, sdktranslator.FormatClaude, model, original, req.Payload, true, helps.APIKeyModelIsCompat(req))
	if err != nil {
		return nil, err
	}
	if translated, err = helps.ApplyRequestThinking(translated, req, opts, opts.SourceFormat.String(), "claude", e.Identifier()); err != nil {
		return nil, err
	}
	// NeoPi omits the thinking display only for hideThinkingSummary, a
	// summary opt-out under requested thinking. A thinking-off request that a
	// model cannot honor clamps to its floor with summaries; the pipeline
	// marks that clamp omitted, which NeoPi does not.
	if gjson.GetBytes(originalTranslated, "thinking.type").String() == "disabled" && gjson.GetBytes(translated, "thinking.display").String() == "omitted" {
		translated, _ = sjson.DeleteBytes(translated, "thinking.display")
	}
	session := neoPiSession(opts, translated)
	if session == "" {
		// NeoPi's streamSimple always carries an inference session id.
		session = helps.ProviderSessionUUID(e.Identifier(), req.Metadata, opts.Metadata)
	}
	identity := neoPiWireIdentity{SessionID: session, APIKey: true}
	// normalizeAnthropicBaseUrl: an endpoint named with its version (an
	// account's .../v1) drops it before /v1/messages is appended.
	base := strings.TrimSuffix(strings.TrimRight(strings.TrimSpace(row.BaseURL), "/"), "/v1")
	profile := neoPiThirdPartyProfile(row)
	if provider.headers != nil {
		profile.thirdParty.headers = provider.headers()
	}
	var factoryHeaders http.Header
	if provider.factory != nil {
		prefixed, factory, headers, errFactory := provider.factory(translated)
		if errFactory != nil {
			return nil, errFactory
		}
		translated = prefixed
		profile.thirdParty.factory = factory
		factoryHeaders = headers
		// streamMessagesWire forwards no session id to the transport.
		identity.SessionID = ""
	}
	webSearch, webSearchSet := "", false
	if provider.gatewayWebSearch != nil {
		// NeoPi decides over model headers merged with the caller's, the
		// caller's winning: here the account's configured headers, then the
		// request's.
		merged := make(http.Header)
		if auth != nil {
			util.ApplyCustomHeadersFromAttrs((&http.Request{Header: merged}).WithContext(ctx), auth.Attributes, opts.Headers)
		}
		for name, values := range opts.Headers {
			merged[name] = values
		}
		var gateway bool
		webSearch, webSearchSet, gateway = provider.gatewayWebSearch(merged)
		profile.thirdParty.gatewayWebSearch = gateway
	}
	// resolveWireModelId: a row's wire id (Cloudflare's stripped namespace)
	// replaces the id the client named.
	wireModel := model
	if row.RequestModelID != "" {
		wireModel = row.RequestModelID
	}
	body, headers, err := projectNeoPiAnthropic(translated, wireModel, base, "", identity, profile)
	if err != nil {
		return nil, err
	}
	finalize := helps.NewPayloadFinalizer(e.cfg, e.Identifier(), model, "claude", "", originalTranslated, req, opts)
	body = finalize(body)
	neoPiApplyHeaders(headers, auth, opts.Headers, &anthropicnpi.Credential{APIKey: bearer}, base, ctx)
	applyThirdPartyIdentity(headers, provider, bearer, session)
	if factoryHeaders != nil {
		// NeoPi spreads the caller's headers over the identity set: the
		// account's configured headers win where they name the same header.
		configured := make(http.Header)
		if auth != nil {
			util.ApplyCustomHeadersFromAttrs((&http.Request{Header: configured}).WithContext(ctx), auth.Attributes, opts.Headers)
		}
		for name, values := range factoryHeaders {
			if value := configured.Get(name); value != "" {
				headers.Set(name, value)
				continue
			}
			headers[name] = values
		}
	}
	if provider.enforceBearer {
		headers.Set("Authorization", "Bearer "+bearer)
	}
	if webSearchSet {
		headers.Set("X-Umans-Websearch-Provider", webSearch)
	}
	return &neoPiPrepared{auth: auth, body: body, translated: translated, headers: headers, url: place(base + "/v1/messages"), model: model, identity: identity, base: base, stripToolPrefix: profile.thirdParty.escapeToolNames, finalize: finalize}, nil
}
