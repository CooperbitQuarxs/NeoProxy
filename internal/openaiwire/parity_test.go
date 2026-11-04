package openaiwire

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

type xaiSourceCapture struct {
	Token     string `json:"token"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name         string `json:"name"`
		BaseURL      string `json:"baseUrl"`
		RequestedURL string `json:"requestedURL"`
		Scenario     struct {
			Model      string          `json:"model"`
			Provider   string          `json:"provider"`
			Downstream json.RawMessage `json:"downstream"`
		} `json:"scenario"`
		Requests []struct {
			Headers map[string]string `json:"headers"`
			RawBody string            `json:"rawBody"`
		} `json:"requests"`
	} `json:"fixtures"`
}

// TestShapeResponsesMatchesNeoPiXaiSource pins the xai-oauth request against
// NeoPi's own streamOpenAIResponses (testdata/capture_xai_npi.ts): URL,
// headers and body bytes for each downstream scenario.
func TestShapeResponsesMatchesNeoPiXaiSource(t *testing.T) {
	raw, err := os.ReadFile("../runtime/executor/testdata/xai_npi_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture xaiSourceCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	catalog := map[string]*registry.NeoPiModel{}
	for _, info := range registry.XaiOAuthModels(nil) {
		catalog[info.ID] = info.NeoPi
	}
	framing := map[string]bool{"host": true, "content-length": true, "accept-encoding": true, "connection": true}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			model := catalog[fixture.Scenario.Model].Clone()
			if fixture.Scenario.Provider == "xai" {
				model = registry.XaiAPIKeyNeoPiModel(fixture.Scenario.Model)
			}
			if model == nil {
				t.Fatalf("model %s missing from the catalog", fixture.Scenario.Model)
			}
			model.BaseURL = fixture.BaseURL
			ctx, opts := FromResponses(fixture.Scenario.Downstream, model)
			opts.SessionID = capture.SessionID
			request := ShapeResponses(model, ctx, opts, capture.Token)
			want := fixture.Requests[0]
			if request.URL != fixture.RequestedURL {
				t.Errorf("URL %s; source %s", request.URL, fixture.RequestedURL)
			}
			if string(request.Body) != want.RawBody {
				t.Errorf("body\ngot    %s\nsource %s", request.Body, want.RawBody)
			}
			got := map[string]string{}
			for name, values := range request.Header {
				got[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			source := map[string]string{}
			for name, value := range want.Headers {
				if !framing[name] {
					source[name] = value
				}
			}
			if !reflect.DeepEqual(got, source) {
				t.Errorf("headers\ngot    %v\nsource %v", got, source)
			}
		})
	}
}
