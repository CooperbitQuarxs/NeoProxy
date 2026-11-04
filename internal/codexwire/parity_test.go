package codexwire

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	codexauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

type sourceCapture struct {
	InstallID string          `json:"installId"`
	AccountID string          `json:"accountId"`
	Token     string          `json:"token"`
	SessionID string          `json:"sessionId"`
	Discovery json.RawMessage `json:"discovery"`
	Fixtures  []struct {
		Name         string `json:"name"`
		Transport    string `json:"transport"`
		BaseURL      string `json:"baseUrl"`
		RequestedURL string `json:"requestedURL"`
		Scenario     struct {
			Downstream json.RawMessage `json:"downstream"`
		} `json:"scenario"`
		Requests []struct {
			Path    string            `json:"path"`
			Headers map[string]string `json:"headers"`
			RawBody string            `json:"rawBody"`
		} `json:"requests"`
	} `json:"fixtures"`
}

var (
	uuidPattern      = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	turnStartPattern = regexp.MustCompile(`(turn_started_at_unix_ms\\?":)\d+`)
)

// normalizeRandom replaces only random-by-design identity (per-session thread,
// window and turn UUIDs) and the Date.now() turn start; the installation ID is kept.
func normalizeRandom(text, installID string) string {
	ids := map[string]string{installID: installID}
	text = uuidPattern.ReplaceAllStringFunc(text, func(id string) string {
		if _, ok := ids[id]; !ok {
			ids[id] = "<uuid-" + string(rune('a'+len(ids)-1)) + ">"
		}
		return ids[id]
	})
	return turnStartPattern.ReplaceAllString(text, "${1}<now>")
}

func loadSourceCapture(t *testing.T) (sourceCapture, registry.CodexModel) {
	t.Helper()
	raw, err := os.ReadFile("../runtime/executor/testdata/codex_npi_requests.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture sourceCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	// The model comes from NeoProxy's own discovery of the same roster entry.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(capture.Discovery)
	}))
	defer server.Close()
	catalog, err := codexauth.FetchModels(context.Background(), server.Client(), codexauth.ModelDiscoveryOptions{AccessToken: capture.Token, AccountID: capture.AccountID, BaseURL: server.URL, ClientVersion: ClientVersion})
	if err != nil || len(catalog.Models) != 1 {
		t.Fatalf("discovery: %v", err)
	}
	// Discovery rows resolve through the catalog as NeoPi's buildModel does.
	return capture, *catalog.Models[0].ModelInfo().Codex
}

func TestShapeMatchesNeoPiSource(t *testing.T) {
	capture, model := loadSourceCapture(t)
	framing := map[string]bool{"host": true, "content-length": true, "accept-encoding": true, "connection": true, "content-encoding": true}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			ctx, opts := FromResponses(fixture.Scenario.Downstream, &model)
			opts.SessionID = capture.SessionID
			request, err := NewSessions().Shape(&model, ctx, opts, Call{AccessToken: capture.Token, BaseURL: fixture.BaseURL, InstallID: capture.InstallID, Transport: Transport(fixture.Transport)})
			if err != nil {
				t.Fatal(err)
			}
			want := fixture.Requests[0]
			if request.URL != fixture.RequestedURL {
				t.Errorf("URL %s; source %s", request.URL, fixture.RequestedURL)
			}
			if got, source := normalizeRandom(string(request.Body), capture.InstallID), normalizeRandom(want.RawBody, capture.InstallID); got != source {
				t.Errorf("body\ngot    %s\nsource %s", got, source)
			}
			gotHeaders := map[string]string{}
			for name, values := range request.Header {
				gotHeaders[strings.ToLower(name)] = normalizeRandom(strings.Join(values, ", "), capture.InstallID)
			}
			sourceHeaders := map[string]string{}
			for name, value := range want.Headers {
				if !framing[name] {
					sourceHeaders[name] = normalizeRandom(value, capture.InstallID)
				}
			}
			if !reflect.DeepEqual(gotHeaders, sourceHeaders) {
				t.Errorf("headers\ngot    %v\nsource %v", gotHeaders, sourceHeaders)
			}
		})
	}
}
