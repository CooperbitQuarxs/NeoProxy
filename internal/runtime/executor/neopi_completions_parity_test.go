package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// neoPiCompletionsCapture is one provider's testdata capture: NeoPi's OWN
// streamSimple run over its bundled rows against a loopback server, one
// request per scenario, beside the downstream body a client sends NeoProxy.
type neoPiCompletionsCapture struct {
	Provider  string `json:"provider"`
	APIKey    string `json:"apiKey"`
	SessionID string `json:"sessionId"`
	// Listing is the /models answer NeoPi's manager discovered the captured
	// rows from, for a provider NeoPi bundles no rows for.
	Listing string `json:"listing"`
	// ModelsDev is the models.dev catalog the manager hydrated the listing
	// from (SiliconFlow).
	ModelsDev string `json:"modelsDev"`
	// Env is the environment NeoPi's provider transport read while capturing
	// (Snowflake's SNOWFLAKE_ACCOUNT).
	Env map[string]string `json:"env"`
	// LmStudioV0 and LmStudioV1 are LM Studio's native metadata answers.
	LmStudioV0 string `json:"lmStudioV0"`
	LmStudioV1 string `json:"lmStudioV1"`
	Fixtures   []struct {
		Name         string            `json:"name"`
		Model        string            `json:"model"`
		RequestedURL string            `json:"requestedURL"`
		Headers      map[string]string `json:"headers"`
		RawBody      string            `json:"rawBody"`
		Downstream   json.RawMessage   `json:"downstream"`
		Error        string            `json:"error"`
	} `json:"fixtures"`
}

// TestNeoPiCompletionsRequestParity replays every captured request: URL,
// headers and body must match NeoPi's own byte for byte.
func TestNeoPiCompletionsRequestParity(t *testing.T) {
	// The captures ran without a W&B project; one here would add CoreWeave's
	// OpenAI-Project header.
	for _, name := range neoPiCoreWeaveProjectEnv {
		t.Setenv(name, "")
	}
	files, err := filepath.Glob(filepath.Join("testdata", "neopi_completions", "*.fixtures.json.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no captures: %v", err)
	}
	total := 0
	for _, file := range files {
		capture := readNeoPiCompletionsCapture(t, file)
		provider := capture.Provider
		t.Run(provider, func(t *testing.T) {
			for name, value := range capture.Env {
				t.Setenv(name, value)
			}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": capture.APIKey}}
			exec := NewNeoPiCompletionsExecutor(provider, &config.Config{}, NeoPiCompletionsProfile{})
			discovered := neoPiCapturedListingRows(t, capture)
			for _, fixture := range capture.Fixtures {
				if fixture.Error != "" {
					t.Errorf("%s: NeoPi capture failed: %s", fixture.Name, fixture.Error)
					continue
				}
				t.Run(fixture.Name, func(t *testing.T) {
					row := discovered[fixture.Model]
					if row == nil {
						row = registry.NeoPiCompletionsModel(provider, fixture.Model)
					}
					if row == nil || row.CompletionsCompat == nil {
						t.Fatalf("no NeoPi row for %s", fixture.Model)
					}
					if row.ID != fixture.Model {
						t.Fatalf("row = %s, want %s", row.ID, fixture.Model)
					}
					var downstream bytes.Buffer
					if err := json.Compact(&downstream, fixture.Downstream); err != nil {
						t.Fatal(err)
					}
					body, err := neoPiCompletionsRequest(downstream.Bytes(), row, neoPiCompletionsCall{SessionID: capture.SessionID})
					if err != nil {
						t.Fatal(err)
					}
					// The bearer and endpoint the provider's transport sends with
					// (Snowflake's account in place of the catalog placeholder,
					// Vertex's ADC token on the account's project and location).
					key, place, err := exec.credential(context.Background(), auth)
					if err != nil {
						t.Fatal(err)
					}
					url := place(exec.url(exec.accountRow(auth, row)))
					if url != fixture.RequestedURL {
						t.Errorf("url = %s, want %s", url, fixture.RequestedURL)
					}
					if string(body) != fixture.RawBody {
						t.Errorf("body:\n got %s\nwant %s", body, fixture.RawBody)
					}
					headers := exec.requestHeaders(context.Background(), auth, nil, key, row, capture.SessionID)
					want := map[string]string{}
					for name, value := range fixture.Headers {
						if name == "host" || name == "content-length" {
							continue
						}
						want[name] = value
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
					for name, value := range got {
						if _, ok := want[name]; !ok {
							t.Errorf("extra header %s = %q", name, value)
						}
					}
				})
			}
		})
		total += len(capture.Fixtures)
	}
	t.Logf("%d captured requests replayed", total)
}

// neoPiCapturedListingRows builds the rows NeoPi's manager discovered for a
// capture of a provider it bundles no rows for, from the recorded listing
// (and the models.dev catalog it hydrated from).
func neoPiCapturedListingRows(t *testing.T, capture neoPiCompletionsCapture) map[string]*registry.NeoPiModel {
	t.Helper()
	discovered := map[string]*registry.NeoPiModel{}
	if capture.Listing == "" {
		return discovered
	}
	base := registry.NeoPiCompletionsListingBase(capture.Provider)
	infos, err := registry.NeoPiCompletionsListing(capture.Provider, base, []byte(capture.Listing))
	switch {
	case capture.ModelsDev != "":
		infos, err = registry.NeoPiSiliconFlowListing(capture.Provider, base, []byte(capture.Listing), []byte(capture.ModelsDev))
	case capture.LmStudioV0 != "" || capture.LmStudioV1 != "":
		infos, err = registry.NeoPiLmStudioListing(base, []byte(capture.Listing), []byte(capture.LmStudioV0), []byte(capture.LmStudioV1))
	}
	if err != nil {
		t.Fatalf("listing: %v", err)
	}
	for _, info := range infos {
		discovered[info.ID] = info.NeoPi
	}
	return discovered
}

func readNeoPiCompletionsCapture(t *testing.T, path string) neoPiCompletionsCapture {
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
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	var capture neoPiCompletionsCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Fixtures) == 0 {
		t.Fatalf("%s has no fixtures", path)
	}
	return capture
}

