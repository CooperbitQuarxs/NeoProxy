package neo

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func identityHTTP(t *testing.T) (*Store, *gin.Engine) {
	t.Helper()
	s, _ := newTestStore(t)
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	h := NewHTTP(s)
	operator := func(c *gin.Context) {
		if c.GetHeader("Authorization") != "Bearer operator-secret" {
			c.AbortWithStatus(401)
			return
		}
		c.Next()
	}
	h.RegisterRoutes(engine, operator)
	engine.POST("/v8/management/mutate", h.ManagementMiddleware(operator), func(c *gin.Context) { c.Status(204) })
	engine.GET("/v8/management/config", h.ManagementMiddleware(operator), func(c *gin.Context) { c.JSON(200, gin.H{"engine": true}) })
	return s, engine
}
func requestJSON(engine http.Handler, method, path string, body any, token string, headers map[string]string) *httptest.ResponseRecorder {
	var b bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	}
	req := httptest.NewRequest(method, "http://proxy.test"+path, &b)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.AddCookie(&http.Cookie{Name: SessionCookie, Value: token})
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	out := httptest.NewRecorder()
	engine.ServeHTTP(out, req)
	return out
}
func mustStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d want %d: %s", w.Code, status, w.Body.String())
	}
}
func TestHTTPBootstrapSessionCookieAndLogout(t *testing.T) {
	_, engine := identityHTTP(t)
	body := gin.H{"handle": "captain", "password": testPassword}
	native := map[string]string{"X-Neoproxy-CSRF": "1"}
	mustStatus(t, requestJSON(engine, "POST", "/v8/session/bootstrap", body, "", native), 401)
	// The operator secret alone cannot bypass the origin check.
	mustStatus(t, requestJSON(engine, "POST", "/v8/session/bootstrap", body, "", map[string]string{"Authorization": "Bearer operator-secret"}), 403)
	native["Authorization"] = "Bearer operator-secret"
	out := requestJSON(engine, "POST", "/v8/session/bootstrap", body, "", native)
	mustStatus(t, out, 201)
	var session Session
	if err := json.Unmarshal(out.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Role != "admin" || session.UserID == "" || session.ExpiresAt == 0 {
		t.Fatal(session)
	}
	cookies := out.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal(cookies)
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Secure {
		t.Fatal("incorrect local cookie flags", cookie)
	}
	mustStatus(t, requestJSON(engine, "GET", "/v8/session", nil, cookie.Value, nil), 200)
	mustStatus(t, requestJSON(engine, "POST", "/v8/session/bootstrap", body, "", native), 409)
	mustStatus(t, requestJSON(engine, "DELETE", "/v8/session", nil, cookie.Value, map[string]string{"Origin": "http://attacker.test"}), 403)
	mustStatus(t, requestJSON(engine, "GET", "/v8/session", nil, cookie.Value, nil), 200)
	logout := requestJSON(engine, "DELETE", "/v8/session", nil, cookie.Value, map[string]string{"Origin": "http://proxy.test"})
	mustStatus(t, logout, 204)
	if cookies = logout.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatal("logout did not delete cookie")
	}
	mustStatus(t, requestJSON(engine, "GET", "/v8/session", nil, cookie.Value, nil), 401)
}
func TestHTTPRoleAndKeyAuthorityMatrix(t *testing.T) {
	s, engine := identityHTTP(t)
	ctx := t.Context()
	admin := testUser(t, s, "admin", RoleAdmin)
	owner := testUser(t, s, "owner", RoleOwner)
	member := testUser(t, s, "member", RoleMember)
	service := testUser(t, s, "service", RoleService)
	native := map[string]string{"X-Neoproxy-CSRF": "1"}
	for _, u := range []User{admin, owner, member} {
		t.Run(u.Role, func(t *testing.T) {
			_, token, err := s.Login(ctx, u.Handle, testPassword)
			if err != nil {
				t.Fatal(err)
			}
			mustStatus(t, requestJSON(engine, "GET", "/v8/neo/users/"+u.ID, nil, token, nil), 200)
			expected := 403
			if u.Role == RoleAdmin {
				expected = 200
			}
			mustStatus(t, requestJSON(engine, "GET", "/v8/neo/users", nil, token, nil), expected)
			mustStatus(t, requestJSON(engine, "GET", "/v8/management/config", nil, token, nil), expected)
			expected = 403
			if u.Role == RoleAdmin {
				expected = 204
			}
			mustStatus(t, requestJSON(engine, "POST", "/v8/management/mutate", nil, token, native), expected)
			if u.Role != RoleAdmin {
				mustStatus(t, requestJSON(engine, "GET", "/v8/neo/users/"+admin.ID, nil, token, nil), 403)
				mustStatus(t, requestJSON(engine, "PUT", "/v8/neo/users/"+u.ID, gin.H{"role": "admin"}, token, native), 403)
				mustStatus(t, requestJSON(engine, "POST", "/v8/neo/users/"+admin.ID+"/keys", gin.H{"label": "steal"}, token, native), 403)
			}
		})
	}
	mustStatus(t, requestJSON(engine, "POST", "/v8/session", gin.H{"handle": service.Handle, "password": testPassword}, "", native), 403)
	_, secret, err := s.MintKey(ctx, testActor(admin), admin.ID, "admin-inference", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	keyHeader := map[string]string{"Authorization": "Bearer " + secret, "X-Neoproxy-CSRF": "1"}
	mustStatus(t, requestJSON(engine, "GET", "/v8/session", nil, "", keyHeader), 401)
	mustStatus(t, requestJSON(engine, "GET", "/v8/neo/users", nil, "", keyHeader), 401)
	mustStatus(t, requestJSON(engine, "GET", "/v8/management/config", nil, "", keyHeader), 403)
	// Explicit operator maintenance still works without creating a dashboard session.
	operator := map[string]string{"Authorization": "Bearer operator-secret"}
	mustStatus(t, requestJSON(engine, "GET", "/v8/management/config", nil, "", operator), 200)
	mustStatus(t, requestJSON(engine, "GET", "/v8/session", nil, "", operator), 401)
}
func TestHTTPMutationOriginAndPasswordTransitions(t *testing.T) {
	s, engine := identityHTTP(t)
	ctx := t.Context()
	u := testUser(t, s, "member", RoleMember)
	_, token, err := s.Login(ctx, u.Handle, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	body := gin.H{"label": "local-client"}
	route := "/v8/neo/users/" + u.ID + "/keys"
	for _, headers := range []map[string]string{
		nil,
		{"Origin": "null"},
		{"Origin": "http://evil.test"},
		{"Origin": "https://proxy.test"},
		{"Origin": "http://proxy.test.evil.test"},
		{"Origin": "http://proxy.test/path"},
		{"Origin": "http://proxy.test", "Sec-Fetch-Site": "cross-site"},
		{"Origin": "http://evil.test", "X-Neoproxy-CSRF": "1"},
	} {
		mustStatus(t, requestJSON(engine, "POST", route, body, token, headers), 403)
	}
	out := requestJSON(engine, "POST", route, body, token, map[string]string{"Origin": "http://proxy.test"})
	mustStatus(t, out, 201)
	var issued struct {
		Key    Key    `json:"key"`
		Secret string `json:"secret"`
	}
	if err = json.Unmarshal(out.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	if !keyPattern.MatchString(issued.Secret) || issued.Key.Prefix == issued.Secret {
		t.Fatal("invalid issued key")
	}
	native := map[string]string{"X-Neoproxy-CSRF": "1"}
	mustStatus(t, requestJSON(engine, "POST", "/v8/neo/users/"+u.ID+"/password", gin.H{"currentPassword": "wrong", "password": "replacement-password"}, token, native), 401)
	mustStatus(t, requestJSON(engine, "POST", "/v8/neo/users/"+u.ID+"/password", gin.H{"currentPassword": testPassword, "password": "replacement-password"}, token, native), 204)
	mustStatus(t, requestJSON(engine, "GET", "/v8/session", nil, token, nil), 401)
	if _, err = s.AuthenticateKey(ctx, issued.Secret); err == nil {
		t.Fatal("password change retained HTTP-minted key")
	}
	out = requestJSON(engine, "POST", "/v8/session", gin.H{"handle": u.Handle, "password": "wrong"}, "", native)
	mustStatus(t, out, 401)
	if strings.Contains(out.Body.String(), u.Handle) {
		t.Fatal("login error disclosed handle")
	}
}
func TestHTTPSCookieSecureAndUnconfiguredStore(t *testing.T) {
	s, _ := newTestStore(t)
	_ = testUser(t, s, "member", RoleMember)
	engine := gin.New()
	NewHTTP(s).RegisterRoutes(engine, func(c *gin.Context) { c.AbortWithStatus(401) })
	req := httptest.NewRequest("POST", "https://proxy.test/v8/session", strings.NewReader(`{"handle":"member","password":"`+testPassword+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://proxy.test")
	out := httptest.NewRecorder()
	engine.ServeHTTP(out, req)
	mustStatus(t, out, 200)
	if cookies := out.Result().Cookies(); len(cookies) != 1 || !cookies[0].Secure {
		t.Fatal("HTTPS session lacked Secure cookie")
	}
	unavailable := gin.New()
	NewHTTP(nil).RegisterRoutes(unavailable, func(c *gin.Context) { c.AbortWithStatus(401) })
	mustStatus(t, requestJSON(unavailable, "POST", "/v8/session", gin.H{}, "", map[string]string{"X-Neoproxy-CSRF": "1"}), 503)
}

func TestHTTPAdminUserCRUDGrantsAndOneTimeKeys(t *testing.T) {
	s, engine := identityHTTP(t)
	admin := testUser(t, s, "admin", RoleAdmin)
	_, token, err := s.Login(t.Context(), admin.Handle, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	native := map[string]string{"X-Neoproxy-CSRF": "1"}
	in := UserInput{Handle: "new-member", Name: "Member", Role: RoleMember, State: "active", Password: testPassword}
	out := requestJSON(engine, "POST", "/v8/neo/users", in, token, native)
	mustStatus(t, out, 201)
	var u User
	if err = json.Unmarshal(out.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	if u.ID == "" || len(u.Keys) != 0 {
		t.Fatal(u)
	}
	in.Password = ""
	in.Grants = []Grant{{VirtualModel: "vm-granted", Allotment: .25}}
	in.ProviderAccess = []string{"codex"}
	out = requestJSON(engine, "PUT", "/v8/neo/users/"+u.ID, in, token, native)
	mustStatus(t, out, 200)
	if err = json.Unmarshal(out.Body.Bytes(), &u); err != nil || len(u.Grants) != 1 || u.Grants[0].Allotment != .25 {
		t.Fatal("grant not persisted", u, err)
	}
	out = requestJSON(engine, "POST", "/v8/neo/users/"+u.ID+"/keys", gin.H{"label": "client"}, token, native)
	mustStatus(t, out, 201)
	var issued struct {
		Key    Key    `json:"key"`
		Secret string `json:"secret"`
	}
	if err = json.Unmarshal(out.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	out = requestJSON(engine, "GET", "/v8/neo/users/"+u.ID, nil, token, nil)
	mustStatus(t, out, 200)
	if strings.Contains(out.Body.String(), issued.Secret) {
		t.Fatal("key secret was returned more than once")
	}
	mustStatus(t, requestJSON(engine, "DELETE", "/v8/neo/users/"+u.ID+"/keys/"+issued.Key.ID, nil, token, native), 204)
	if _, err = s.AuthenticateKey(t.Context(), issued.Secret); err == nil {
		t.Fatal("HTTP revoke retained inference access")
	}
	mustStatus(t, requestJSON(engine, "DELETE", "/v8/neo/users/"+u.ID, nil, token, native), 204)
	mustStatus(t, requestJSON(engine, "GET", "/v8/neo/users/"+u.ID, nil, token, nil), 404)
}
