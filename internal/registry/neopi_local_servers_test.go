package registry

import (
	"strings"
	"testing"
)

// A decision model whose loaded instance carries a thinking suffix forms a
// thinking pair; NeoPi's collapse keeps the pair's Decisions transport, so
// neither id reaches the chat roster.
func TestLmStudioDecisionThinkingPairStaysOutOfChat(t *testing.T) {
	listing := `{"object":"list","data":[{"id":"zz-local","object":"model"},{"id":"zz-local-thinking","object":"model"},{"id":"zz-chat","object":"model"}]}`
	v1 := `{"models":[{"key":"zz-local","type":"decision","loaded_instances":[{"id":"zz-local-thinking"}]}]}`
	models, err := NeoPiLmStudioRuntimeListing("http://127.0.0.1:1234/v1", func(endpoint string, _ bool) (int, []byte, error) {
		switch {
		case strings.HasSuffix(endpoint, "/v1/models") && !strings.Contains(endpoint, "/api/"):
			return 200, []byte(listing), nil
		case strings.HasSuffix(endpoint, "/api/v1/models"):
			return 200, []byte(v1), nil
		}
		return 404, nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	if len(ids) != 1 || ids[0] != "zz-chat" {
		t.Fatalf("chat roster = %v, want only zz-chat", ids)
	}
}
