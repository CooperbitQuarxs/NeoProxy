package anthropicnpi

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type discoveryUsageRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}
type discoveryUsageReply struct {
	Status      int               `json:"status"`
	ContentType string            `json:"contentType"`
	Headers     map[string]string `json:"headers"`
	Body        json.RawMessage   `json:"body"`
	Text        *string           `json:"text"`
}
type discoveryUsageWindow struct {
	ID       string  `json:"id"`
	Used     float64 `json:"used"`
	ResetsAt *int64  `json:"resetsAt"`
}
type discoveryUsageCase struct {
	Name     string                  `json:"name"`
	Base     string                  `json:"base"`
	Token    string                  `json:"token"`
	Replies  []discoveryUsageReply   `json:"replies"`
	Requests []discoveryUsageRequest `json:"requests"`
	Delays   []int64                 `json:"delays"`
	Windows  []discoveryUsageWindow  `json:"windows"`
	Models   []DiscoveredModel       `json:"models"`
}

func discoveryUsageRecorder(t *testing.T, fixture discoveryUsageCase) (*Client, func()) {
	t.Helper()
	captured := make(chan discoveryUsageRequest, 32)
	index := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		headers := map[string]string{"host": r.Host}
		for key, values := range r.Header {
			headers[strings.ToLower(key)] = strings.Join(values, ", ")
		}
		captured <- discoveryUsageRequest{Method: r.Method, Headers: headers, Body: string(body)}
		reply := fixture.Replies[min(index, len(fixture.Replies)-1)]
		index++
		contentType := reply.ContentType
		if contentType == "" {
			contentType = "application/json"
		}
		w.Header().Set("Content-Type", contentType)
		for key, value := range reply.Headers {
			w.Header().Set(key, value)
		}
		status := reply.Status
		if status == 0 {
			status = 200
		}
		w.WriteHeader(status)
		if reply.Text != nil {
			_, _ = io.WriteString(w, *reply.Text)
		} else if len(reply.Body) > 0 {
			_, _ = w.Write(reply.Body)
		} else {
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	transport := &captureTransport{target: target, base: http.DefaultTransport}
	client := &Client{HTTP: &http.Client{Transport: transport}}
	return client, func() {
		t.Helper()
		if len(transport.urls) != len(fixture.Requests) {
			t.Fatalf("requests = %v, want %v", transport.urls, fixture.Requests)
		}
		for i, want := range fixture.Requests {
			got := <-captured
			got.URL = transport.urls[i]
			if !reflect.DeepEqual(got, want) {
				t.Errorf("wire request %d differs\ngot: %#v\nwant: %#v", i, got, want)
			}
		}
	}
}

func TestClaudeDiscoveryUsageSourceCaptureParity(t *testing.T) {
	data, err := os.ReadFile("testdata/discovery_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		FixedNow int64                `json:"fixedNow"`
		Usage    []discoveryUsageCase `json:"usage"`
		Models   []discoveryUsageCase `json:"models"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range capture.Usage {
		t.Run("usage/"+fixture.Name, func(t *testing.T) {
			client, verify := discoveryUsageRecorder(t, fixture)
			delays := make([]int64, 0)
			usage, err := client.usage(context.Background(), "sk-ant-oat-parity-fake", fixture.Base, func(_ context.Context, delay time.Duration) error {
				delays = append(delays, delay.Milliseconds())
				return nil
			}, func() time.Time { return time.UnixMilli(capture.FixedNow) })
			verify()
			if !reflect.DeepEqual(delays, fixture.Delays) {
				t.Errorf("retry delays = %v, want %v", delays, fixture.Delays)
			}
			last := fixture.Replies[min(len(fixture.Requests)-1, len(fixture.Replies)-1)]
			if last.Status >= 400 && err == nil {
				t.Error("rejected upstream returned successful usage")
			}
			windows := make([]discoveryUsageWindow, 0)
			if usage != nil {
				for _, item := range []struct {
					id     string
					bucket *UsageBucket
				}{{"5h", usage.FiveHour}, {"7d", usage.SevenDay}} {
					if item.bucket == nil || item.bucket.Utilization == nil {
						continue
					}
					window := discoveryUsageWindow{ID: item.id, Used: math.Max(0, math.Min(100, *item.bucket.Utilization))}
					if item.bucket.ResetsAt != "" {
						reset, parseErr := time.Parse(time.RFC3339Nano, item.bucket.ResetsAt)
						if parseErr != nil {
							t.Fatal(parseErr)
						}
						millis := reset.UnixMilli()
						window.ResetsAt = &millis
					}
					windows = append(windows, window)
				}
			}
			if !reflect.DeepEqual(windows, fixture.Windows) {
				t.Errorf("shared windows = %#v, want %#v (error %v)", windows, fixture.Windows, err)
			}
		})
	}
	for _, fixture := range capture.Models {
		t.Run("models/"+fixture.Name, func(t *testing.T) {
			client, verify := discoveryUsageRecorder(t, fixture)
			models, err := client.Models(context.Background(), fixture.Token, fixture.Base)
			verify()
			if fixture.Models == nil {
				if err == nil || models != nil {
					t.Fatalf("unusable source response became models: %#v, error %v", models, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(models, fixture.Models) {
				t.Errorf("models = %#v, want %#v", models, fixture.Models)
			}
		})
	}
}
