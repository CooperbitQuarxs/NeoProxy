package cliproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// The real discovery fetch for an Ollama account sends NeoPi's runtime
// requests over HTTP: GET /api/tags with no headers of its own and one POST
// /api/show per entry with only a JSON Content-Type. Neither carries the
// account's key, which NeoPi's implicit provider never puts on discovery.
func TestNeoPiOllamaDiscoveryRequestsOverHTTP(t *testing.T) {
	raw, err := os.ReadFile("../../internal/registry/testdata/neopi_ollama_discovery.json.gz")
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
		Results []struct {
			Scenario string                        `json:"scenario"`
			Tags     neoPiCapturedReply            `json:"tags"`
			Shows    map[string]neoPiCapturedReply `json:"shows"`
			Requests []struct {
				Method  string            `json:"method"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"requests"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	result := capture.Results[0]
	if result.Scenario != "roster with show metadata" {
		t.Fatalf("first scenario is %q", result.Scenario)
	}
	type request struct{ method, path, body, contentType, authorization, accept string }
	var mu sync.Mutex
	var got []request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, request{r.Method, r.URL.Path, string(body), r.Header.Get("Content-Type"), r.Header.Get("Authorization"), r.Header.Get("Accept")})
		mu.Unlock()
		reply := neoPiCapturedReply{Status: http.StatusNotFound, Body: "not found"}
		switch r.URL.Path {
		case "/api/tags":
			reply = result.Tags
		case "/api/show":
			var show struct {
				Model any `json:"model"`
			}
			_ = json.Unmarshal(body, &show)
			if answer, ok := result.Shows[fmt.Sprint(show.Model)]; ok {
				reply = answer
			}
		}
		w.WriteHeader(reply.Status)
		_, _ = w.Write([]byte(reply.Body))
	}))
	defer server.Close()
	auth := &coreauth.Auth{ID: "ollama", Provider: registry.NeoPiOllamaProvider, Attributes: map[string]string{
		"api_key": "sk-ollama-key", "base_url": server.URL + "/v1",
	}}
	models, err := fetchNeoPiCompletionsModels(registry.NeoPiOllamaProvider)(context.Background(), &config.Config{}, auth)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		t.Fatal("no models discovered")
	}
	var want []request
	for _, captured := range result.Requests {
		path := "/api/tags"
		if captured.Method == http.MethodPost {
			path = "/api/show"
		}
		want = append(want, request{captured.Method, path, captured.Body, captured.Headers["content-type"], captured.Headers["authorization"], captured.Headers["accept"]})
	}
	byKey := func(requests []request) {
		sort.Slice(requests, func(i, j int) bool {
			return requests[i].method+requests[i].path+requests[i].body < requests[j].method+requests[j].path+requests[j].body
		})
	}
	byKey(got)
	byKey(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("requests:\n port  %+v\n NeoPi %+v", got, want)
	}
}
