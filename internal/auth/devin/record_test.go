package devin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/devinwire"
	"google.golang.org/protobuf/encoding/protowire"
)

// capturedUserStatus is a GetUserStatus response from the NeoPi usage capture.
func capturedUserStatus(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/neopi_devin_usage_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		Fixtures []struct {
			Name     string `json:"name"`
			Response string `json:"response"`
		} `json:"fixtures"`
	}
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range capture.Fixtures {
		if fixture.Name == name {
			response, _ := base64.StdEncoding.DecodeString(fixture.Response)
			return response
		}
	}
	t.Fatalf("no %s fixture", name)
	return nil
}

func userStatusServer(t *testing.T, response []byte) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != devinwire.UserStatusPath {
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if response == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(response)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestCreateAuthRecord(t *testing.T) {
	server := userStatusServer(t, capturedUserStatus(t, "quota-plan"))
	svc := NewDevinAuthService(server.Client())
	svc.SetServerBaseURL(server.URL)
	expires := time.Date(2030, 3, 1, 12, 0, 0, 0, time.UTC)
	record, errRecord := svc.CreateAuthRecord(context.Background(), Credential{Access: "eyJ.test.token", Expires: expires})
	if errRecord != nil {
		t.Fatal(errRecord)
	}
	if record.Provider != "devin" || record.FileName != "devin-pilot@example.com.json" || record.ID != record.FileName {
		t.Fatalf("unexpected record identity: %s %s", record.Provider, record.FileName)
	}
	for key, want := range map[string]string{
		"api_key": "eyJ.test.token", "auth_kind": "oauth", "email": "pilot@example.com",
		"user_id": "user-1", "org_id": "org-1", "org_name": "Pilot Org", "plan": "Devin Pro",
	} {
		if record.Attributes[key] != want || record.Metadata[key] != want {
			t.Errorf("%s = %q / %v, want %q in attributes and metadata", key, record.Attributes[key], record.Metadata[key], want)
		}
	}
	if record.Metadata["expired"] != "2030-03-01T12:00:00Z" {
		t.Errorf("expired = %v, want the credential's projected expiry", record.Metadata["expired"])
	}
	for key, want := range map[string]string{
		"plan": "Devin Pro", "daily_quota_remaining_percent": "55%", "weekly_quota_remaining_percent": "80%",
		"daily_quota_reset_at": "2026-09-22T18:00:00Z", "weekly_quota_reset_at": "2026-09-27T09:06:40Z",
	} {
		if record.Quota.Signals[key] != want {
			t.Errorf("signal %s = %q, want %q", key, record.Quota.Signals[key], want)
		}
	}
}

func TestCreateAuthRecordSafeFallbackFilename(t *testing.T) {
	for _, email := range []string{"", "../../outside/file", `..\outside\file`, strings.Repeat("x", 300)} {
		t.Run(email, func(t *testing.T) {
			var response []byte
			if email != "" {
				status := protowire.AppendString(protowire.AppendTag(nil, 7, protowire.BytesType), email)
				response = protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), status)
			}
			server := userStatusServer(t, response)
			svc := NewDevinAuthService(server.Client())
			svc.SetServerBaseURL(server.URL)
			record, errRecord := svc.CreateAuthRecord(context.Background(), Credential{Access: "eyJ.first.token"})
			if errRecord != nil {
				t.Fatal(errRecord)
			}
			if record.FileName != filepath.Base(record.FileName) || strings.ContainsAny(record.FileName, `/\`) || len(record.FileName) > 200 {
				t.Fatalf("unsafe credential filename: %q", record.FileName)
			}
			if email == "" {
				second, errSecond := svc.CreateAuthRecord(context.Background(), Credential{Access: "eyJ.second.token"})
				if errSecond != nil {
					t.Fatal(errSecond)
				}
				if second.FileName == record.FileName {
					t.Fatal("different unknown accounts overwrite the same file")
				}
			}
		})
	}
	if _, errRecord := NewDevinAuthService(nil).CreateAuthRecord(context.Background(), Credential{}); errRecord == nil {
		t.Fatal("empty token accepted")
	}
}
