package neopool

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/alibabatokenplan"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/neopikeys"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/xiaomi"
)

// Providers NeoProxy serves through NeoPi's OpenAI Chat Completions provider
// take their keys the way NeoPi's own sign-in does: the pasted key is trimmed
// (DeepSeek also drops a "Bearer" prefix), probed once with NeoPi's request,
// and kept as typed. The account then serves NeoPi's catalog and discovery, so
// no model list is kept with the key.

// normalizeNeoPiKey applies the provider's login normalisation to a pasted key.
func normalizeNeoPiKey(info ProviderInfo, key string) (string, error) {
	login, ok := neopikeys.LoginFor(info.ID)
	if !ok || key == "" {
		return key, nil
	}
	normalized, err := login.NormalizeKey(key)
	if err != nil {
		return "", accessErr(http.StatusBadRequest, "%s", err.Error())
	}
	return normalized, nil
}

// validateNeoPiKey runs NeoPi's key check for providers that have one and
// reports whether it handled the provider. It returns the base URL that
// accepted the key.
func validateNeoPiKey(ctx context.Context, info ProviderInfo, base, preferred, key string) (string, bool, error) {
	if info.ID == alibabatokenplan.Provider {
		accepted, err := validateTokenPlanKey(ctx, info, preferred, key)
		return accepted, true, err
	}
	if info.ID == xiaomi.Provider {
		// loginXiaomi: the standard host, or the Token Plan clusters for a
		// tp-/ttp- key, which discovery and inference then reach on their own.
		return base, true, neoPiKeyFailure(info, xiaomi.ValidateKey(ctx, accessHTTPClient, key))
	}
	if info.ID == "alibaba-coding-plan" {
		// loginAlibabaCodingPlan: the chosen endpoint (International unless
		// another is named) checks the key, and the account keeps it.
		accepted := neopikeys.AlibabaCodingPlanBaseURL(preferred)
		return accepted, true, neoPiKeyFailure(info, neopikeys.AlibabaCodingPlanLogin(accepted).Validate(ctx, accessHTTPClient, key))
	}
	login, ok := neopikeys.LoginFor(info.ID)
	if !ok {
		return "", false, nil
	}
	return base, true, neoPiKeyFailure(info, login.Validate(ctx, accessHTTPClient, key))
}

// validateTokenPlanKey validates only the region selected by the caller, as
// NeoPi does. An empty selection means the default international endpoint.
func validateTokenPlanKey(ctx context.Context, info ProviderInfo, preferred, key string) (string, error) {
	base := alibabatokenplan.NormalizeBaseURL(preferred)
	if base == "" {
		regions := catalogRegions(info.ID)
		if len(regions) == 0 {
			return "", accessErr(http.StatusBadRequest, "%s has no default endpoint.", info.Name)
		}
		base = regions[0]
	}
	return base, neoPiKeyFailure(info, alibabatokenplan.ValidateKey(ctx, accessHTTPClient, base, key))
}

// neoPiKeyFailure turns a failed probe into the dashboard's error. Upstream
// bodies are never surfaced: they can echo partial keys or account details.
func neoPiKeyFailure(info ProviderInfo, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, neopikeys.ErrCoreWeaveProject) {
		// NeoPi reads the project from the environment, so the server's must name it.
		return accessErr(http.StatusServiceUnavailable, "%s needs a W&B project: set COREWEAVE_PROJECT=<team>/<project> in the NeoProxy server's environment and restart it.", info.Name)
	}
	var upstream *neopikeys.HTTPError
	if !errors.As(err, &upstream) {
		return accessErr(http.StatusBadGateway, "Could not reach %s to check the key. Try again in a moment.", info.Name)
	}
	switch {
	case upstream.AuthFailed():
		return &AccessError{Status: http.StatusBadRequest, keyRejected: true, Message: "The provider rejected this key (HTTP " + strconv.Itoa(upstream.Status) + "). Check that the whole key was pasted and that it is still active."}
	case upstream.Status == http.StatusTooManyRequests:
		return accessErr(http.StatusServiceUnavailable, "%s is rate-limiting requests right now (HTTP 429); the key itself was not rejected. Wait a minute and try again.", info.Name)
	default:
		return accessErr(http.StatusBadGateway, "%s answered HTTP %d when checking the key. Try again in a moment.", info.Name, upstream.Status)
	}
}

// maxConsoleCookie bounds a pasted console Cookie header.
const maxConsoleCookie = 16384

// neoPiConsoleCookie is the Token Plan sign-in's optional quota-reporting
// cookie: NeoPi accepts the whole Cookie request header of the console's usage
// request, with or without its "Cookie:" prefix. Other providers take none.
func neoPiConsoleCookie(info ProviderInfo, raw, accepted string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil
	}
	if info.ID != alibabatokenplan.Provider {
		return "", accessErr(http.StatusBadRequest, "%s does not use a console cookie.", info.Name)
	}
	cookie, err := alibabatokenplan.NormalizeCookie(raw, accepted)
	if err != nil {
		return "", accessErr(http.StatusBadRequest, "%s", err.Error())
	}
	if len(cookie) > maxConsoleCookie || strings.ContainsAny(cookie, "\r\n\x00") {
		return "", accessErr(http.StatusBadRequest, "The cookie is too long or spans several lines. Paste the Cookie request header as one line.")
	}
	return cookie, nil
}
