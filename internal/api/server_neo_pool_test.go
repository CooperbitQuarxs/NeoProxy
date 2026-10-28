package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestNeoPoolAttachedServerHTTP(t *testing.T) {
	t.Setenv("NEOPROXY_DATA_DIR", "")
	store, _, _, _ := neoTestIdentity(t)
	owner, err := store.CreateUser(t.Context(), neo.UserInput{Handle: "pool-owner", Role: neo.RoleOwner, State: "active", Password: "correct-horse-battery"})
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWithOptions(t, WithNeoStore(store), WithRequestLoggerFactory(nil))
	if server.neoErr != nil {
		t.Fatal(server.neoErr)
	}
	const authID = "attached-pool-private@example.com.json"
	if _, err = server.handlers.AuthManager.Register(t.Context(), &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "attached-pool-private-token"}}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: "attached-pool-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	listener := httptest.NewServer(server.engine)
	defer listener.Close()
	request := func(method, path, body string, cookie *http.Cookie, want int) []byte {
		t.Helper()
		req, errRequest := http.NewRequestWithContext(t.Context(), method, listener.URL+path, strings.NewReader(body))
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Neoproxy-CSRF", "1")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		response, errRequest := listener.Client().Do(req)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		defer func() {
			if errClose := response.Body.Close(); errClose != nil {
				t.Error(errClose)
			}
		}()
		data, errRead := io.ReadAll(response.Body)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if response.StatusCode != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, response.StatusCode, want, data)
		}
		return data
	}
	login := func(handle string) *http.Cookie {
		t.Helper()
		req, errRequest := http.NewRequestWithContext(t.Context(), "POST", listener.URL+"/v8/session", strings.NewReader(`{"handle":"`+handle+`","password":"correct-horse-battery"}`))
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Neoproxy-CSRF", "1")
		response, errRequest := listener.Client().Do(req)
		if errRequest != nil {
			t.Fatal(errRequest)
		}
		defer func() {
			if errClose := response.Body.Close(); errClose != nil {
				t.Error(errClose)
			}
		}()
		if response.StatusCode != 200 {
			t.Fatalf("%s session login: %d", handle, response.StatusCode)
		}
		for _, cookie := range response.Cookies() {
			if cookie.Name == neo.SessionCookie {
				return cookie
			}
		}
		t.Fatal("login omitted session cookie")
		return nil
	}
	adminCookie := login("captain")
	ownerCookie := login(owner.Handle)
	request("GET", "/v8/neo/vault", "", nil, 401)
	var vault struct {
		Accounts []neopool.Account `json:"accounts"`
	}
	data := request("GET", "/v8/neo/vault", "", adminCookie, 200)
	if err = json.Unmarshal(data, &vault); err != nil {
		t.Fatal(err)
	}
	if len(vault.Accounts) != 1 || vault.Accounts[0].OwnerID != "" || len(vault.Accounts[0].Windows) != 0 {
		t.Fatalf("existing credential must remain unowned with unknown quota: %s", data)
	}
	if strings.Contains(string(data), "private@example.com") || strings.Contains(string(data), "private-token") {
		t.Fatal("vault disclosed engine identity or credential")
	}
	accountPath := "/v8/neo/vault/accounts/" + vault.Accounts[0].ID
	request("PATCH", accountPath, `{"ownerId":"`+owner.ID+`"}`, ownerCookie, 403)
	data = request("GET", "/v8/neo/vault", "", ownerCookie, 200)
	if err = json.Unmarshal(data, &vault); err != nil {
		t.Fatal(err)
	}
	if len(vault.Accounts) != 0 {
		t.Fatal("owner could see unowned credential")
	}
	request("PATCH", accountPath, `{"ownerId":"`+owner.ID+`"}`, adminCookie, 204)
	request("PUT", accountPath+"/windows/day", `{"capacityTokens":100,"startsAt":0}`, ownerCookie, 204)
	data = request("GET", "/v8/neo/vault", "", ownerCookie, 200)
	if err = json.Unmarshal(data, &vault); err != nil {
		t.Fatal(err)
	}
	if len(vault.Accounts) != 1 || vault.Accounts[0].OwnerID != owner.ID || len(vault.Accounts[0].Windows) != 1 || vault.Accounts[0].Windows[0].Source != "configured-metered" {
		t.Fatalf("attached pool ownership/meter unavailable: %s", data)
	}
	request("GET", "/v8/neo/owner/stats?userId=another-owner", "", ownerCookie, 200)
	request("GET", "/v8/management/config", "", ownerCookie, 403)
	request("PATCH", "/v8/neo/vault/providers/codex", `{"exposed":false}`, ownerCookie, 403)
}
