package neopool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
)

func TestNeoPiShareItemsPauseAndDetachSurviveSync(t *testing.T) {
	f := newFixture(t)
	attachSourceExecutor(t, f.pool, f.manager)
	ctx := t.Context()
	admin := neo.WithPrincipal(ctx, neo.Principal{UserID: "admin", Role: neo.RoleAdmin, Session: true})
	gatewayCalls := []string{}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gatewayCalls = append(gatewayCalls, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"openai-codex/gpt-5.5"},{"id":"anthropic/claude-opus"}]}`))
		case "/v1/neoproxy/accounts":
			_, _ = w.Write([]byte(`{"data":[{"id":"c1","provider":"openai-codex","label":"shared codex","models":["openai-codex/gpt-5.5"],"state":"healthy","windows":[]},{"id":"c2","provider":"anthropic","label":"shared claude","models":["anthropic/claude-opus"],"state":"healthy","windows":[]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer gateway.Close()
	source, err := f.pool.SaveSource(admin, "", SourceInput{Label: "Office NeoPi", URL: gateway.URL, Bearer: "gateway-bearer-secret", OwnerID: "owner-a", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	h := NewHTTP(f.pool, nil)
	h.Register(router.Group("/v8/neo", neo.NewHTTP(f.identity).SessionMiddleware()))
	tokens := map[string]string{}
	for _, id := range []string{"admin", "owner-a"} {
		sum := sha256.Sum256([]byte(id))
		token := hex.EncodeToString(sum[:])
		hash := sha256.Sum256([]byte(token))
		if _, err = f.identity.DB().Exec(`INSERT INTO neo_sessions(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,0)`, hex.EncodeToString(hash[:]), id, int64(1<<62)); err != nil {
			t.Fatal(err)
		}
		tokens[id] = token
	}
	call := func(user, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: tokens[user]})
		req.Header.Set("X-Neoproxy-CSRF", "1")
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}
	shared := func(user string) map[string]AccessItem {
		w := call(user, "GET", "/v8/neo/access", "")
		if w.Code != 200 || strings.Contains(w.Body.String(), "gateway-bearer-secret") || strings.Contains(w.Body.String(), "private-test-token") {
			t.Fatalf("list: %d %s", w.Code, w.Body.String())
		}
		var list AccessList
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
			t.Fatal(err)
		}
		out := map[string]AccessItem{}
		for _, item := range list.Items {
			if item.Kind == "neopi" {
				out[item.Label] = item
			}
		}
		return out
	}
	items := shared("owner-a")
	codex, claude := items["shared codex"], items["shared claude"]
	if len(items) != 2 || codex.Provider != "codex" || claude.Provider != "claude" || codex.Refresh != "neopi" || codex.OwnerID != "owner-a" || codex.NeoPiSource == nil || codex.NeoPiSource.Name != "Office NeoPi" || len(codex.Models) != 1 {
		t.Fatalf("neopi items: %+v", items)
	}
	if strings.Contains(strings.Join(codex.Actions, ","), "remove") {
		t.Fatalf("owners must not remove gateway accounts the admin manages: %v", codex.Actions)
	}
	if w := call("owner-a", "DELETE", "/v8/neo/access/"+codex.ID, ""); w.Code != 403 {
		t.Fatalf("owner detached a gateway account: %d %s", w.Code, w.Body.String())
	}

	if w := call("admin", "PATCH", "/v8/neo/access/"+claude.ID, `{"disabled":true}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"health":"disabled"`) {
		t.Fatalf("pause gateway account: %d %s", w.Code, w.Body.String())
	}
	if w := call("admin", "DELETE", "/v8/neo/access/"+codex.ID, ""); w.Code != 204 {
		t.Fatalf("detach: %d %s", w.Code, w.Body.String())
	}
	gatewayCalls = nil
	if err = f.pool.SyncSource(ctx, source.ID); err != nil {
		t.Fatal(err)
	}
	for _, c := range gatewayCalls {
		if !strings.HasPrefix(c, "GET ") {
			t.Fatalf("detach or pause wrote to NeoPi: %v", gatewayCalls)
		}
	}
	items = shared("admin")
	if _, still := items["shared codex"]; still || len(items) != 1 || items["shared claude"].Health != "disabled" {
		t.Fatalf("after resync: %+v", items)
	}
	if _, ok := f.manager.GetByID(sourceAuthID(source.ID, "c1")); ok {
		t.Fatal("detached gateway account re-registered by sync")
	}
	if w := call("admin", "PATCH", "/v8/neo/access/"+claude.ID, `{"disabled":false}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"health":"healthy"`) {
		t.Fatalf("resume gateway account: %d %s", w.Code, w.Body.String())
	}
}
