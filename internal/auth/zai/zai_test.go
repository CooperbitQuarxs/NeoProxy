package zai

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
)

// signInCapture is testdata/neopi_zai_auth_capture.json: NeoPi's OWN
// zai-coding-plan flow and zai-mint-key hook against synthetic replies.
type signInCapture struct {
	Scenarios []struct {
		Name  string `json:"name"`
		Code  string `json:"code"`
		State string `json:"state"`
		Auth  struct {
			URL string `json:"url"`
		} `json:"auth"`
		Credential *struct {
			Access    string `json:"access"`
			Email     string `json:"email"`
			AccountID string `json:"accountId"`
		} `json:"credential"`
		Error    string                       `json:"error"`
		Token    *signInReply                 `json:"token"`
		Login    *signInReply                 `json:"login"`
		Customer *signInReply                 `json:"customer"`
		List     *signInReply                 `json:"list"`
		Create   *signInReply                 `json:"create"`
		Copy     *signInReply                 `json:"copy"`
		Requests []map[string]json.RawMessage `json:"requests"`
	} `json:"scenarios"`
}

type signInReply struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

// The capture script's default replies.
var (
	defaultToken    = signInReply{Body: json.RawMessage(`{"code":0,"msg":"success","data":{"zai":{"access_token":"oauth-access-fake"},"user":{"email":"Zai@Example.Invalid","id":4242}}}`)}
	defaultLogin    = signInReply{Body: json.RawMessage(`{"code":200,"msg":"ok","success":true,"data":{"access_token":"biz-token-fake"}}`)}
	defaultCustomer = signInReply{Body: json.RawMessage(`{"code":200,"success":true,"data":{"organizations":[{"organizationId":"org-other","isDefault":false,"projects":[{"projectId":"proj-other","isDefault":true}]},{"organizationId":"org-main","isDefault":true,"projects":[{"projectId":"proj-first","isDefault":false},{"projectId":"proj-main","isDefault":true}]}]}}`)}
	defaultList     = signInReply{Body: json.RawMessage(`{"code":200,"success":true,"data":{"list":[{"name":"zcode-api-key","apiKey":"ak-zcode"},{"name":"oh-my-pi","apiKey":"ak-existing"}]}}`)}
	defaultCreate   = signInReply{Body: json.RawMessage(`{"code":200,"success":true,"data":{"apiKey":"ak-created"}}`)}
	defaultCopy     = signInReply{Body: json.RawMessage(`{"code":200,"success":true,"data":{"secretKey":"secret-fake-0000"}}`)}
)

type redirect struct{ target *url.URL }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("X-Capture-Url", req.URL.String())
	clone.URL.Scheme, clone.URL.Host = r.target.Scheme, r.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

// The sign-in sends NeoPi's requests byte for byte and reports its outcomes:
// the authorize URL, token exchange, business login, key lookup or creation
// and secret copy, and every failure message.
func TestSignInMatchesNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_zai_auth_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture signInCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			pick := func(reply *signInReply, fallback signInReply) signInReply {
				if reply != nil {
					return *reply
				}
				return fallback
			}
			var mu sync.Mutex
			var requests []map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				original := r.Header.Get("X-Capture-Url")
				headers := map[string]any{}
				for name, values := range r.Header {
					lower := strings.ToLower(name)
					if lower != "x-capture-url" && lower != "content-length" {
						headers[lower] = strings.Join(values, ", ")
					}
				}
				mu.Lock()
				requests = append(requests, map[string]any{"method": r.Method, "url": original, "headers": headers, "body": string(body)})
				mu.Unlock()
				path := r.URL.Path
				var reply signInReply
				switch {
				case path == "/api/v1/oauth/token":
					reply = pick(scenario.Token, defaultToken)
				case path == "/api/auth/z/login":
					reply = pick(scenario.Login, defaultLogin)
				case path == "/api/biz/customer/getCustomerInfo":
					reply = pick(scenario.Customer, defaultCustomer)
				case strings.Contains(path, "/copy/"):
					reply = pick(scenario.Copy, defaultCopy)
				case strings.HasSuffix(path, "/api_keys") && r.Method == http.MethodGet:
					reply = pick(scenario.List, defaultList)
				case strings.HasSuffix(path, "/api_keys") && r.Method == http.MethodPost:
					reply = pick(scenario.Create, defaultCreate)
				default:
					w.WriteHeader(599)
					return
				}
				status := reply.Status
				if status == 0 {
					status = http.StatusOK
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				// NeoPi's capture server sent compact JSON (Response.json).
				var compact bytes.Buffer
				_ = json.Compact(&compact, reply.Body)
				_, _ = w.Write(compact.Bytes())
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			service := NewService(&http.Client{Transport: redirect{target: target}})
			if got := service.AuthorizationURL(scenario.State); got != scenario.Auth.URL {
				t.Errorf("authorize URL = %s, want %s", got, scenario.Auth.URL)
			}
			code := scenario.Code
			if code == "" {
				code = "the-code"
			}
			ctx := context.Background()
			token, account, err := service.ExchangeCode(ctx, code, scenario.State)
			var key string
			if err == nil {
				key, err = service.MintAPIKey(ctx, token)
			}
			switch {
			case scenario.Error != "":
				if err == nil || err.Error() != scenario.Error {
					t.Errorf("error = %v, want %q", err, scenario.Error)
				}
			case err != nil:
				t.Fatalf("sign-in failed: %v", err)
			default:
				if key != scenario.Credential.Access || account.Email != scenario.Credential.Email || account.ID != scenario.Credential.AccountID {
					t.Errorf("credential = %q %+v, want %+v", key, account, *scenario.Credential)
				}
			}
			if len(requests) != len(scenario.Requests) {
				t.Fatalf("requests = %d, want %d", len(requests), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				var wantHeaders map[string]any
				_ = json.Unmarshal(want["headers"], &wantHeaders)
				delete(wantHeaders, "host")
				delete(wantHeaders, "content-length")
				gotJSON, _ := json.Marshal(map[string]any{"method": requests[i]["method"], "url": requests[i]["url"], "headers": requests[i]["headers"], "body": requests[i]["body"]})
				var wantMethod, wantURL, wantBody string
				_ = json.Unmarshal(want["method"], &wantMethod)
				_ = json.Unmarshal(want["url"], &wantURL)
				_ = json.Unmarshal(want["body"], &wantBody)
				wantJSON, _ := json.Marshal(map[string]any{"method": wantMethod, "url": wantURL, "headers": wantHeaders, "body": wantBody})
				if string(gotJSON) != string(wantJSON) {
					t.Errorf("request %d:\n got %s\nwant %s", i, gotJSON, wantJSON)
				}
			}
		})
	}
}
