package neopool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopigateway"
)

const pairingBearer = "pairing-gateway-secret"

func pairingGateway(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+pairingBearer {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"native/chat"}]}`))
		case "/v1/neoproxy/accounts":
			_, _ = w.Write([]byte(`{"data":[{"id":"7","provider":"native","label":"pairing account","models":["native/chat"],"state":"healthy","windows":[]}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type pairingHarness struct {
	t      *testing.T
	router *gin.Engine
	tokens map[string]string
}

func newPairingHarness(t *testing.T) (*pairingHarness, *fixture) {
	t.Helper()
	f := newFixture(t)
	attachSourceExecutor(t, f.pool, f.manager)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	NewHTTP(f.pool, nil).Register(router.Group("/v8/neo", neo.NewHTTP(f.identity).SessionMiddleware()))
	h := &pairingHarness{t: t, router: router, tokens: map[string]string{}}
	for _, id := range []string{"admin", "owner-a"} {
		sum := sha256.Sum256([]byte(id))
		token := hex.EncodeToString(sum[:])
		hash := sha256.Sum256([]byte(token))
		if _, err := f.identity.DB().Exec(`INSERT INTO neo_sessions(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,0)`, hex.EncodeToString(hash[:]), id, int64(1<<62)); err != nil {
			t.Fatal(err)
		}
		h.tokens[id] = token
	}
	return h, f
}

func (h *pairingHarness) do(user, method, path, body string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: neo.SessionCookie, Value: h.tokens[user]})
	req.Header.Set("X-Neoproxy-CSRF", "1")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), pairingBearer) {
		h.t.Fatalf("%s %s returned the gateway bearer: %s", method, path, w.Body.String())
	}
	return w
}

func TestConnectionLinkCreatesEnabledSourceNamedFromLink(t *testing.T) {
	h, _ := newPairingHarness(t)
	gateway := pairingGateway(t)
	u, _ := url.Parse(gateway.URL)
	link := "neopi-gateway://" + u.Host + "?token=" + pairingBearer + "&name=blackswan"

	w := h.do("admin", http.MethodPost, "/v8/neo/vault/sources", `{"connectionLink":"`+link+`"}`)
	var source Source
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &source) != nil {
		t.Fatalf("link source: %d %s", w.Code, w.Body.String())
	}
	if source.Label != "blackswan" || source.URL != gateway.URL || !source.Enabled || source.OwnerID != "admin" || source.Status != "connected" || source.AccountCount != 1 {
		t.Fatalf("link source state: %+v", source)
	}

	w = h.do("admin", http.MethodPost, "/v8/neo/vault/sources", `{"connectionLink":"https://`+u.Host+`?token=x"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "connection link") {
		t.Fatalf("bad scheme: %d %s", w.Code, w.Body.String())
	}
	w = h.do("admin", http.MethodPost, "/v8/neo/vault/sources", `{"connectionLink":"`+link+`","url":"`+gateway.URL+`","bearer":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("link plus url accepted: %d %s", w.Code, w.Body.String())
	}
	if w = h.do("owner-a", http.MethodPost, "/v8/neo/vault/sources", `{"connectionLink":"`+link+`"}`); w.Code != http.StatusForbidden {
		t.Fatalf("owner created a source: %d %s", w.Code, w.Body.String())
	}
}

func TestLocalNeoPiDetectionAndConnect(t *testing.T) {
	h, _ := newPairingHarness(t)
	gateway := pairingGateway(t)
	root := filepath.Join(t.TempDir(), ".omp")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(gateway.URL)
	runtime := `{"pid":1,"hostname":"` + u.Hostname() + `","port":` + u.Port() + `,"auth":"token","startedAt":0,"version":"test"}`
	if err := os.WriteFile(filepath.Join(root, neopigateway.RuntimeFileName), []byte(runtime), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, neopigateway.TokenFileName), []byte(pairingBearer+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	previous := neopiConfigRoot
	neopiConfigRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { neopiConfigRoot = previous })

	if w := h.do("owner-a", http.MethodGet, "/v8/neo/access/neopi/local", ""); w.Code != http.StatusForbidden {
		t.Fatalf("owner saw local detection: %d %s", w.Code, w.Body.String())
	}
	var local struct {
		Found            bool   `json:"found"`
		Running          bool   `json:"running"`
		URL              string `json:"url"`
		Name             string `json:"name"`
		AccountCount     *int   `json:"accountCount"`
		AlreadyConnected string `json:"alreadyConnected"`
		Reason           string `json:"reason"`
	}
	w := h.do("admin", http.MethodGet, "/v8/neo/access/neopi/local", "")
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &local) != nil {
		t.Fatalf("detect: %d %s", w.Code, w.Body.String())
	}
	if !local.Found || !local.Running || local.URL != gateway.URL || local.AccountCount == nil || *local.AccountCount != 1 || local.AlreadyConnected != "" || local.Reason != "" || local.Name == "" {
		t.Fatalf("detected: %s", w.Body.String())
	}

	w = h.do("admin", http.MethodPost, "/v8/neo/access/neopi/local/connect", `{"ownerId":"owner-a"}`)
	var source Source
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &source) != nil {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	if source.URL != gateway.URL || source.OwnerID != "owner-a" || !source.Enabled || source.Status != "connected" || source.AccountCount != 1 || source.Label != local.Name {
		t.Fatalf("connected source: %+v", source)
	}

	w = h.do("admin", http.MethodGet, "/v8/neo/access/neopi/local", "")
	if json.Unmarshal(w.Body.Bytes(), &local) != nil || local.AlreadyConnected != source.ID {
		t.Fatalf("already connected not reported: %s", w.Body.String())
	}
	w = h.do("admin", http.MethodPost, "/v8/neo/access/neopi/local/connect", "")
	var again Source
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &again) != nil || again.ID != source.ID {
		t.Fatalf("second connect created a duplicate: %d %s", w.Code, w.Body.String())
	}

	stopped := httptest.NewServer(http.NotFoundHandler())
	su, _ := url.Parse(stopped.URL)
	stopped.Close()
	other := filepath.Join(t.TempDir(), ".omp")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	stoppedRuntime := `{"pid":1,"hostname":"` + su.Hostname() + `","port":` + su.Port() + `,"auth":"token","startedAt":0,"version":"test"}`
	if err := os.WriteFile(filepath.Join(other, neopigateway.RuntimeFileName), []byte(stoppedRuntime), 0o600); err != nil {
		t.Fatal(err)
	}
	neopiConfigRoot = func() (string, error) { return other, nil }
	w = h.do("admin", http.MethodPost, "/v8/neo/access/neopi/local/connect", "{}")
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "not running") || !strings.Contains(w.Body.String(), neopigateway.StartCommand) {
		t.Fatalf("stopped gateway connect: %d %s", w.Code, w.Body.String())
	}
}
