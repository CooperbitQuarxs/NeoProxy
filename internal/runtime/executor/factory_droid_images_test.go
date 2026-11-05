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
	"github.com/tidwall/gjson"
)

// factoryDroidImageCapture is testdata/neopi_factory_droid/images.json.gz.
type factoryDroidImageCapture struct {
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
		Endpoint     string            `json:"endpoint"`
		Downstream   json.RawMessage   `json:"downstream"`
		Images       []string          `json:"images"`
		RequestedURL string            `json:"requestedURL"`
		Method       string            `json:"method"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
	} `json:"fixtures"`
}

func factoryDroidImageAuth(token, orgID, region, inferenceRegion string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: "factory-droid-images", Provider: "factory-droid", Metadata: map[string]any{
		"type": "factory-droid", "access_token": token, "org_id": orgID, "region": region, "inference_region": inferenceRegion,
	}}
}

func factoryDroidImageOptions(endpoint, session string, payload []byte) cliproxyexecutor.Options {
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString(openAICompatImageHandlerType), OriginalRequest: payload,
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
		Metadata: map[string]any{cliproxyexecutor.RequestPathMetadataKey: endpoint, cliproxyexecutor.ExecutionSessionMetadataKey: session}}
}

// TestFactoryDroidImagesReplayNeoPiRequests replays the OpenAI Images
// requests behind NeoPi's own generateFactoryDroidImage captures: the
// upstream request must match byte for byte, and the reply must carry the
// images NeoPi decoded from the same upstream answer.
func TestFactoryDroidImagesReplayNeoPiRequests(t *testing.T) {
	file, err := os.Open("testdata/neopi_factory_droid/images.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var capture factoryDroidImageCapture
	if err = json.NewDecoder(reader).Decode(&capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatal("image capture holds no fixtures")
	}
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
	var sent struct {
		method, path string
		headers      http.Header
		body         []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.method, sent.path, sent.headers = r.Method, r.URL.RequestURI(), r.Header.Clone()
		sent.body, _ = io.ReadAll(r.Body)
		// The answers the capture server gave NeoPi.
		if strings.HasSuffix(r.URL.Path, "/generate") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Here it is."}]}}]}`+"\n\n"+
				`data: {"candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"`+png+`"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":11,"totalTokenCount":18}}`+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"created":1,"data":[{"b64_json":"`+png+`"}],"usage":{"input_tokens":7,"output_tokens":11}}`)
	}))
	defer server.Close()
	exec := NewFactoryDroidExecutor(&config.Config{})
	auth := factoryDroidImageAuth(capture.APIKey, capture.Identity.OrgID, capture.Identity.Region, capture.Identity.InferenceRegion)
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
			row := registry.BuildFactoryDroidModel(model, scope, capture.Identity.InferenceRegion, model.Policy.Rotation)
			prefix := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(requested.Path, "/images/generations"), "/images/edits"), "/generate")
			row.BaseURL = server.URL + prefix
			var downstream bytes.Buffer
			if err = json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			sent.body = nil
			response, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:    fixture.Model,
				Payload:  downstream.Bytes(),
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.FactoryDroidModelInfo(row)},
			}, factoryDroidImageOptions(fixture.Endpoint, capture.SessionID, downstream.Bytes()))
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if sent.method != fixture.Method || sent.path != requested.RequestURI() {
				t.Errorf("request = %s %s, want %s %s", sent.method, sent.path, fixture.Method, requested.RequestURI())
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
				if _, errParse := uuid.Parse(headers["x-assistant-message-id"]); errParse == nil {
					headers["x-assistant-message-id"] = "<uuid>"
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("headers:\n got %v\nwant %v", got, want)
			}
			var images []string
			for _, item := range gjson.GetBytes(response.Payload, "data").Array() {
				images = append(images, item.Get("b64_json").String())
			}
			if !reflect.DeepEqual(images, fixture.Images) {
				t.Errorf("images = %v, want NeoPi's %v", images, fixture.Images)
			}
		})
	}
}

// TestFactoryDroidImageFailures pins the answers NeoPi's transport gives
// instead of an image: rejected references before any request, and a 200
// without an image as an error rather than an empty success.
func TestFactoryDroidImageFailures(t *testing.T) {
	const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="
	ref := `"data:image/png;base64,` + png + `"`
	many := strings.TrimSuffix(strings.Repeat(ref+",", 17), ",")
	oversize := `"data:image/png;base64,` + strings.Repeat("A", 5*1024*1024*4/3+8) + `"`
	cases := []struct {
		name, model, endpoint, payload string
		stream                         bool
		reply, contentType             string
		status                         int
		message                        string
		upstream                       bool
	}{
		{name: "gemini text only", model: "gemini-3-pro-image-preview", endpoint: "/v1/images/generations", payload: `{"prompt":"A red square."}`,
			reply: `data: {"candidates":[{"content":{"parts":[{"text":"I cannot draw that."}]},"finishReason":"STOP"}]}` + "\n\n", contentType: "text/event-stream",
			status: http.StatusBadGateway, message: "factory-droid/gemini-3-pro-image-preview returned no image: the model answered with text only: I cannot draw that.", upstream: true},
		{name: "gemini prompt block", model: "gemini-3-pro-image-preview", endpoint: "/v1/images/generations", payload: `{"prompt":"A red square."}`,
			reply: `data: {"promptFeedback":{"blockReason":"SAFETY"}}` + "\n\n", contentType: "text/event-stream",
			status: http.StatusBadGateway, message: "returned no image: the prompt was blocked (SAFETY)", upstream: true},
		{name: "gemini stopped early", model: "gemini-3-pro-image-preview", endpoint: "/v1/images/generations", payload: `{"prompt":"A red square."}`,
			reply: `data: {"candidates":[{"content":{"parts":[]},"finishReason":"IMAGE_SAFETY"}]}` + "\n\n", contentType: "text/event-stream",
			status: http.StatusBadGateway, message: "Factory Gemini image generation stopped with IMAGE_SAFETY", upstream: true},
		{name: "gemini truncated", model: "gemini-3-pro-image-preview", endpoint: "/v1/images/generations", payload: `{"prompt":"A red square."}`,
			reply: `data: {"candidates":[{"content":{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + png + `"}}]}}]}` + "\n\n", contentType: "text/event-stream",
			status: http.StatusBadGateway, message: "Factory Gemini image stream ended before a finish reason", upstream: true},
		{name: "images empty data", model: "gpt-image-2.5-sunburst", endpoint: "/v1/images/generations", payload: `{"prompt":"A red square."}`,
			reply: `{"created":1,"data":[]}`, contentType: "application/json",
			status: http.StatusBadGateway, message: "factory-droid/gpt-image-2.5-sunburst returned no image: the response carried no image", upstream: true},
		{name: "upstream rejection", model: "gpt-image-2.5-sunburst", endpoint: "/v1/images/generations", payload: `{"prompt":"A red square."}`,
			reply: `{"error":{"message":"Image generation is disabled for this organization."}}`, contentType: "application/json",
			status: http.StatusForbidden, message: "factory-droid/gpt-image-2.5-sunburst image request failed (403): Image generation is disabled for this organization.", upstream: true},
		{name: "streamed", model: "gpt-image-2.5-sunburst", endpoint: "/v1/images/generations", payload: `{"prompt":"A red square.","stream":true}`, stream: true,
			status: http.StatusBadRequest, message: "image generation does not stream"},
		{name: "remote reference", model: "gpt-image-2.5-sunburst", endpoint: "/v1/images/edits", payload: `{"prompt":"Blue.","images":["https://example.com/a.png"]}`,
			status: http.StatusBadRequest, message: "reference image 1 must be an inline base64 data URL"},
		{name: "too many edit references", model: "gpt-image-2.5-sunburst", endpoint: "/v1/images/edits", payload: `{"prompt":"Blue.","images":[` + many + `]}`,
			status: http.StatusBadRequest, message: "factory-droid image edits accept up to 16 reference images; got 17"},
		{name: "oversize reference", model: "gemini-3-pro-image-preview", endpoint: "/v1/images/edits", payload: `{"prompt":"Blue.","images":[` + oversize + `]}`,
			status: http.StatusBadRequest, message: "the limit is 5242880 (5 MiB)"},
		{name: "unsupported edit type", model: "gpt-image-2.5-sunburst", endpoint: "/v1/images/edits", payload: `{"prompt":"Blue.","images":["data:image/gif;base64,R0lGODlhAQABAAAAACw="]}`,
			status: http.StatusBadRequest, message: "image edits accept image/png, image/jpeg, image/webp references; got image/gif"},
		{name: "chat model", model: "claude-opus-5-5", endpoint: "/v1/images/generations", payload: `{"prompt":"A red square."}`,
			status: http.StatusBadRequest, message: "is not an image generation model"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits++
				w.Header().Set("Content-Type", tc.contentType)
				if tc.status != http.StatusBadGateway {
					w.WriteHeader(tc.status)
				}
				_, _ = io.WriteString(w, tc.reply)
			}))
			defer server.Close()
			model := registry.FactoryDroidModelFor(tc.model)
			if model == nil {
				t.Fatalf("no registry model %s", tc.model)
			}
			scope := registry.FactoryDroidScope{Region: "global", InferenceRegion: "global", OrgID: "org-fixture"}
			row := registry.BuildFactoryDroidModel(model, scope, "global", model.Policy.Rotation)
			row.BaseURL = server.URL
			req := cliproxyexecutor.Request{Model: tc.model, Payload: []byte(tc.payload),
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": registry.FactoryDroidModelInfo(row)}}
			exec := NewFactoryDroidExecutor(&config.Config{})
			auth := factoryDroidImageAuth("token", "org-fixture", "global", "global")
			opts := factoryDroidImageOptions(tc.endpoint, "session", []byte(tc.payload))
			if tc.stream {
				_, err := exec.ExecuteStream(context.Background(), auth, req, opts)
				factoryDroidAssertStatus(t, err, tc.status, tc.message)
			} else {
				_, err := exec.Execute(context.Background(), auth, req, opts)
				factoryDroidAssertStatus(t, err, tc.status, tc.message)
			}
			if upstream := hits > 0; upstream != tc.upstream {
				t.Errorf("reached upstream = %v, want %v", upstream, tc.upstream)
			}
		})
	}
}

func factoryDroidAssertStatus(t *testing.T, err error, status int, message string) {
	t.Helper()
	if err == nil {
		t.Fatal("request succeeded")
	}
	coded, ok := err.(interface{ StatusCode() int })
	if !ok || coded.StatusCode() != status {
		t.Errorf("error %T %v, want status %d", err, err, status)
	}
	if !strings.Contains(err.Error(), message) {
		t.Errorf("error %q, want it to contain %q", err.Error(), message)
	}
}
