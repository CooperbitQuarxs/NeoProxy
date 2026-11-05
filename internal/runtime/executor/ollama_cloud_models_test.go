package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// Discovery reaches /api/tags and /api/show as NeoPi's model manager does
// (Bun's fetch headers, one show call per listed entry, a configured /api
// base normalized) and resolves NeoPi's roster; a refused listing fails, so
// the bundled roster stays (../../registry/testdata/ollama_cloud_discovery.json).
func TestFetchOllamaCloudModelsMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("../../registry/testdata/ollama_cloud_discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Scenarios []struct {
			Name    string `json:"name"`
			APIKey  string `json:"apiKey"`
			BaseURL string `json:"baseUrl"`
			Tags    struct {
				Status int             `json:"status"`
				Body   json.RawMessage `json:"body"`
			} `json:"tags"`
			Shows map[string]struct {
				Status int             `json:"status"`
				Body   json.RawMessage `json:"body"`
			} `json:"shows"`
			Requests []struct {
				Method  string            `json:"method"`
				Path    string            `json:"path"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"requests"`
			Source string `json:"source"`
			Models []struct {
				ID string `json:"id"`
			} `json:"models"`
		} `json:"scenarios"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Scenarios) == 0 {
		t.Fatal("capture has no scenarios")
	}
	raw := func(body json.RawMessage) string {
		var text string
		if json.Unmarshal(body, &text) == nil {
			return text
		}
		return string(body)
	}
	for _, tc := range capture.Scenarios {
		t.Run(tc.Name, func(t *testing.T) {
			var mu sync.Mutex
			var got []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				line := r.Method + " " + r.URL.Path + " " + string(body)
				for _, name := range []string{"accept", "accept-encoding", "authorization", "connection", "content-type", "user-agent"} {
					line += " | " + name + "=" + r.Header.Get(name)
				}
				mu.Lock()
				got = append(got, line)
				mu.Unlock()
				status, reply := http.StatusNotFound, "not found"
				switch r.URL.Path {
				case "/api/tags":
					status, reply = tc.Tags.Status, raw(tc.Tags.Body)
				case "/api/show":
					if show, ok := tc.Shows[gjson.GetBytes(body, "model").String()]; ok {
						status, reply = show.Status, raw(show.Body)
					}
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, reply)
			}))
			defer server.Close()
			previousWait := ollamaCloudDiscoveryWait
			ollamaCloudDiscoveryWait = func(context.Context, time.Duration) error { return nil }
			defer func() { ollamaCloudDiscoveryWait = previousWait }()
			base := server.URL
			if tc.BaseURL != "" {
				base = strings.Replace(tc.BaseURL, "https://ollama.com", server.URL, 1)
			}
			auth := &cliproxyauth.Auth{ID: "ollama-cloud-discovery", Provider: "ollama-cloud", Attributes: map[string]string{"api_key": tc.APIKey, "base_url": base}}
			models, errFetch := FetchOllamaCloudModels(context.Background(), &config.Config{}, auth)
			var empty *registry.NeoPiEmptyListing
			if errors.As(errFetch, &empty) {
				models, errFetch = empty.Models, nil
			}
			var want []string
			for _, request := range tc.Requests {
				line := request.Method + " " + request.Path + " " + request.Body
				for _, name := range []string{"accept", "accept-encoding", "authorization", "connection", "content-type", "user-agent"} {
					line += " | " + name + "=" + request.Headers[name]
				}
				want = append(want, line)
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("requests:\n got %q\nwant %q", got, want)
			}
			if tc.Source == "bundled" {
				if errFetch == nil {
					t.Fatal("a refused listing succeeded")
				}
				return
			}
			if errFetch != nil {
				t.Fatal(errFetch)
			}
			ids := make([]string, len(models))
			for i, model := range models {
				ids[i] = model.ID
			}
			wantIDs := make([]string, len(tc.Models))
			for i, model := range tc.Models {
				wantIDs[i] = model.ID
			}
			if !slices.Equal(ids, wantIDs) {
				t.Errorf("roster:\n got %q\nwant %q", ids, wantIDs)
			}
		})
	}
}
