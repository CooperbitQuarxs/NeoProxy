package githubcopilot

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
)

type usageRoundTripper func(*http.Request) (*http.Response, error)

func (f usageRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// FetchUsage sends the requests NeoPi's own Copilot usage provider sent in
// each captured scenario (testdata/capture_neopi_github_copilot_usage.ts) and
// reports the same limits and account, fails with the HTTP status where NeoPi
// threw an auth rejection, and fails transiently where NeoPi reported
// nothing. Every host is answered in process.
func TestFetchUsageMatchesNeoPi(t *testing.T) {
	raw, err := os.ReadFile("testdata/neopi_github_copilot_usage.json")
	if err != nil {
		t.Fatal(err)
	}
	type reply struct {
		Status int    `json:"status"`
		Body   string `json:"body"`
	}
	type request struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	var capture struct {
		Token   string `json:"token"`
		Results []struct {
			Name          string    `json:"name"`
			EnterpriseURL string    `json:"enterpriseUrl"`
			Internal      reply     `json:"internal"`
			User          *reply    `json:"user"`
			Requests      []request `json:"requests"`
			Report        *struct {
				Limits   json.RawMessage `json:"limits"`
				Metadata struct {
					AccountID *string `json:"accountId"`
				} `json:"metadata"`
			} `json:"report"`
			Error *struct {
				Status int `json:"status"`
			} `json:"error"`
		} `json:"results"`
	}
	if err = json.Unmarshal(raw, &capture); err != nil {
		t.Fatal(err)
	}
	for _, result := range capture.Results {
		t.Run(result.Name, func(t *testing.T) {
			var sent []request
			client := &http.Client{Transport: usageRoundTripper(func(req *http.Request) (*http.Response, error) {
				headers := map[string]string{}
				for name := range req.Header {
					headers[strings.ToLower(name)] = req.Header.Get(name)
				}
				sent = append(sent, request{Method: req.Method, URL: req.URL.String(), Headers: headers})
				answer := reply{Status: http.StatusNotFound, Body: "{}"}
				if strings.HasSuffix(req.URL.Path, "/copilot_internal/user") {
					answer = result.Internal
				} else if result.User != nil {
					answer = *result.User
				}
				return &http.Response{StatusCode: answer.Status, Status: http.StatusText(answer.Status), Header: http.Header{"Content-Type": {"application/json"}},
					Body: io.NopCloser(strings.NewReader(answer.Body)), Request: req}, nil
			})}
			usage, err := FetchUsage(context.Background(), client, &Credential{AccessToken: capture.Token, EnterpriseURL: result.EnterpriseURL})
			if !reflect.DeepEqual(sent, result.Requests) {
				t.Errorf("requests\n port %+v\nNeoPi %+v", sent, result.Requests)
			}
			switch {
			case result.Error != nil:
				var refused *Error
				if !errors.As(err, &refused) || refused.Status != result.Error.Status {
					t.Fatalf("err = %v, NeoPi threw HTTP %d", err, result.Error.Status)
				}
			case result.Report == nil:
				var refused *Error
				if err == nil || errors.As(err, &refused) && (refused.Status == http.StatusUnauthorized || refused.Status == http.StatusForbidden) {
					t.Fatalf("err = %v, NeoPi reported nothing (a transient failure)", err)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				got, errMarshal := json.Marshal(usage.Limits)
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				var gotLimits, wantLimits any
				if err = json.Unmarshal(got, &gotLimits); err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(result.Report.Limits, &wantLimits); err != nil {
					t.Fatal(err)
				}
				if gotLimits == nil {
					gotLimits = []any{}
				}
				if !reflect.DeepEqual(gotLimits, wantLimits) {
					t.Errorf("limits\n port %s\nNeoPi %s", got, result.Report.Limits)
				}
				wantAccount := ""
				if result.Report.Metadata.AccountID != nil {
					wantAccount = *result.Report.Metadata.AccountID
				}
				if usage.AccountID != wantAccount {
					t.Errorf("account %q, NeoPi %q", usage.AccountID, wantAccount)
				}
			}
		})
	}
}
