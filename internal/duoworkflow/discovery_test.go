package duoworkflow

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

// discoveryCapture is testdata/gitlab_duo_agent_discovery.json: NeoPi's OWN
// fetchGitLabDuoWorkflowModels and discoverGitLabDuoWorkflowRuntimeNamespace
// against a scripted gitlab.com, each request recorded with its answer
// (capture_neopi_gitlab_duo_agent_discovery.ts).
type discoveryCapture struct {
	APIKey    string `json:"apiKey"`
	Scenarios []struct {
		Name    string            `json:"name"`
		Env     map[string]string `json:"env"`
		Config  map[string]string `json:"config"`
		Models  discoveryRun      `json:"models"`
		Runtime discoveryRun      `json:"runtime"`
	} `json:"scenarios"`
}

type discoveryRun struct {
	Requests []struct {
		Method   string            `json:"method"`
		URL      string            `json:"url"`
		Headers  map[string]string `json:"headers"`
		Body     string            `json:"body"`
		Response struct {
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"response"`
	} `json:"requests"`
	Result json.RawMessage `json:"result"`
	Error  *string         `json:"error"`
}

// replay answers each request with the captured answer at its position,
// failing on any request NeoPi did not send there.
type replay struct {
	t    *testing.T
	run  discoveryRun
	sent int
}

func (r *replay) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Helper()
	if r.sent >= len(r.run.Requests) {
		r.t.Fatalf("request %d %s %s: NeoPi sent only %d", r.sent, req.Method, req.URL, len(r.run.Requests))
	}
	want := r.run.Requests[r.sent]
	r.sent++
	var body string
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
	}
	if req.Method != want.Method || req.URL.String() != want.URL || body != want.Body {
		r.t.Fatalf("request %d = %s %s %q, NeoPi sent %s %s %q", r.sent-1, req.Method, req.URL, body, want.Method, want.URL, want.Body)
	}
	headers := map[string]string{}
	for name := range req.Header {
		headers[strings.ToLower(name)] = req.Header.Get(name)
	}
	expected := map[string]string{}
	for name, value := range want.Headers {
		if name != "content-length" {
			expected[name] = value
		}
	}
	if !reflect.DeepEqual(headers, expected) {
		r.t.Fatalf("request %d headers = %v, NeoPi sent %v", r.sent-1, headers, expected)
	}
	answer := &http.Response{StatusCode: want.Response.Status, Header: http.Header{}, Request: req, Body: io.NopCloser(strings.NewReader(want.Response.Body))}
	for name, value := range want.Response.Headers {
		answer.Header.Set(name, value)
	}
	return answer, nil
}

func (r *replay) done() {
	r.t.Helper()
	if r.sent != len(r.run.Requests) {
		r.t.Fatalf("sent %d requests, NeoPi sent %d", r.sent, len(r.run.Requests))
	}
}

// Every captured discovery: the requests NeoPi sends in order, and the model
// rows or runtime namespace it settles on, or its error.
func TestDiscoveryMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/gitlab_duo_agent_discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture discoveryCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range capture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			for _, key := range []string{"GITLAB_DUO_NAMESPACE_ID", "GITLAB_DUO_PROJECT_ID", "GITLAB_DUO_PROJECT_PATH"} {
				t.Setenv(key, scenario.Env[key])
			}
			config := func(run discoveryRun) (DiscoveryConfig, *replay) {
				transport := &replay{t: t, run: run}
				return DiscoveryConfig{APIKey: capture.APIKey, BaseURL: "https://gitlab.com/", ProjectPath: scenario.Config["projectPath"], Client: &http.Client{Transport: transport}}, transport
			}

			modelsConfig, transport := config(scenario.Models)
			models, err := FetchModels(context.Background(), modelsConfig)
			transport.done()
			assertDiscoveryError(t, "models", err, scenario.Models.Error)
			if err == nil {
				var want []struct {
					ID              string `json:"id"`
					Name            string `json:"name"`
					BaseURL         string `json:"baseUrl"`
					ContextWindow   int    `json:"contextWindow"`
					RootNamespaceID string `json:"gitlabDuoWorkflowRootNamespaceId"`
				}
				_ = json.Unmarshal(scenario.Models.Result, &want)
				if len(models) != len(want) {
					t.Fatalf("models = %+v, NeoPi listed %s", models, scenario.Models.Result)
				}
				for i, row := range want {
					if got := models[i]; got != (Model{ID: row.ID, Name: row.Name, BaseURL: row.BaseURL, ContextWindow: row.ContextWindow, RootNamespaceID: row.RootNamespaceID}) {
						t.Fatalf("model %d = %+v, NeoPi listed %+v", i, got, row)
					}
				}
			}

			runtimeConfig, transport := config(scenario.Runtime)
			selection, err := DiscoverRuntimeNamespace(context.Background(), runtimeConfig)
			transport.done()
			assertDiscoveryError(t, "runtime", err, scenario.Runtime.Error)
			if err == nil {
				var want struct {
					RootNamespaceID string `json:"rootNamespaceId"`
					NamespacePath   string `json:"namespacePath"`
					ProjectPath     string `json:"projectPath"`
					Source          string `json:"source"`
				}
				_ = json.Unmarshal(scenario.Runtime.Result, &want)
				if selection != (NamespaceSelection{RootNamespaceID: want.RootNamespaceID, NamespacePath: want.NamespacePath, ProjectPath: want.ProjectPath, Source: want.Source}) {
					t.Fatalf("runtime namespace = %+v, NeoPi chose %s", selection, scenario.Runtime.Result)
				}
			}
		})
	}
}

func assertDiscoveryError(t *testing.T, label string, err error, want *string) {
	t.Helper()
	switch {
	case want == nil && err != nil:
		t.Fatalf("%s failed: %v", label, err)
	case want != nil && (err == nil || err.Error() != *want):
		t.Fatalf("%s error = %v, NeoPi failed with %q", label, err, *want)
	}
}
