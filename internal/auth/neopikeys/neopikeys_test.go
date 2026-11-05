package neopikeys

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
)

type capturedRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

// keysCapture is testdata/neopi_keys_capture.json: NeoPi's OWN createApiKeyLogin
// (and loginAlibabaTokenPlan) over a scripted loopback server.
type keysCapture struct {
	Scenarios []struct {
		Provider     string            `json:"provider"`
		Label        string            `json:"label"`
		Input        string            `json:"input"`
		Answers      []string          `json:"answers"`
		Env          *string           `json:"env"`
		EnvVars      map[string]string `json:"envVars"`
		Status       int               `json:"status"`
		Body         json.RawMessage   `json:"body"`
		NetworkError bool              `json:"networkError"`
		Result       struct {
			Value     captureValue `json:"value"`
			Error     string       `json:"error"`
			ErrorName string       `json:"errorName"`
		} `json:"result"`
		Requests []capturedRequest `json:"requests"`
	} `json:"scenarios"`
}

// captureValue is a login's result: the key, or for a sign-in that returns a
// credential, its access key and the endpoint it chose (enterpriseUrl).
type captureValue struct {
	Key, BaseURL string
}

func (v *captureValue) UnmarshalJSON(raw []byte) error {
	if len(raw) > 0 && raw[0] == '"' {
		return json.Unmarshal(raw, &v.Key)
	}
	var credential struct {
		Access        string `json:"access"`
		EnterpriseURL string `json:"enterpriseUrl"`
	}
	if err := json.Unmarshal(raw, &credential); err != nil {
		return err
	}
	v.Key, v.BaseURL = credential.Access, credential.EnterpriseURL
	return nil
}

// codingPlanBase is the endpoint the Coding Plan sign-in's answers choose:
// 2 is China, 3 a custom URL, anything else International.
func codingPlanBase(answers []string) string {
	switch {
	case len(answers) > 0 && strings.TrimSpace(answers[0]) == "2":
		return AlibabaCodingPlanCNURL
	case len(answers) > 1 && strings.TrimSpace(answers[0]) == "3":
		return answers[1]
	}
	return AlibabaCodingPlanIntlURL
}

// redirect sends every request to a loopback server and keeps the original URL.
type redirect struct {
	target *url.URL
	err    error
	seen   *[]capturedRequest
}

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	if r.err != nil {
		return nil, r.err
	}
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
	}
	headers := map[string]string{}
	for name := range req.Header {
		headers[strings.ToLower(name)] = req.Header.Get(name)
	}
	if req.ContentLength > 0 {
		headers["content-length"] = strconv.FormatInt(req.ContentLength, 10)
	}
	*r.seen = append(*r.seen, capturedRequest{Method: req.Method, URL: req.URL.String(), Headers: headers, Body: string(body)})
	forward := req.Clone(req.Context())
	forward.URL.Scheme, forward.URL.Host = r.target.Scheme, r.target.Host
	forward.Body = io.NopCloser(bytes.NewReader(body))
	forward.RequestURI = ""
	return http.DefaultTransport.RoundTrip(forward)
}

