package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type neoPiImageFixture struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	APIKey   string `json:"apiKey"`
	Request  struct {
		Prompt      string `json:"prompt"`
		Count       *int   `json:"count"`
		ImageSize   string `json:"imageSize"`
		InputImages []struct {
			Data     string `json:"data"`
			MimeType string `json:"mimeType"`
		} `json:"inputImages"`
	} `json:"request"`
	Replies []struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	} `json:"replies"`
	Requests []struct {
		URL     string            `json:"url"`
		Path    string            `json:"path"`
		Method  string            `json:"method"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"requests"`
	Error string `json:"error"`
}

func loadNeoPiImageFixtures(t *testing.T) []neoPiImageFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/neopi_images.fixtures.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Fixtures []neoPiImageFixture `json:"fixtures"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	return capture.Fixtures
}

// neoPiImageRowFor is the row NeoProxy serves the fixture's model from:
// SingularityAPI's image rows come from its capabilities listing, DeepInfra's
// from its bundle.
func neoPiImageRowFor(t *testing.T, provider, model string) *registry.NeoPiModel {
	t.Helper()
	if row := registry.NeoPiCompletionsModel(provider, model); row != nil && row.API == "openai-images" {
		return row
	}
	listing := `{"data":[{"id":"` + model + `","capabilities":[{"endpoint":"/v1/images/generations"}]}]}`
	infos, err := registry.NeoPiCompletionsListing(provider, registry.NeoPiCompletionsListingBase(provider), []byte(listing))
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == model && info.Type == registry.OpenAIImageModelType {
			return info.NeoPi
		}
	}
	t.Fatalf("no image row for %s/%s", provider, model)
	return nil
}

// neoPiImageClientRequests are the OpenAI images requests a client sends for
// the fixture's NeoPi request: JSON, and for an edit also multipart.
func neoPiImageClientRequests(t *testing.T, fixture neoPiImageFixture) map[string][2]string {
	t.Helper()
	body := map[string]any{"model": fixture.Model, "prompt": fixture.Request.Prompt}
	if fixture.Request.Count != nil {
		body["n"] = *fixture.Request.Count
	}
	if fixture.Request.ImageSize != "" {
		body["size"] = fixture.Request.ImageSize
	}
	if len(fixture.Request.InputImages) == 0 {
		raw, _ := json.Marshal(body)
		return map[string][2]string{"json": {string(raw), "application/json"}}
	}
	var images []any
	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	_ = writer.WriteField("model", fixture.Model)
	_ = writer.WriteField("prompt", fixture.Request.Prompt)
	for _, image := range fixture.Request.InputImages {
		images = append(images, map[string]any{"image_url": map[string]any{"url": "data:" + image.MimeType + ";base64," + image.Data}})
		header := textproto.MIMEHeader{}
		header.Set("Content-Disposition", `form-data; name="image"; filename="image"`)
		header.Set("Content-Type", image.MimeType)
		part, _ := writer.CreatePart(header)
		data, _ := base64.StdEncoding.DecodeString(image.Data)
		_, _ = part.Write(data)
	}
	_ = writer.Close()
	body["images"] = images
	raw, _ := json.Marshal(body)
	return map[string][2]string{"json": {string(raw), "application/json"}, "multipart": {form.String(), writer.FormDataContentType()}}
}

// TestNeoPiImageRequestParity replays NeoPi's own generateImage captures: the
// upstream requests (URL path, body, headers), the edit-to-generation 404
// fallback, and the error NeoPi raises for a failed request.
func TestNeoPiImageRequestParity(t *testing.T) {
	fixtures := loadNeoPiImageFixtures(t)
	if len(fixtures) == 0 {
		t.Fatal("no image captures")
	}
	for _, fixture := range fixtures {
		row := neoPiImageRowFor(t, fixture.Provider, fixture.Model)
		for variant, client := range neoPiImageClientRequests(t, fixture) {
			t.Run(fixture.Name+"/"+variant, func(t *testing.T) {
				var got []struct {
					path, body string
					headers    http.Header
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, _ := io.ReadAll(r.Body)
					got = append(got, struct {
						path, body string
						headers    http.Header
					}{r.URL.Path, string(data), r.Header.Clone()})
					reply := fixture.Replies[min(len(got)-1, len(fixture.Replies)-1)]
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(reply.Status)
					_, _ = io.WriteString(w, reply.Body)
				}))
				defer server.Close()
				base, err := url.Parse(row.BaseURL)
				if err != nil {
					t.Fatal(err)
				}
				served := row.Clone()
				served.BaseURL = server.URL + base.Path
				profile := NeoPiCompletionsProfile{Row: func(string, string) *registry.NeoPiModel { return served.Clone() }}
				exec := NewNeoPiCompletionsExecutor(fixture.Provider, nil, profile)
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": fixture.APIKey}}
				path := "/v1/images/generations"
				if len(fixture.Request.InputImages) > 0 {
					path = "/v1/images/edits"
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.Format(openAICompatImageHandlerType),
					Headers:      http.Header{"Content-Type": {client[1]}},
					Metadata:     map[string]any{cliproxyexecutor.RequestPathMetadataKey: path},
				}
				resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: fixture.Model, Payload: []byte(client[0])}, opts)
				if fixture.Error != "" {
					if err == nil || err.Error() != strings.TrimPrefix(fixture.Error, "ImageApiError: ") {
						t.Errorf("error = %v, NeoPi %s", err, fixture.Error)
					}
				} else if err != nil {
					t.Fatal(err)
				} else if !gjson.GetBytes(resp.Payload, "data").IsArray() {
					t.Errorf("response = %s", resp.Payload)
				}
				if len(got) != len(fixture.Requests) {
					t.Fatalf("sent %d requests, NeoPi %d", len(got), len(fixture.Requests))
				}
				for i, want := range fixture.Requests {
					wantURL, _ := url.Parse(want.URL)
					if got[i].path != wantURL.Path {
						t.Errorf("request %d path = %s, NeoPi %s", i, got[i].path, wantURL.Path)
					}
					if got[i].body != want.Body {
						t.Errorf("request %d body:\n got %s\nwant %s", i, got[i].body, want.Body)
					}
					for _, name := range []string{"authorization", "content-type", "user-agent"} {
						if value := got[i].headers.Get(name); value != want.Headers[name] {
							t.Errorf("request %d header %s = %q, NeoPi %q", i, name, value, want.Headers[name])
						}
					}
				}
			})
		}
	}
}

// User payload rules apply to the finished image body, after NeoPi's own
// shaping, and nothing restores what they set.
func TestNeoPiImagePayloadRulesApplyLast(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		body = string(data)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer server.Close()
	row := &registry.NeoPiModel{ID: "flux-1-schnell", API: "openai-images", Kind: "image", Provider: "singularityapi-dev", BaseURL: server.URL + "/v1"}
	rules := config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: row.ID}}, Params: map[string]any{"response_format": "url"}}},
	}
	exec := NewNeoPiCompletionsExecutor("singularityapi-dev", &config.Config{Payload: rules}, NeoPiCompletionsProfile{Row: func(string, string) *registry.NeoPiModel { return row.Clone() }})
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.Format(openAICompatImageHandlerType),
		Headers:      http.Header{"Content-Type": {"application/json"}},
		Metadata:     map[string]any{cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations"},
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key"}}
	if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: row.ID, Payload: []byte(`{"model":"flux-1-schnell","prompt":"A cube."}`)}, opts); err != nil {
		t.Fatal(err)
	}
	if got := gjson.Get(body, "response_format").String(); got != "url" {
		t.Errorf("response_format = %q in %s, want the configured url", got, body)
	}
}
