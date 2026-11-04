package executor

import (
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// nousPortalHost is Nous Research's inference API. It rejects chat completions
// whose top-level "tags" array lacks a "user=" attribution tag ("missing tags",
// then "missing user tag"); its own Hermes client sends "product=" tags there.
const nousPortalHost = "inference-api.nousresearch.com"

// applyNousPortalTags adds the attribution tags Nous Portal requires, keeping
// any tags the client already sent. Other hosts are left untouched.
func applyNousPortalTags(payload []byte, baseURL string) []byte {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || !strings.EqualFold(parsed.Hostname(), nousPortalHost) {
		return payload
	}
	tags := gjson.GetBytes(payload, "tags")
	if !tags.IsArray() {
		if updated, errSet := sjson.SetBytes(payload, "tags", []string{"product=neoproxy", "user=neoproxy"}); errSet == nil {
			return updated
		}
		return payload
	}
	hasProduct, hasUser := false, false
	for _, tag := range tags.Array() {
		hasProduct = hasProduct || strings.HasPrefix(tag.String(), "product=")
		hasUser = hasUser || strings.HasPrefix(tag.String(), "user=")
	}
	for _, missing := range []struct {
		present bool
		tag     string
	}{{hasProduct, "product=neoproxy"}, {hasUser, "user=neoproxy"}} {
		if missing.present {
			continue
		}
		if updated, errSet := sjson.SetBytes(payload, "tags.-1", missing.tag); errSet == nil {
			payload = updated
		}
	}
	return payload
}
