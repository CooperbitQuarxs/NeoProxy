package neopool

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	metaauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/meta"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// errNoMetaUsage is a key response that reports no limits.
var errNoMetaUsage = errors.New("muse-code usage: the key response reports no limits")

// metaDeviceToken is the Meta account token a Muse sign-in keeps beside its
// minted key; the usage endpoint reads the subscription with it.
func metaDeviceToken(a *coreauth.Auth) string {
	if a == nil {
		return ""
	}
	if token, _ := a.Metadata["dca_token"].(string); strings.TrimSpace(token) != "" {
		return strings.TrimSpace(token)
	}
	return strings.TrimSpace(a.Attributes["dca_token"])
}

// metaUsageWindows adapts NeoPi's Muse Code report: its rolling window becomes
// the 5h or day window by length, the weekly cap the week window.
func metaUsageWindows(usage *metaauth.Usage) []Window {
	out := []Window{}
	if usage == nil {
		return out
	}
	for _, limit := range usage.Limits {
		kind := "week"
		if limit.ID != "1w" {
			switch {
			case limit.DurationMs > 36*int64(time.Hour/time.Millisecond):
				kind = "week"
			case limit.DurationMs > 6*int64(time.Hour/time.Millisecond):
				kind = "day"
			default:
				kind = "5h"
			}
		}
		resets, starts := time.Time{}, time.Time{}
		if limit.ResetsAtMs > 0 {
			resets = time.UnixMilli(limit.ResetsAtMs)
			if limit.DurationMs > 0 {
				starts = resets.Add(-time.Duration(limit.DurationMs) * time.Millisecond)
			}
		}
		out = append(out, percentWindow(kind, limit.UsedPercent, starts, resets))
	}
	return out
}

// fetchMetaUsage reads a Muse sign-in's report without publishing it. The caller
// supplies the accepted previous generation and commits this report with its
// windows only after the refresh generation is accepted. A response with nothing
// to report (NeoPi's null) leaves the cached reading in place by failing the poll.
func fetchMetaUsage(ctx context.Context, client *http.Client, a *coreauth.Auth, previous *metaauth.Usage, now time.Time) (*metaauth.Usage, error) {
	token := metaDeviceToken(a)
	if token == "" {
		return nil, errors.New("sign-in has no access token")
	}
	email, _ := a.Metadata["email"].(string)
	accountID, _ := a.Metadata["account_id"].(string)
	usage, err := metaauth.NewMetaAuthWithClient(client).FetchUsage(ctx, token, previous, strings.TrimSpace(email), strings.TrimSpace(accountID), now)
	if err != nil {
		return nil, err
	}
	if usage == nil {
		return nil, errNoMetaUsage
	}
	return usage, nil
}

// NeoPi's auth/usage.ts purges the previous report and last-good reading when a
// provider returns 401 or 403, including Muse's inactive-subscription response.
func isDefinitiveMetaUsageError(err error) bool {
	var usageErr *metaauth.UsageError
	return errors.As(err, &usageErr) && (usageErr.Status == http.StatusUnauthorized || usageErr.Status == http.StatusForbidden)
}
