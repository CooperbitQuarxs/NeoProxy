package devin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
)

// TestFetchUsageMatchesNeoPi replays testdata/neopi_devin_usage_capture.json
// (NeoPi's own devinUsageProvider against loopback GetUserStatus responses):
// the request on the wire and the report built from each response.
func TestFetchUsageMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/neopi_devin_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Token    string `json:"token"`
		Fixtures []struct {
			Name     string `json:"name"`
			Status   int    `json:"status"`
			Response string `json:"response"`
			Request  struct {
				Method  string            `json:"method"`
				Path    string            `json:"path"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"request"`
			Report json.RawMessage `json:"report"`
		} `json:"fixtures"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range capture.Fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			response, _ := base64.StdEncoding.DecodeString(fixture.Response)
			var method, path string
			var headers map[string]string
			var body []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method, path = r.Method, r.URL.Path
				headers = map[string]string{}
				for name, values := range r.Header {
					if name != "Content-Length" {
						headers[strings.ToLower(name)] = strings.Join(values, ", ")
					}
				}
				body, _ = io.ReadAll(r.Body)
				if fixture.Status != http.StatusOK {
					http.Error(w, "unavailable", fixture.Status)
					return
				}
				w.Header().Set("Content-Type", "application/proto")
				_, _ = w.Write(response)
			}))
			defer server.Close()
			usage, err := FetchUsage(context.Background(), server.Client(), server.URL+"/", " "+capture.Token+" ")
			if method != fixture.Request.Method || path != fixture.Request.Path {
				t.Errorf("request = %s %s, want %s %s", method, path, fixture.Request.Method, fixture.Request.Path)
			}
			want := map[string]string{}
			for name, value := range fixture.Request.Headers {
				if name != "content-length" {
					want[name] = value
				}
			}
			if !reflect.DeepEqual(headers, want) {
				t.Errorf("headers:\n got %v\nwant %v", headers, want)
			}
			if wantBody, _ := base64.StdEncoding.DecodeString(fixture.Request.Body); !bytes.Equal(body, wantBody) {
				t.Errorf("body:\n got %x\nwant %x", body, wantBody)
			}
			if string(fixture.Report) == "null" {
				if err == nil {
					t.Fatalf("NeoPi reported nothing, got %+v", usage)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(usage)
			var got, wantReport any
			_ = json.Unmarshal(encoded, &got)
			_ = json.Unmarshal(fixture.Report, &wantReport)
			if !reflect.DeepEqual(got, wantReport) {
				t.Errorf("report:\n got %s\nwant %s", encoded, fixture.Report)
			}
		})
	}
}
