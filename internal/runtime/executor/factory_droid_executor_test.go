package executor

import (
	"bytes"
	"compress/gzip"
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

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// factoryDroidCapture is testdata/neopi_factory_droid/<wire>.json.gz: NeoPi's
// OWN streamSimple over every registry model of one wire, built for a
// signed-in account, against a loopback server, beside the downstream
// request a client sends NeoProxy for each scenario.
type factoryDroidCapture struct {
	Wire      string `json:"wire"`
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	Identity  struct {
		OrgID           string `json:"orgId"`
		Region          string `json:"region"`
		InferenceRegion string `json:"inferenceRegion"`
	} `json:"identity"`
	Fixtures []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		Method       string            `json:"method"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
	} `json:"fixtures"`
}

// Every captured NeoPi Factory request is sent byte for byte: method, path,
// headers (the per-request x-assistant-message-id is a fresh v4 UUID) and
// body, for the client format the model's wire takes.
func TestFactoryDroidReplaysNeoPiRequests(t *testing.T) {
	formats := map[string]sdktranslator.Format{
		"anthropic-messages": sdktranslator.FormatClaude,
		"openai-completions": sdktranslator.FormatOpenAI,
		"google-generate":    sdktranslator.FormatGemini,
	}
	var sent struct {
		method, path string
		headers      http.Header
		body         []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.method, sent.path, sent.headers = r.Method, r.URL.RequestURI(), r.Header.Clone()
		sent.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	for _, wire := range []string{"anthropic-messages"} {
		capture := loadFactoryDroidCapture(t, "testdata/neopi_factory_droid/"+wire+".json.gz")
		t.Run(wire, func(t *testing.T) {
			exec := NewFactoryDroidExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: "factory-droid-replay", Provider: "factory-droid", Metadata: map[string]any{
				"type": "factory-droid", "access_token": capture.APIKey, "org_id": capture.Identity.OrgID,
				"region": capture.Identity.Region, "inference_region": capture.Identity.InferenceRegion,
			}}
			scope := registry.FactoryDroidScope{Region: capture.Identity.Region, InferenceRegion: capture.Identity.InferenceRegion, OrgID: capture.Identity.OrgID}
			for _, fixture := range capture.Fixtures {
				t.Run(fixture.Name, func(t *testing.T) {
					model := registry.FactoryDroidModelFor(fixture.Model)
					if model == nil {
						t.Fatalf("no registry model %s", fixture.Model)
					}
					requested, err := url.Parse(fixture.RequestedURL)
					if err != nil {
						t.Fatal(err)
					}
					// The account's row as discovery registers it, served from
					// the capture server at the wire's path.
					row := registry.BuildFactoryDroidModel(model, scope, capture.Identity.InferenceRegion, model.Policy.Rotation)
					prefix := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(requested.Path, "/v1/messages"), "/chat/completions"), "/generate")
					row.BaseURL = server.URL + prefix
					var downstream bytes.Buffer
					if err := json.Compact(&downstream, fixture.Downstream); err != nil {
						t.Fatal(err)
					}
					sent.body = nil
					_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
						Model:    fixture.Model,
						Payload:  downstream.Bytes(),
						Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.FactoryDroidModelInfo(row)},
					}, cliproxyexecutor.Options{SourceFormat: formats[wire], OriginalRequest: downstream.Bytes(),
						Metadata: map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: capture.SessionID}})
					if sent.body == nil {
						t.Fatal("executor sent no request")
					}
					if sent.method != fixture.Method {
						t.Errorf("method = %s, want %s", sent.method, fixture.Method)
					}
					if sent.path != requested.RequestURI() {
						t.Errorf("path = %s, want %s", sent.path, requested.RequestURI())
					}
					if string(sent.body) != fixture.RawBody {
						t.Errorf("body:\n got %s\nwant %s", sent.body, fixture.RawBody)
					}
					want := map[string]string{}
					for name, value := range fixture.Headers {
						if name != "host" && name != "content-length" {
							want[name] = value
						}
					}
					got := map[string]string{}
					for name, values := range sent.headers {
						if name != "Content-Length" {
							got[strings.ToLower(name)] = strings.Join(values, ", ")
						}
					}
					for _, headers := range []map[string]string{got, want} {
						if _, err := uuid.Parse(headers["x-assistant-message-id"]); err == nil {
							headers["x-assistant-message-id"] = "<uuid>"
						}
					}
					if !reflect.DeepEqual(got, want) {
						t.Errorf("headers:\n got %v\nwant %v", got, want)
					}
				})
			}
		})
	}
}

func loadFactoryDroidCapture(t *testing.T, path string) factoryDroidCapture {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var capture factoryDroidCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatalf("%s holds no fixtures", path)
	}
	return capture
}