func TestKeyEntryMatchesNeoPiCapture(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_keys_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture keysCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, scenario := range capture.Scenarios {
		login, ok := LoginFor(scenario.Provider)
		if scenario.Provider == "alibaba-coding-plan" {
			login, ok = AlibabaCodingPlanLogin(codingPlanBase(scenario.Answers)), true
		}
		if !ok {
			continue
		}
		checked++
		t.Run(scenario.Provider+"/"+scenario.Label, func(t *testing.T) {
			env := map[string]string{}
			if scenario.Env != nil {
				env["MOONSHOT_BASE_URL"] = *scenario.Env
			}
			for name, value := range scenario.EnvVars {
				env[name] = value
			}
			for _, name := range []string{"MOONSHOT_BASE_URL", "COREWEAVE_PROJECT", "WANDB_INFERENCE_PROJECT", "WANDB_PROJECT", "WANDB_ENTITY"} {
				t.Setenv(name, env[name])
			}
			key, normalizeErr := login.NormalizeKey(scenario.Input)
			if normalizeErr != nil {
				if scenario.Result.Error != normalizeErr.Error() && !(scenario.Result.ErrorName == "ApiKeyRequiredError" && errors.Is(normalizeErr, ErrKeyRequired)) {
					t.Fatalf("NormalizeKey error = %q, want %q", normalizeErr, scenario.Result.Error)
				}
				if len(scenario.Requests) != 0 {
					t.Fatalf("NeoPi probed after %q", scenario.Result.Error)
				}
				return
			}
			status := scenario.Status
			if status == 0 {
				status = http.StatusOK
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				var text string
				if json.Unmarshal(scenario.Body, &text) == nil && len(scenario.Body) > 0 && scenario.Body[0] == '"' {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(text))
					return
				}
				body := `{"data":[{"id":"fixture-model"}],"choices":[{"message":{"content":"pong"}}]}`
				if len(scenario.Body) > 0 && string(scenario.Body) != "null" {
					var compact bytes.Buffer
					_ = json.Compact(&compact, scenario.Body)
					body = compact.String()
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			var seen []capturedRequest
			rt := redirect{target: target, seen: &seen}
			if scenario.NetworkError {
				rt.err = errors.New("fetch failed")
			}
			validateErr := login.Validate(context.Background(), &http.Client{Transport: rt}, key)

			if want := scenario.Result.Value; want.Key != "" && key != want.Key {
				t.Errorf("key = %q, want %q", key, want.Key)
			}
			if want := scenario.Result.Value.BaseURL; want != "" && AlibabaCodingPlanBaseURL(codingPlanBase(scenario.Answers)) != want {
				t.Errorf("endpoint = %q, want %q", AlibabaCodingPlanBaseURL(codingPlanBase(scenario.Answers)), want)
			}
			if len(seen) != len(scenario.Requests) {
				t.Fatalf("requests = %d, want %d", len(seen), len(scenario.Requests))
			}
			for i, want := range scenario.Requests {
				got := seen[i]
				if got.Method != want.Method || got.URL != want.URL || got.Body != want.Body {
					t.Errorf("request %d = %s %s %q, want %s %s %q", i, got.Method, got.URL, got.Body, want.Method, want.URL, want.Body)
				}
				for name, value := range want.Headers {
					if name == "host" {
						continue
					}
					if got.Headers[name] != value {
						t.Errorf("request %d header %s = %q, want %q", i, name, got.Headers[name], value)
					}
				}
				for name := range got.Headers {
					if _, ok := want.Headers[name]; !ok {
						t.Errorf("request %d has extra header %s", i, name)
					}
				}
			}
			if scenario.Result.Value.Key != "" {
				if validateErr != nil {
					t.Fatalf("Validate() = %v, want success", validateErr)
				}
				return
			}
			if validateErr == nil {
				t.Fatalf("Validate() succeeded, want %q", scenario.Result.Error)
			}
			var httpErr *HTTPError
			if scenario.NetworkError {
				if errors.As(validateErr, &httpErr) {
					t.Fatalf("Validate() = %v, want a transport failure", validateErr)
				}
				return
			}
			if scenario.Result.ErrorName == "ConfigurationError" {
				if validateErr.Error() != scenario.Result.Error {
					t.Fatalf("Validate() = %v, want %q", validateErr, scenario.Result.Error)
				}
				return
			}
			if !errors.As(validateErr, &httpErr) || httpErr.Error() != scenario.Result.Error {
				t.Fatalf("Validate() = %v, want %q", validateErr, scenario.Result.Error)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no scenarios replayed")
	}
}

// Groq, Mistral and AIML API have no key entry in NeoPi, so a key is accepted
// untouched.
func TestProvidersWithoutProbeAcceptKey(t *testing.T) {
	for _, provider := range []string{"groq", "mistral", "aimlapi"} {
		login, ok := LoginFor(provider)
		if !ok || login.Probe != nil {
			t.Fatalf("%s login = %+v, %v", provider, login, ok)
		}
		if err := login.Validate(context.Background(), &http.Client{Transport: redirect{err: errors.New("no network")}}, "key"); err != nil {
			t.Errorf("%s: %v", provider, err)
		}
	}
}
