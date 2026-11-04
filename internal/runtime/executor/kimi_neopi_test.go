package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	kimiauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/kimi"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func kimiLiveRows(t *testing.T) map[string]gjson.Result {
	t.Helper()
	data, err := os.ReadFile("testdata/kimi_models_live.json")
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]gjson.Result{}
	for _, row := range gjson.GetBytes(data, "data").Array() {
		rows[row.Get("id").String()] = row
	}
	return rows
}

// The mapper resolves every discovered row as NeoPi does: name, limits,
// modalities, the adaptive effort ladder and the Anthropic compat.
func TestKimiModelsMatchNeoPiResolution(t *testing.T) {
	data, err := os.ReadFile("testdata/kimi_npi_profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	rows := kimiLiveRows(t)
	for _, want := range gjson.GetBytes(data, "models").Array() {
		id := want.Get("id").String()
		got := registry.KimiNeoPiModel(rows[id])
		if got.Name != want.Get("name").String() || got.ContextWindow != int(want.Get("contextWindow").Int()) || got.MaxTokens != int(want.Get("maxTokens").Int()) || got.Reasoning != want.Get("reasoning").Bool() {
			t.Errorf("%s = %+v, want %s", id, got, want.Raw)
		}
		var input []string
		_ = json.Unmarshal([]byte(want.Get("input").Raw), &input)
		if !reflect.DeepEqual(got.Input, input) || got.BaseURL != want.Get("anthropic.baseUrl").String() {
			t.Errorf("%s input/base = %v %s, want %v %s", id, got.Input, got.BaseURL, input, want.Get("anthropic.baseUrl").String())
		}
		thinking := want.Get("anthropic.thinking")
		if thinking.Type == gjson.Null != (got.Thinking == nil) {
			t.Fatalf("%s thinking = %+v, want %s", id, got.Thinking, thinking.Raw)
		}
		if got.Thinking != nil {
			var efforts []string
			_ = json.Unmarshal([]byte(thinking.Get("efforts").Raw), &efforts)
			if !reflect.DeepEqual(got.Thinking.Efforts, efforts) || got.Thinking.Mode != thinking.Get("mode").String() || got.Thinking.RequiresEffort != thinking.Get("requiresEffort").Bool() {
				t.Errorf("%s thinking = %+v, want %s", id, *got.Thinking, thinking.Raw)
			}
		}
		compat := want.Get("anthropic.anthropicCompat")
		c := got.AnthropicCompat
		for name, value := range map[string]bool{"supportsContextManagement": c.SupportsContextManagement, "supportsOutputEffort": c.SupportsOutputEffort,
			"supportsSamplingParams": c.SupportsSamplingParams, "supportsForcedToolChoice": c.SupportsForcedToolChoice, "requiresToolResultId": c.RequiresToolResultID,
			"supportsLongCacheRetention": c.SupportsLongCacheRetention, "supportsEagerToolInputStreaming": c.SupportsEagerToolInputStreaming,
			"replayUnsignedThinking": c.ReplayUnsignedThinking, "officialEndpoint": c.OfficialEndpoint, "supportsBetweenToolsThinking": c.SupportsBetweenToolsThinking,
			"supportsTurnScopedSystem": c.SupportsTurnScopedSystem, "supportsPerMessageEffort": c.SupportsPerMessageEffort, "supportsThinkingBindingControls": c.SupportsThinkingBindingControls} {
			if compat.Get(name).Bool() != value {
				t.Errorf("%s compat %s = %t, want %s", id, name, value, compat.Get(name).Raw)
			}
		}
	}
}

// Every captured NeoPi Kimi Code request is reproduced byte for byte by the
// executor (translation, thinking, payload finalization, projection, bearer,
// identity headers): path, headers (the fingerprint's host-derived values are
// this machine's) and body.
func TestKimiExecutorReplaysNeoPiRequests(t *testing.T) {
	data, err := os.ReadFile("testdata/kimi_npi_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		APIKey      string            `json:"apiKey"`
		SessionID   string            `json:"sessionId"`
		KimiHeaders map[string]string `json:"kimiHeaders"`
		Fixtures    []struct {
			Name         string            `json:"name"`
			Model        string            `json:"model"`
			RequestedURL string            `json:"requestedURL"`
			Headers      map[string]string `json:"headers"`
			RawBody      string            `json:"rawBody"`
			Downstream   json.RawMessage   `json:"downstream"`
		} `json:"fixtures"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	authDir := t.TempDir()
	if err = os.WriteFile(filepath.Join(authDir, "kimi-device-id"), []byte(capture.KimiHeaders["X-Msh-Device-Id"]+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint := kimiauth.CommonHeaders(authDir)
	var sent struct {
		path    string
		headers http.Header
		body    []byte
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent.path, sent.headers = r.URL.RequestURI(), r.Header.Clone()
		sent.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	models := map[string]*registry.ModelInfo{}
	live, err := os.ReadFile("testdata/kimi_models_live.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range registry.KimiModels(live) {
		info.NeoPi.BaseURL = server.URL
		models[info.ID] = info
	}
	exec := NewKimiExecutor(&config.Config{AuthDir: authDir})
	auth := &cliproxyauth.Auth{ID: "kimi-replay", Provider: "kimi", Metadata: map[string]any{"access_token": capture.APIKey}}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			var downstream bytes.Buffer
			if err := json.Compact(&downstream, fixture.Downstream); err != nil {
				t.Fatal(err)
			}
			sent.body = nil
			_, _ = exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:    fixture.Model,
				Payload:  downstream.Bytes(),
				Metadata: map[string]any{"cliproxy.resolved_oauth_model_info": models[fixture.Model]},
			}, cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: downstream.Bytes(),
				Headers:         http.Header{"X-Claude-Code-Session-Id": {capture.SessionID}},
			})
			if sent.body == nil {
				t.Fatal("executor sent no request")
			}
			if want := strings.TrimPrefix(fixture.RequestedURL, registry.KimiAnthropicBaseURL); sent.path != want {
				t.Errorf("path = %s, want %s", sent.path, want)
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
			for _, name := range []string{"X-Msh-Device-Name", "X-Msh-Device-Model", "X-Msh-Os-Version"} {
				want[strings.ToLower(name)] = fingerprint.Get(name)
			}
			got := map[string]string{}
			for name, values := range sent.headers {
				if name != "Content-Length" {
					got[strings.ToLower(name)] = strings.Join(values, ", ")
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("headers:\n got %v\nwant %v", got, want)
			}
		})
	}
}
