package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// zaiCapture is testdata/zai_npi_capture.json: NeoPi's OWN streamSimple run
// over its bundled zai rows against a loopback server, one request per
// scenario, beside the downstream request a client sends NeoProxy for it.
type zaiCapture struct {
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Fixtures  []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		API          string            `json:"api"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
	} `json:"fixtures"`
}

// Every captured NeoPi request is reproduced byte for byte: URL, headers and
// body, for each thinking mode, tool, image and tool-result shape.
func TestZaiNeoPiRequestParity(t *testing.T) {
	data, err := os.ReadFile("testdata/zai_npi_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture zaiCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("capture has no fixtures")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": capture.APIKey}}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			row := registry.ZaiNeoPiModel(fixture.Model)
			if row == nil || row.API != fixture.API {
				t.Fatalf("row = %+v, want api %s", row, fixture.API)
			}
			// Translators hand the shaper compact JSON.
			var downstream bytes.Buffer
			if err = json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			var body []byte
			var headers http.Header
			var url string
			switch row.API {
			case "anthropic-messages":
				base := strings.TrimRight(row.BaseURL, "/")
				body, headers, err = projectNeoPiAnthropic(downstream.Bytes(), row.ID, base, "", neoPiWireIdentity{SessionID: capture.SessionID, APIKey: true}, neoPiThirdPartyProfile(row))
				if err == nil {
					neoPiApplyHeaders(headers, auth, nil, nil, &anthropicnpi.Credential{APIKey: capture.APIKey}, base, context.Background())
				}
				url = base + "/v1/messages"
			default:
				body, err = neoPiZaiCompletionsRequest(downstream.Bytes(), row)
				headers = neoPiZaiCompletionsHeaders(context.Background(), auth, nil, capture.APIKey)
				url = strings.TrimRight(row.BaseURL, "/") + "/chat/completions"
			}
			if err != nil {
				t.Fatal(err)
			}
			if url != fixture.RequestedURL {
				t.Errorf("url = %s, want %s", url, fixture.RequestedURL)
			}
			if string(body) != fixture.RawBody {
				t.Errorf("body:\n got %s\nwant %s", body, fixture.RawBody)
			}
			want := map[string]string{}
			for name, value := range fixture.Headers {
				if name != "host" && name != "content-length" {
					want[name] = value
				}
			}
			got := map[string]string{}
			for name, values := range headers {
				got[strings.ToLower(name)] = strings.Join(values, ", ")
			}
			for name, value := range want {
				if got[name] != value {
					t.Errorf("header %s = %q, want %q", name, got[name], value)
				}
			}
			for name := range got {
				if _, ok := want[name]; !ok {
					t.Errorf("extra header %s = %q", name, got[name])
				}
			}
		})
	}
}
