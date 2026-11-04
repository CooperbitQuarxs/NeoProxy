package neopool_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

func TestAccessTestPinsOneCredentialAndShapesResult(t *testing.T) {
	f := newAccessFixture(t)
	item := itemOf(t, f.json("owner-a", "POST", "/v8/neo/access/api-key", `{"provider":"openrouter","apiKey":"`+routerKey+`"}`, 201))
	var engineID string
	for _, a := range f.manager.List() {
		if neopool.AccountID(a.ID) == item.ID {
			engineID = a.ID
		}
	}
	registry.GetGlobalRegistry().RegisterClient(engineID, "openrouter", []*registry.ModelInfo{{ID: "big-model"}, {ID: "tiny-mini"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(engineID) })

	var gotAuth, gotProvider string
	var gotBody []byte
	reply := `{"choices":[{"message":{"content":"` + strings.Repeat("O", 300) + `"}}]}`
	var failure error
	status := 0
	f.http.SetAccessProbe(func(_ context.Context, authID, provider string, body []byte) ([]byte, int, error) {
		gotAuth, gotProvider, gotBody = authID, provider, body
		return []byte(reply), status, failure
	})
	path := "/v8/neo/access/" + item.ID + "/test"

	f.json("owner-b", "POST", path, "", 404)
	f.json("member-a", "POST", path, "", 404)
	f.json("owner-a", "POST", path, `{"prompt":`, 400)

	var res neopool.AccessProbeResult
	if err := json.Unmarshal(f.json("owner-a", "POST", path, "", 200), &res); err != nil {
		t.Fatal(err)
	}
	if gotAuth != engineID || gotProvider != "openai-compatible-openrouter" {
		t.Fatalf("probe not pinned to the account: %q %q", gotAuth, gotProvider)
	}
	if gjson.GetBytes(gotBody, "model").String() != "tiny-mini" || gjson.GetBytes(gotBody, "max_tokens").Int() != 8 || gjson.GetBytes(gotBody, "stream").Bool() ||
		gjson.GetBytes(gotBody, "messages.0.content").String() != "Reply with the single word OK." {
		t.Fatalf("request body: %s", gotBody)
	}
	if !res.OK || res.Model != "openai-compatible-openrouter/tiny-mini" || res.Status != 200 || len(res.Reply) != 200 || res.Error != "" || res.CheckedAt == 0 {
		t.Fatalf("success result: %+v", res)
	}
	listed := listOf(t, f.json("owner-a", "GET", "/v8/neo/access", "", 200))
	if p := listed.Items[0].LastProbe; p == nil || !p.OK || p.CheckedAt != res.CheckedAt {
		t.Fatalf("lastProbe not surfaced: %+v", listed.Items[0].LastProbe)
	}

	status, failure = 401, errors.New(`{"error":"bad key `+routerKey+`"}`)
	raw := f.json("admin", "POST", path, `{"model":"big-model","prompt":"hi"}`, 200)
	if strings.Contains(string(raw), routerKey) {
		t.Fatalf("probe leaked upstream text: %s", raw)
	}
	res = neopool.AccessProbeResult{}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.OK || res.Status != 401 || res.Model != "big-model" || !strings.Contains(res.Error, "rejected this credential") || res.Reply != "" ||
		gjson.GetBytes(gotBody, "model").String() != "big-model" || gjson.GetBytes(gotBody, "messages.0.content").String() != "hi" {
		t.Fatalf("failure result: %+v body %s", res, gotBody)
	}
	listed = listOf(t, f.json("owner-a", "GET", "/v8/neo/access", "", 200))
	if p := listed.Items[0].LastProbe; p == nil || p.OK || p.Error == "" {
		t.Fatalf("lastProbe after failure: %+v", listed.Items[0].LastProbe)
	}
}

// A contributor demoted to member still owns the access but may not test it:
// a test lifts the engine hold and spends a real request.
func TestAccessTestRequiresOwnerRole(t *testing.T) {
	f := newAccessFixture(t)
	item := itemOf(t, f.json("owner-a", "POST", "/v8/neo/access/api-key", `{"provider":"openrouter","apiKey":"`+routerKey+`"}`, 201))
	probed := false
	f.http.SetAccessProbe(func(context.Context, string, string, []byte) ([]byte, int, error) {
		probed = true
		return []byte(`{"choices":[{"message":{"content":"OK"}}]}`), 200, nil
	})
	if _, err := f.identity.DB().Exec(`UPDATE neo_users SET role='member' WHERE id='owner-a'`); err != nil {
		t.Fatal(err)
	}
	path := "/v8/neo/access/" + item.ID + "/test"
	f.json("owner-a", "POST", path, `{"model":"some-model"}`, 403)
	if probed {
		t.Fatal("member reached the probe")
	}
	f.json("admin", "POST", path, `{"model":"some-model"}`, 200)
}
