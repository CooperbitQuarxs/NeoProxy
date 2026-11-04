package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestApplyNousPortalTags(t *testing.T) {
	const nous = "https://inference-api.nousresearch.com/v1"
	for _, tc := range []struct {
		name, base, payload, want string
	}{
		{"adds both tags", nous, `{"model":"m"}`, `["product=neoproxy","user=neoproxy"]`},
		{"keeps client tags", nous, `{"tags":["product=app","conversation=c1"]}`, `["product=app","conversation=c1","user=neoproxy"]`},
		{"client user tag wins", nous, `{"tags":["user=alice"]}`, `["user=alice","product=neoproxy"]`},
		{"replaces a non-array", nous, `{"tags":"x"}`, `["product=neoproxy","user=neoproxy"]`},
		{"other hosts untouched", "https://openrouter.ai/api/v1", `{"model":"m"}`, ``},
	} {
		got := gjson.GetBytes(applyNousPortalTags([]byte(tc.payload), tc.base), "tags").Raw
		if got != tc.want {
			t.Errorf("%s: tags = %s, want %s", tc.name, got, tc.want)
		}
	}
}
