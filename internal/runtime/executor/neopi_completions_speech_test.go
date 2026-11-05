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
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type neoPiSpeechFixture struct {
	Name     string         `json:"name"`
	Provider string         `json:"provider"`
	Model    string         `json:"model"`
	APIKey   string         `json:"apiKey"`
	Request  map[string]any `json:"request"`
	Reply    struct {
		Status      int    `json:"status"`
		Body        string `json:"body"`
		ContentType string `json:"contentType"`
		RetryAfter  string `json:"retryAfter"`
	} `json:"reply"`
	Requests []struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	} `json:"requests"`
	Error    string `json:"error"`
	MimeType string `json:"mimeType"`
	Audio    string `json:"audio"`
}

// TestNeoPiSpeechRequestParity replays NeoPi's own synthesizeSpeech captures
// as OpenAI speech requests: the upstream request (URL path, body, headers),
// the audio and MIME type returned, and NeoPi's error for a failed request.
func TestNeoPiSpeechRequestParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/neopi_speech.fixtures.json.gz")
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
		Fixtures []neoPiSpeechFixture `json:"fixtures"`
	}
	if err = json.Unmarshal(data, &capture); err != nil || len(capture.Fixtures) == 0 {
		t.Fatalf("no speech captures: %v", err)
	}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			row := registry.NeoPiCompletionsModel(fixture.Provider, fixture.Model)
			if row == nil || row.API != "openai-speech" {
				t.Fatalf("no speech row for %s/%s", fixture.Provider, fixture.Model)
			}
			var path, body string
			var headers http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				received, _ := io.ReadAll(r.Body)
				path, body, headers = r.URL.Path, string(received), r.Header.Clone()
				w.Header().Set("Content-Type", fixture.Reply.ContentType)
				if fixture.Reply.RetryAfter != "" {
					w.Header().Set("Retry-After", fixture.Reply.RetryAfter)
				}
				w.WriteHeader(fixture.Reply.Status)
				_, _ = io.WriteString(w, fixture.Reply.Body)
			}))
			defer server.Close()
			base, err := url.Parse(row.BaseURL)
			if err != nil {
				t.Fatal(err)
			}
			served := row.Clone()
			served.BaseURL = server.URL + base.Path
			exec := NewNeoPiCompletionsExecutor(fixture.Provider, nil, NeoPiCompletionsProfile{Row: func(string, string) *registry.NeoPiModel { return served.Clone() }})
			client := map[string]any{"model": fixture.Model, "input": fixture.Request["text"], "response_format": fixture.Request["format"]}
			for from, to := range map[string]string{"voice": "voice", "speed": "speed", "instructions": "instructions"} {
				if value, ok := fixture.Request[from]; ok {
					client[to] = value
				}
			}
			payload, _ := json.Marshal(client)
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": fixture.APIKey}}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.Format(xaiSpeechHandlerType)}
			resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: fixture.Model, Payload: payload}, opts)
			if fixture.Error != "" {
				if err == nil || err.Error() != strings.TrimPrefix(fixture.Error, "SpeechApiError: ") {
					t.Errorf("error = %v, NeoPi %s", err, fixture.Error)
				}
				// The reply's Retry-After reaches account scheduling.
				if fixture.Reply.RetryAfter != "" {
					hinted, ok := err.(interface{ RetryAfter() *time.Duration })
					if !ok || hinted.RetryAfter() == nil || *hinted.RetryAfter() != 75*time.Second {
						t.Errorf("error %T carries no 75s retry hint", err)
					}
				}
			} else if err != nil {
				t.Fatal(err)
			} else if string(resp.Payload) != fixture.Audio || resp.Headers.Get("Content-Type") != fixture.MimeType {
				t.Errorf("audio %q (%s), NeoPi %q (%s)", resp.Payload, resp.Headers.Get("Content-Type"), fixture.Audio, fixture.MimeType)
			}
			want := fixture.Requests[0]
			wantURL, _ := url.Parse(want.URL)
			if path != wantURL.Path {
				t.Errorf("path = %s, NeoPi %s", path, wantURL.Path)
			}
			if body != want.Body {
				t.Errorf("body:\n got %s\nwant %s", body, want.Body)
			}
			for _, name := range []string{"authorization", "content-type", "user-agent"} {
				if value := headers.Get(name); value != want.Headers[name] {
					t.Errorf("header %s = %q, NeoPi %q", name, value, want.Headers[name])
				}
			}
		})
	}
}

