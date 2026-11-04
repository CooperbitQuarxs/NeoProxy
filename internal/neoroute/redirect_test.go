package neoroute

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neo"
)

func TestRedirectPrecedenceAndPrefix(t *testing.T) {
	astra := "neoproxy/codex/gpt-6-astra"
	rules := []Redirect{
		{ID: "global-exact", From: astra, To: "neoproxy/codex/a", Enabled: true, CreatedAt: 1},
		{ID: "user-prefix", From: "neoproxy/codex/gpt-6-astra*", To: "neoproxy/codex/b", Scope: RedirectScope{Users: []string{"u1"}}, Enabled: true, CreatedAt: 2},
		{ID: "user-exact-newer", From: astra, To: "neoproxy/codex/c", Scope: RedirectScope{Users: []string{"u1"}}, Enabled: true, CreatedAt: 4},
		{ID: "user-exact-older", From: astra, To: "neoproxy/codex/d", Scope: RedirectScope{Users: []string{"u1"}}, Enabled: true, CreatedAt: 3},
		{ID: "key-prefix", From: "neoproxy/codex/gpt-6*", To: "neoproxy/codex/e", Scope: RedirectScope{Users: []string{"u1"}, Keys: []string{"k1"}}, Enabled: true, CreatedAt: 5},
		{ID: "key-disabled", From: astra, To: "neoproxy/codex/f", Scope: RedirectScope{Users: []string{"u1"}, Keys: []string{"k1"}}, CreatedAt: 0},
	}
	for _, test := range []struct {
		name  string
		p     neo.Principal
		asked string
		want  string
	}{
		{"key scope beats user and global, prefix included", neo.Principal{UserID: "u1", KeyID: "k1"}, astra, "key-prefix"},
		{"other key of the user gets the user rule, exact first, oldest first", neo.Principal{UserID: "u1", KeyID: "k2"}, astra, "user-exact-older"},
		{"session of the user", neo.Principal{UserID: "u1"}, astra, "user-exact-older"},
		{"user prefix matches a longer name", neo.Principal{UserID: "u1"}, astra + "-mini", "user-prefix"},
		{"everyone else gets the global rule", neo.Principal{UserID: "u2", KeyID: "k9"}, astra, "global-exact"},
		{"global exact does not prefix-match", neo.Principal{UserID: "u2"}, astra + "-mini", ""},
		{"unprefixed asks never match", neo.Principal{UserID: "u2"}, "codex/gpt-6-astra", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := redirectFor(rules, test.p, test.asked)
			if (got == nil) != (test.want == "") || (got != nil && got.ID != test.want) {
				t.Fatalf("got %+v want %q", got, test.want)
			}
		})
	}
}