func TestNeoPiCompletionsTimeoutDisabled(t *testing.T) {
	zero := 0
	row := &registry.NeoPiModel{CompletionsCompat: &registry.NeoPiCompletionsCompat{StreamFirstEventTimeoutMs: &zero}}
	if value := neoPiCompletionsHeaders(context.Background(), nil, nil, "key", row, "").Get("X-Stainless-Timeout"); value != "" {
		t.Fatalf("disabled timeout sent %q", value)
	}
}

var neoPiCoreWeaveProjectEnv = []string{"COREWEAVE_PROJECT", "WANDB_INFERENCE_PROJECT", "WANDB_PROJECT", "WANDB_ENTITY"}

// CoreWeave's OpenAI-Project header: a configured value wins, a blank one is
// dropped, and otherwise the environment's project is sent.
func TestNeoPiCompletionsCoreWeaveProjectHeader(t *testing.T) {
	row := &registry.NeoPiModel{Provider: "coreweave", CompletionsCompat: &registry.NeoPiCompletionsCompat{}}
	cases := []struct {
		name       string
		configured map[string]string
		env        map[string]string
		want       string
	}{
		{name: "environment", env: map[string]string{"COREWEAVE_PROJECT": " team/env "}, want: "team/env"},
		{name: "configured wins", configured: map[string]string{"header:OpenAI-Project": "team/configured"}, env: map[string]string{"COREWEAVE_PROJECT": "team/env"}, want: "team/configured"},
		{name: "blank configured falls back", configured: map[string]string{"header:OpenAI-Project": "  "}, env: map[string]string{"WANDB_ENTITY": "team", "WANDB_PROJECT": "proj"}, want: "team/proj"},
		{name: "unqualified project", env: map[string]string{"WANDB_PROJECT": "proj"}, want: ""},
		{name: "none", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, name := range neoPiCoreWeaveProjectEnv {
				t.Setenv(name, tc.env[name])
			}
			auth := &cliproxyauth.Auth{Attributes: tc.configured}
			headers := neoPiCompletionsHeaders(context.Background(), auth, nil, "key", row, "")
			if got := headers.Get("OpenAI-Project"); got != tc.want {
				t.Fatalf("OpenAI-Project = %q, want %q", got, tc.want)
			}
			if _, present := headers["Openai-Project"]; tc.want == "" && present {
				t.Fatalf("blank OpenAI-Project sent")
			}
		})
	}
}

func TestNeoPiCompletionsFinishedEOF(t *testing.T) {
	input := `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}` + "\n\n"
	var lines []string
	err := neoPiScanCompletions(context.Background(), nil, "test", strings.NewReader(input), func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	if err != nil || len(lines) != 2 || lines[1] != "data: [DONE]" {
		t.Fatalf("lines = %q, error = %v", lines, err)
	}
}