// A speech request is read with NeoPi's speech-server contract: malformed
// fields are rejected rather than coerced or dropped, and the payload is
// written as JSON.stringify writes it.
func TestNeoPiSpeechPayloadContract(t *testing.T) {
	for _, bad := range []string{
		`{"model":"m"}`,
		`{"model":"m","input":""}`,
		`{"model":"m","input":123}`,
		`{"model":"m","input":"hi","voice":123}`,
		`{"model":"m","input":"hi","voice":""}`,
		`{"model":"m","input":"hi","speed":"1.5"}`,
		`{"model":"m","input":"hi","instructions":123}`,
		`{"model":"m","input":"hi","response_format":"ogg"}`,
		`{"model":"m","input":"hi","response_format":null}`,
		`[1]`,
	} {
		if payload, err := neoPiSpeechPayload("m", []byte(bad)); err == nil {
			t.Errorf("%s accepted as %s", bad, payload)
		}
	}
	payload, err := neoPiSpeechPayload("m", []byte(`{"model":"m","input":"a\u2028b","speed":-0,"instructions":""}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\"model\":\"m\",\"input\":\"a\u2028b\",\"response_format\":\"mp3\",\"speed\":0,\"instructions\":\"\"}"; string(payload) != want {
		t.Errorf("payload = %s, want %s", payload, want)
	}
}

// A payload rule that changes the format changes the audio's MIME type.
func TestNeoPiSpeechMimeFollowsPayloadRules(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "RIFF")
	}))
	defer server.Close()
	row := &registry.NeoPiModel{ID: "hexgrad/Kokoro-82M", API: "openai-speech", Kind: "tts", Provider: "deepinfra", BaseURL: server.URL}
	rules := config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: row.ID}}, Params: map[string]any{"response_format": "wav"}}},
	}
	exec := NewNeoPiCompletionsExecutor("deepinfra", &config.Config{Payload: rules}, NeoPiCompletionsProfile{Row: func(string, string) *registry.NeoPiModel { return row.Clone() }})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.Format(xaiSpeechHandlerType)}
	resp, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: row.ID, Payload: []byte(`{"model":"hexgrad/Kokoro-82M","input":"Hi."}`)}, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := resp.Headers.Get("Content-Type"); got != "audio/wav" {
		t.Errorf("content type = %q, want audio/wav", got)
	}
}

// User payload rules apply to the finished speech payload, after NeoPi's own
// shaping, and nothing restores what they set.
func TestNeoPiSpeechPayloadRulesApplyLast(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ := io.ReadAll(r.Body)
		body = string(received)
		_, _ = io.WriteString(w, "audio")
	}))
	defer server.Close()
	row := &registry.NeoPiModel{ID: "hexgrad/Kokoro-82M", API: "openai-speech", Kind: "tts", Provider: "deepinfra", BaseURL: server.URL + "/v1/openai"}
	rules := config.PayloadConfig{
		Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: row.ID}}, Params: map[string]any{"voice": "af_sky"}}},
	}
	exec := NewNeoPiCompletionsExecutor("deepinfra", &config.Config{Payload: rules}, NeoPiCompletionsProfile{Row: func(string, string) *registry.NeoPiModel { return row.Clone() }})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key"}}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.Format(xaiSpeechHandlerType)}
	if _, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: row.ID, Payload: []byte(`{"model":"hexgrad/Kokoro-82M","input":"Hi.","voice":"af_bella"}`)}, opts); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"voice":"af_sky"`) {
		t.Errorf("upstream body %s lacks the configured voice", body)
	}
}
