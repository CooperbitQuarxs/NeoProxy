package umans

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// The key check sends NeoPi's probe byte for byte (testdata/umans_key_capture.json)
// and reports the status Umans answered.
func TestValidateKeyMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/umans_key_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		APIKey  string `json:"apiKey"`
		Request struct {
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"request"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	var sent *http.Request
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent, body = r.Clone(context.Background()), nil
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	status, err := ValidateKey(context.Background(), server.Client(), server.URL+"/v1/", capture.APIKey)
	if err != nil || status != http.StatusUnauthorized {
		t.Fatalf("status, err = %d, %v; want 401", status, err)
	}
	if sent.Method != capture.Request.Method || sent.URL.Path != "/v1/messages" {
		t.Errorf("request = %s %s", sent.Method, sent.URL.Path)
	}
	if string(body) != capture.Request.Body {
		t.Errorf("body = %s, want %s", body, capture.Request.Body)
	}
	for name, value := range capture.Request.Headers {
		if got := sent.Header.Get(name); got != value {
			t.Errorf("header %s = %q, want %q", name, got, value)
		}
	}
	if sent.Header.Get("Authorization") != "" {
		t.Errorf("Authorization sent: %q", sent.Header.Get("Authorization"))
	}
}
