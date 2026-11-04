package codex

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestCodexNeoPiModelsCaptureParity(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_models_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Scenarios []struct {
			Name      string `json:"name"`
			AccountID string `json:"accountId"`
			Responses []struct {
				Status int             `json:"status"`
				ETag   string          `json:"etag"`
				Raw    string          `json:"raw"`
				Body   json.RawMessage `json:"body"`
			} `json:"responses"`
			Requests     []neopiOAuthRequest `json:"requests"`
			Result       json.RawMessage     `json:"result"`
			Materialized []struct {
				registry.CodexModel
				WebSearch string `json:"webSearch"`
			} `json:"materialized"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range fixture.Scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			captured := make(chan neopiOAuthRequest, len(scenario.Responses)+1)
			index := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
				}
				headers := map[string]string{"host": r.Host}
				for name, values := range r.Header {
					headers[strings.ToLower(name)] = strings.Join(values, ", ")
				}
				captured <- neopiOAuthRequest{Method: r.Method, URL: "https://" + r.Host + r.URL.RequestURI(), Headers: headers, Body: string(body)}
				if index >= len(scenario.Responses) {
					t.Error("unexpected model discovery request")
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				response := scenario.Responses[index]
				index++
				if response.ETag != "" {
					w.Header().Set("ETag", response.ETag)
				}
				status := response.Status
				if status == 0 {
					status = http.StatusOK
				}
				w.WriteHeader(status)
				if response.Raw != "" {
					_, _ = io.WriteString(w, response.Raw)
				} else {
					_, _ = w.Write(response.Body)
				}
			}))
			defer server.Close()
			target, _ := url.Parse(server.URL)
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				clone := r.Clone(r.Context())
				clone.Host = r.URL.Host
				clone.URL.Scheme, clone.URL.Host = target.Scheme, target.Host
				return http.DefaultTransport.RoundTrip(clone)
			})}
			catalog, errFetch := FetchModels(context.Background(), client, ModelDiscoveryOptions{AccessToken: "discovery-access-fake", AccountID: scenario.AccountID})
			close(captured)
			var requests []neopiOAuthRequest
			for request := range captured {
				requests = append(requests, request)
			}
			if !reflect.DeepEqual(requests, scenario.Requests) {
				t.Errorf("model requests differ\ngot  %#v\nwant %#v", requests, scenario.Requests)
			}
			if string(scenario.Result) == "null" {
				if catalog != nil || errFetch == nil {
					t.Fatalf("failed discovery must remain unavailable, got %#v, %v", catalog, errFetch)
				}
				return
			}
			if errFetch != nil {
				t.Fatal(errFetch)
			}
			actualJSON, errJSON := json.Marshal(catalog)
			if errJSON != nil {
				t.Fatal(errJSON)
			}
			var actual, expected any
			if errJSON = json.Unmarshal(actualJSON, &actual); errJSON != nil {
				t.Fatal(errJSON)
			}
			if errJSON = json.Unmarshal(scenario.Result, &expected); errJSON != nil {
				t.Fatal(errJSON)
			}
			if !reflect.DeepEqual(actual, expected) {
				t.Errorf("normalized discovery differs\ngot  %s\nwant %s", actualJSON, scenario.Result)
			}
			for i, model := range catalog.Models {
				if i >= len(scenario.Materialized) {
					t.Fatal("missing source materialization")
				}
				// Compare every field represented in the runtime metadata, including
				// the source-derived policies consumed by request transformation.
				info := model.ModelInfo()
				got := info.Codex
				want := &scenario.Materialized[i].CodexModel
				if info.NativeCapabilities == nil || info.NativeCapabilities.WebSearch == nil || *info.NativeCapabilities.WebSearch != (scenario.Materialized[i].WebSearch == "codex") {
					t.Errorf("materialized %s lost its source web-search capability", model.ID)
				}
				if !reflect.DeepEqual(got, want) {
					gotJSON, _ := json.Marshal(got)
					wantJSON, _ := json.Marshal(want)
					t.Errorf("materialized %s differs\ngot  %s\nwant %s", model.ID, gotJSON, wantJSON)
				}
			}
		})
	}
}
