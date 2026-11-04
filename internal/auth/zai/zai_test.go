package zai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// fakeZai serves Z.ai's token, business and model endpoints with fixture data.
type fakeZai struct {
	mu        sync.Mutex
	existing  bool
	created   []string
	tokenBody map[string]string
}

func (f *fakeZai) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		_ = json.Unmarshal(raw, &f.tokenBody)
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"zai":{"access_token":"oauth-fixture"},"user":{"email":"zai@example.test","id":42}}}`)
	})
	mux.HandleFunc("POST /api/auth/z/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["token"] != "oauth-fixture" {
			_, _ = io.WriteString(w, `{"code":401,"msg":"bad token","success":false}`)
			return
		}
		_, _ = io.WriteString(w, `{"code":200,"success":true,"data":{"access_token":"biz-fixture"}}`)
	})
	requireBiz := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer biz-fixture" {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}
	mux.HandleFunc("GET /api/biz/customer/getCustomerInfo", func(w http.ResponseWriter, r *http.Request) {
		if requireBiz(w, r) {
			_, _ = io.WriteString(w, `{"code":200,"success":true,"data":{"organizations":[{"organizationId":"org-other","projects":[{"projectId":"p-x"}]},{"organizationId":"org-1","isDefault":true,"projects":[{"projectId":"p-a"},{"projectId":"p-b","isDefault":true}]}]}}`)
		}
	})
	keys := "/api/biz/v1/organization/org-1/projects/p-b/api_keys"
	mux.HandleFunc("GET "+keys, func(w http.ResponseWriter, r *http.Request) {
		if !requireBiz(w, r) {
			return
		}
		list := `[{"name":"zcode-api-key","apiKey":"zcode-id"}]`
		if f.existing {
			list = `[{"name":"zcode-api-key","apiKey":"zcode-id"},{"name":"neoproxy","apiKey":"existing-id"}]`
		}
		_, _ = io.WriteString(w, `{"code":200,"success":true,"data":{"list":`+list+`}}`)
	})
	mux.HandleFunc("POST "+keys, func(w http.ResponseWriter, r *http.Request) {
		if !requireBiz(w, r) {
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.created = append(f.created, body["name"])
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"code":200,"success":true,"data":{"name":"neoproxy","apiKey":"new-id","secretKey":"*****"}}`)
	})
	mux.HandleFunc("GET "+keys+"/copy/{id}", func(w http.ResponseWriter, r *http.Request) {
		if requireBiz(w, r) {
			_, _ = io.WriteString(w, `{"code":200,"success":true,"data":{"secretKey":"secret-of-`+r.PathValue("id")+`"}}`)
		}
	})
	mux.HandleFunc("GET /coding/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer new-id.secret-of-new-id" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"glm-5.2"},{"id":"glm-4.5-air"}]}`)
	})
	return mux
}

func newFakeService(t *testing.T, f *fakeZai) *Service {
	t.Helper()
	server := httptest.NewServer(f.handler(t))
	t.Cleanup(server.Close)
	s := NewService(server.Client())
	s.TokenURL = server.URL + "/oauth/token"
	s.BizBaseURL = server.URL
	s.ModelsBase = server.URL + "/coding"
	return s
}

func TestAuthorizationURLMatchesZCode(t *testing.T) {
	got, err := url.Parse(NewService(nil).AuthorizationURL("abc123"))
	if err != nil {
		t.Fatal(err)
	}
	q := got.Query()
	if got.Scheme+"://"+got.Host+got.Path != AuthorizeURL || q.Get("client_id") != ClientID || q.Get("response_type") != "code" || q.Get("redirect_uri") != RedirectURI || q.Get("state") != "abc123" || q.Has("code_challenge") {
		t.Fatalf("authorization URL = %s", got)
	}
}

func TestSignInMintsNeoProxyKey(t *testing.T) {
	f := &fakeZai{}
	s := newFakeService(t, f)
	ctx := context.Background()
	token, account, err := s.ExchangeCode(ctx, "the-code", "the-state")
	if err != nil || token != "oauth-fixture" || account.Email != "zai@example.test" || account.ID != "42" {
		t.Fatalf("ExchangeCode = %q %+v %v", token, account, err)
	}
	if f.tokenBody["provider"] != "zai" || f.tokenBody["code"] != "the-code" || f.tokenBody["state"] != "the-state" || f.tokenBody["redirect_uri"] != RedirectURI {
		t.Fatalf("token request body = %v", f.tokenBody)
	}
	key, err := s.MintAPIKey(ctx, token)
	if err != nil || key != "new-id.secret-of-new-id" {
		t.Fatalf("MintAPIKey = %q, %v", key, err)
	}
	if len(f.created) != 1 || f.created[0] != KeyName {
		t.Fatalf("created keys = %v", f.created)
	}
	models, err := s.ListModels(ctx, key)
	if err != nil || strings.Join(models, ",") != "glm-5.2,glm-4.5-air" {
		t.Fatalf("ListModels = %v, %v", models, err)
	}
}

func TestSignInReusesExistingNeoProxyKey(t *testing.T) {
	f := &fakeZai{existing: true}
	key, err := newFakeService(t, f).MintAPIKey(context.Background(), "oauth-fixture")
	if err != nil || key != "existing-id.secret-of-existing-id" || len(f.created) != 0 {
		t.Fatalf("MintAPIKey = %q, %v (created %v)", key, err, f.created)
	}
}

func TestEnvelopeFailureHidesBody(t *testing.T) {
	_, err := newFakeService(t, &fakeZai{}).MintAPIKey(context.Background(), "wrong-token")
	if err == nil || !strings.Contains(err.Error(), "business login failed: bad token") {
		t.Fatalf("err = %v", err)
	}
}