// redirectFixture adds a second virtual model "other" the client is not granted.
func redirectFixture(t *testing.T) (*routeFixture, VirtualModel, context.Context) {
	t.Helper()
	f := newRouteFixture(t, "fill")
	admin := neo.WithPrincipal(context.Background(), f.admin)
	other, err := f.route.SaveVirtualModel(admin, VirtualModel{Name: "other", Stages: []Stage{{ID: "s0", Model: "primary/model", Accounts: AccountScope{All: true}, Draw: "fill", PerAccount: .5, Cap: .8, Window: "day", Per: "user"}}, Terminal: Terminal{Kind: "limit", Message: "Wait"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	return f, other, admin
}

func TestRedirectValidationRejectsLoopsAndChains(t *testing.T) {
	f, _, admin := redirectFixture(t)
	first, err := f.route.SaveRedirect(admin, Redirect{From: "route", To: "neoproxy/other", Enabled: true, Note: "move to other"}, true)
	if err != nil || first.From != "neoproxy/route" || first.To != "neoproxy/other" || first.ID == "" || first.CreatedAt == 0 {
		t.Fatalf("save %+v %v", first, err)
	}
	for _, test := range []struct {
		name string
		r    Redirect
		want string
	}{
		{"self", Redirect{From: "neoproxy/primary/model", To: "primary/model", Enabled: true}, "itself"},
		{"prefix covering its own target", Redirect{From: "neoproxy/primary/*", To: "primary/model", Enabled: true}, "itself"},
		{"target is another source", Redirect{From: "neoproxy/legacy", To: "route", Enabled: true}, "is itself redirected"},
		{"source is another target", Redirect{From: "other", To: "primary/model", Enabled: true}, "do not chain"},
		{"duplicate", Redirect{From: "route", To: "primary/model", Enabled: true}, "already redirected"},
		{"unknown target", Redirect{From: "legacy", To: "nope/model", Enabled: true}, "not a model"},
		{"unknown virtual target", Redirect{From: "legacy", To: "nope", Enabled: true}, "not a virtual model"},
		{"star in the middle", Redirect{From: "neoproxy/a*b", To: "other", Enabled: true}, "only at the end"},
		{"unknown key", Redirect{From: "legacy", To: "other", Enabled: true, Scope: RedirectScope{Keys: []string{"missing"}}}, "does not exist"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := f.route.SaveRedirect(admin, test.r, true); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v want %q", err, test.want)
			}
		})
	}
	// Disabled rules and disjoint scopes do not chain, so they are accepted.
	if _, err = f.route.SaveRedirect(admin, Redirect{From: "other", To: "primary/model"}, true); err != nil {
		t.Fatalf("disabled rule rejected: %v", err)
	}
	scoped, err := f.route.SaveRedirect(admin, Redirect{From: "route", To: "other", Enabled: true, Scope: RedirectScope{Keys: []string{f.principal.KeyID}}}, true)
	if err != nil || !slices.Equal(scoped.Scope.Users, []string{f.user.ID}) {
		t.Fatalf("key scope did not record its owner: %+v %v", scoped, err)
	}
	moved := first
	moved.Scope = RedirectScope{Users: []string{f.admin.UserID}}
	if _, err = f.route.SaveRedirect(admin, moved, false); err != nil {
		t.Fatalf("narrowing scope rejected: %v", err)
	}
	if _, err = f.route.SaveRedirect(admin, Redirect{From: "other", To: "primary/model", Enabled: true, Scope: RedirectScope{Users: []string{f.admin.UserID}}}, true); err == nil {
		t.Fatal("overlapping user scope chained")
	}
	if _, err = f.route.SaveRedirect(neo.WithPrincipal(context.Background(), f.principal), Redirect{From: "legacy", To: "other", Enabled: true}, true); err == nil {
		t.Fatal("inference key saved a redirect")
	}
	if err = f.route.DeleteRedirect(admin, scoped.ID); err != nil || len(f.route.Redirects()) != 2 {
		t.Fatalf("delete %v %d", err, len(f.route.Redirects()))
	}
	// The cache survives a restart.
	reopened, err := New(f.identity, f.pool)
	if err != nil || len(reopened.Redirects()) != 2 {
		t.Fatalf("reopen %v", err)
	}
}

func TestRedirectEnforcesTargetAccessUnlessBypassed(t *testing.T) {
	f, other, admin := redirectFixture(t)
	r, err := f.route.SaveRedirect(admin, Redirect{From: "neoproxy/route", To: "neoproxy/other", Enabled: true, Note: "astra to sol"}, true)
	if err != nil {
		t.Fatal(err)
	}
	d, err := f.route.Simulate(f.ctx, boundedRequest(false))
	if err != nil || d.Reason != "not-granted" || d.VirtualModel != other.ID || d.Redirect == nil || d.Redirect.ID != r.ID || d.Redirect.From != "neoproxy/route" || d.Redirect.To != "neoproxy/other" || d.Trail[0].Kind != "redirect" || d.Trail[0].Asked != "neoproxy/route" {
		t.Fatalf("redirect granted access or went unrecorded: %+v %v", d, err)
	}
	r.BypassAccess = true
	if _, err = f.route.SaveRedirect(admin, r, false); err != nil {
		t.Fatal(err)
	}
	d, err = f.route.Simulate(f.ctx, boundedRequest(false))
	if err != nil || d.Outcome != "ok" || d.VirtualModel != other.ID || !d.Redirect.BypassAccess || d.Trail[1].Kind != "stage" {
		t.Fatalf("bypass did not serve the target: %+v %v", d, err)
	}
	// Execution records the caller's asked model and the redirect.
	execution, err := f.route.Run(f.ctx, boundedRequest(false), func(ctx context.Context, s Selection) (*Execution, error) {
		if s.Asked != "neoproxy/route" || s.Decision.VirtualModel != other.ID {
			t.Fatalf("selection %+v", s)
		}
		return successful(ctx, s)
	})
	if err != nil {
		t.Fatal(err)
	}
	record := loadRecord(t, f, execution.Headers.Get("X-NeoProxy-Request-ID"))
	if record.Asked != "neoproxy/route" || record.Decision.Redirect == nil || record.Decision.Redirect.ID != r.ID {
		t.Fatalf("record %+v", record)
	}
	// A direct target needs provider access too; bypass skips it.
	if err = f.route.DeleteRedirect(admin, r.ID); err != nil {
		t.Fatal(err)
	}
	direct, err := f.route.SaveRedirect(admin, Redirect{From: "route", To: "primary/model", Enabled: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if d, err = f.route.Simulate(f.ctx, boundedRequest(false)); err != nil || d.Outcome == "ok" || d.Redirect == nil {
		t.Fatalf("direct redirect bypassed provider access: %+v %v", d, err)
	}
	direct.BypassAccess = true
	if _, err = f.route.SaveRedirect(admin, direct, false); err != nil {
		t.Fatal(err)
	}
	if d, err = f.route.Simulate(f.ctx, boundedRequest(false)); err != nil || d.Outcome != "ok" || !d.Direct || d.Model != "primary/model" {
		t.Fatalf("bypassed direct redirect: %+v %v", d, err)
	}
}

func TestRedirectedSourceStaysListed(t *testing.T) {
	f, _, admin := redirectFixture(t)
	if _, err := f.route.SaveRedirect(admin, Redirect{From: "neoproxy/legacy", To: "route", Enabled: true, Scope: RedirectScope{Keys: []string{f.principal.KeyID}}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.route.SaveRedirect(admin, Redirect{From: "neoproxy/retired", To: "other", Enabled: true}, true); err != nil {
		t.Fatal(err)
	}
	models, err := f.route.Models(f.ctx)
	if err != nil || !slices.Equal(models, []string{"neoproxy/route", "neoproxy/legacy"}) {
		t.Fatalf("models %v %v", models, err)
	}
}

func TestRedirectHTTPAndSimulateByKey(t *testing.T) {
	f, _, _ := redirectFixture(t)
	_, adminToken, err := f.identity.Login(t.Context(), "captain", "a-test-password-123")
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	f.route.RegisterRoutes(router.Group("/v8/neo", neo.NewHTTP(f.identity).SessionMiddleware()))
	call := func(method, path, body string, want int) []byte {
		t.Helper()
		request := httptest.NewRequest(method, "/v8/neo"+path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Neoproxy-CSRF", "1")
		request.AddCookie(&http.Cookie{Name: "neoproxy_session", Value: adminToken})
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.Bytes()
	}
	var r Redirect
	if err = json.Unmarshal(call("POST", "/redirects", `{"from":"route","to":"other","enabled":true,"bypassAccess":true,"note":"laptop only","scope":{"keys":["`+f.principal.KeyID+`"]}}`, 201), &r); err != nil {
		t.Fatal(err)
	}
	call("POST", "/redirects", `{"from":"route","to":"route","enabled":true}`, 400)
	var listed []Redirect
	if err = json.Unmarshal(call("GET", "/redirects", "", 200), &listed); err != nil || len(listed) != 1 || listed[0].ID != r.ID {
		t.Fatalf("list %+v %v", listed, err)
	}
	var d Decision
	if err = json.Unmarshal(call("POST", "/simulate", `{"asked":"neoproxy/route","userId":"`+f.user.ID+`","keyId":"`+f.principal.KeyID+`"}`, 200), &d); err != nil || d.Redirect == nil || d.Redirect.ID != r.ID || d.Trail[0].Kind != "redirect" || d.Trail[0].Redirect.Note != "laptop only" {
		t.Fatalf("key simulation %+v %v", d, err)
	}
	d = Decision{}
	if err = json.Unmarshal(call("POST", "/simulate", `{"asked":"neoproxy/route","userId":"`+f.user.ID+`"}`, 200), &d); err != nil || d.Redirect != nil || d.Outcome != "ok" {
		t.Fatalf("session simulation took the key rule %+v %v", d, err)
	}
	call("POST", "/simulate", `{"asked":"neoproxy/route","userId":"`+f.admin.UserID+`","keyId":"`+f.principal.KeyID+`"}`, 400)
	r.Enabled = false
	body, _ := json.Marshal(r)
	call("PUT", "/redirects/"+r.ID, string(body), 200)
	call("PUT", "/redirects/rd_missing", string(body), 404)
	call("DELETE", "/redirects/"+r.ID, "", 204)
	call("DELETE", "/redirects/"+r.ID, "", 404)
}
