package vertex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type adcCapture struct {
	Now     int64 `json:"now"`
	Results []struct {
		Name    string            `json:"name"`
		Env     map[string]string `json:"env"`
		File    json.RawMessage   `json:"file"`
		UserADC json.RawMessage   `json:"userAdc"`
		Replies []json.RawMessage `json:"replies"`
		Calls   int               `json:"calls"`
		Tokens  []string          `json:"tokens"`
		Error   string            `json:"error"`
		// Requests are what NeoPi's fetch was handed, in order.
		Requests []adcRequest `json:"requests"`
	} `json:"results"`
}

type adcRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type adcReplay struct {
	replies  []json.RawMessage
	requests []adcRequest
}

func (r *adcReplay) RoundTrip(request *http.Request) (*http.Response, error) {
	headers := map[string]string{}
	for name, values := range request.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
	}
	r.requests = append(r.requests, adcRequest{Method: request.Method, URL: request.URL.String(), Headers: headers, Body: string(body)})
	if len(r.replies) == 0 {
		return nil, errors.New("fetch failed")
	}
	reply := r.replies[0]
	r.replies = r.replies[1:]
	var answer struct {
		Status int             `json:"status"`
		Body   json.RawMessage `json:"body"`
	}
	if string(reply) == `"network-error"` || json.Unmarshal(reply, &answer) != nil {
		return nil, errors.New("fetch failed")
	}
	// The script answered with JSON.stringify of the body: compact JSON, or
	// a string as is.
	var payload []byte
	var text string
	if json.Unmarshal(answer.Body, &text) == nil {
		payload = []byte(text)
	} else {
		var compact bytes.Buffer
		if err := json.Compact(&compact, answer.Body); err != nil {
			return nil, err
		}
		payload = compact.Bytes()
	}
	return &http.Response{StatusCode: answer.Status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader(payload)), Request: request}, nil
}

// Every credential source and type sends NeoPi's exact token requests
// (testdata/vertex_adc_capture.json, from capture_vertex_adc.ts with NeoPi's
// clock pinned, so the RS256 assertion is byte-identical) and yields its
// tokens, cache hits and errors. A scenario's credential file also runs as an
// account's own credential, which must behave the same.
func TestAccessTokenReplaysNeoPi(t *testing.T) {
	raw, err := os.ReadFile("testdata/vertex_adc_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture adcCapture
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	pinned := time.UnixMilli(capture.Now)
	nowFunc = func() time.Time { return pinned }
	defer func() { nowFunc = time.Now }()
	accountRuns := 0
	for _, result := range capture.Results {
		for _, account := range []bool{false, true} {
			hasFile := len(result.File) > 0 && string(result.File) != "null"
			if account && (!hasFile || result.Env["GOOGLE_CLOUD_ACCESS_TOKEN"] != "" || result.Env["CLOUDSDK_AUTH_ACCESS_TOKEN"] != "") {
				continue
			}
			name := result.Name
			if account {
				name += "/account"
				accountRuns++
			}
			t.Run(name, func(t *testing.T) {
				resetTokenCache()
				home := t.TempDir()
				t.Setenv("HOME", home)
				for _, env := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_ACCESS_TOKEN", "CLOUDSDK_AUTH_ACCESS_TOKEN", "GOOGLE_VERTEX_REFRESH_SKEW_MS", "APPDATA"} {
					t.Setenv(env, "")
				}
				for key, value := range result.Env {
					t.Setenv(key, strings.ReplaceAll(value, "<home>", home))
				}
				if len(result.UserADC) > 0 && string(result.UserADC) != "null" {
					path := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
					if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, result.UserADC, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var credential []byte
				if hasFile {
					if account {
						credential = result.File
					} else {
						path := filepath.Join(home, "credentials.json")
						if err := os.WriteFile(path, result.File, 0o600); err != nil {
							t.Fatal(err)
						}
						t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
					}
				}
				replay := &adcReplay{replies: append([]json.RawMessage(nil), result.Replies...)}
				client := &http.Client{Transport: replay}
				var tokens []string
				var gotErr string
				for i := 0; i < max(result.Calls, 1); i++ {
					token, err := AccessToken(context.Background(), client, credential)
					if err != nil {
						gotErr = strings.ReplaceAll(err.Error(), home, "<home>")
						break
					}
					tokens = append(tokens, token)
				}
				if gotErr != result.Error {
					t.Errorf("error = %q, NeoPi %q", gotErr, result.Error)
				}
				if len(tokens) != len(result.Tokens) || (len(tokens) > 0 && !reflect.DeepEqual(tokens, result.Tokens)) {
					t.Errorf("tokens = %q, NeoPi %q", tokens, result.Tokens)
				}
				if len(replay.requests) != len(result.Requests) {
					t.Fatalf("sent %d requests, NeoPi %d: %+v", len(replay.requests), len(result.Requests), replay.requests)
				}
				for i, want := range result.Requests {
					if !reflect.DeepEqual(replay.requests[i], want) {
						t.Errorf("request %d:\n got %+v\nwant %+v", i, replay.requests[i], want)
					}
				}
			})
		}
	}
	if accountRuns == 0 {
		t.Fatal("no scenario ran as an account credential")
	}
}

// NeoProxy stores service account keys re-encoded as PKCS#1; that form signs
// the same assertion as the PKCS#8 key NeoPi reads.
func TestServiceAccountAssertionAcceptsPKCS1(t *testing.T) {
	pkcs8, err := os.ReadFile("testdata/fixture_key.pem")
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeServiceAccountMap(map[string]any{"private_key": string(pkcs8)})
	if err != nil {
		t.Fatal(err)
	}
	pkcs1 := normalized["private_key"].(string)
	if !strings.Contains(pkcs1, "BEGIN RSA PRIVATE KEY") {
		t.Fatalf("normalized key is not PKCS#1: %.40q", pkcs1)
	}
	at := time.Unix(1_800_000_000, 0)
	creds := adcCredentials{Type: "service_account", ClientEmail: "sa@p.iam.gserviceaccount.com", PrivateKeyID: "kid"}
	creds.PrivateKey = string(pkcs8)
	want, err := serviceAccountAssertion(&creds, at)
	if err != nil {
		t.Fatal(err)
	}
	creds.PrivateKey = pkcs1
	got, err := serviceAccountAssertion(&creds, at)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("PKCS#1 assertion differs from PKCS#8:\n got %s\nwant %s", got, want)
	}
}
