package xiaomi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/neopikeys"
)

// loginCapture is testdata/xiaomi_login_capture.json: NeoPi's OWN loginXiaomi
// over scripted answers, one per probe.
type loginCapture struct {
	Results []struct {
		Name  string `json:"name"`
		Input string `json:"input"`
		Steps []struct {
			Status       int             `json:"status"`
			Body         json.RawMessage `json:"body"`
			NetworkError bool            `json:"networkError"`
		} `json:"steps"`
		Requests []struct {
			URL     string            `json:"url"`
			Method  string            `json:"method"`
			Headers map[string]string `json:"headers"`
			Body    string            `json:"body"`
		} `json:"requests"`
		Value     *string `json:"value"`
		Error     *string `json:"error"`
		ErrorName *string `json:"errorName"`
	} `json:"results"`
}

type sentRequest struct {
	url    string
	method string
	header http.Header
	body   string
}

// scripted answers each probe with the next scripted step, recording it.
type scripted struct {
	steps func(int) (status int, body []byte, networkError bool)
	sent  *[]sentRequest
}

func (s scripted) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	*s.sent = append(*s.sent, sentRequest{url: req.URL.String(), method: req.Method, header: req.Header.Clone(), body: string(body)})
	status, reply, networkError := s.steps(len(*s.sent) - 1)
	if networkError {
		return nil, errors.New("fetch failed")
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(reply)), Request: req}, nil
}

// Every captured login yields NeoPi's probes (URL, headers, body) in NeoPi's
// order and NeoPi's outcome: the standard host for pay-as-you-go keys, the
// SGP/AMS/CN walk for Token Plan keys that moves on only after a 401 or an
// unreachable cluster, and NeoPi's failure message.
func TestValidateKeyMatchesNeoPi(t *testing.T) {
	data, err := os.ReadFile("testdata/xiaomi_login_capture.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture loginCapture
	if err = json.Unmarshal(data, &capture); err != nil {
		t.Fatal(err)
	}
	for _, result := range capture.Results {
		t.Run(result.Name, func(t *testing.T) {
			key, err := NormalizeKey(result.Input)
			if err != nil {
				if result.ErrorName == nil || *result.ErrorName != "ApiKeyRequiredError" || !errors.Is(err, neopikeys.ErrKeyRequired) {
					t.Fatalf("NormalizeKey = %v, NeoPi %v", err, result.Error)
				}
				return
			}
			var sent []sentRequest
			steps := func(i int) (int, []byte, bool) {
				step := result.Steps[min(i, len(result.Steps)-1)]
				status := step.Status
				if status == 0 {
					status = http.StatusOK
				}
				var text string
				var compact bytes.Buffer
				_ = json.Compact(&compact, step.Body)
				body := compact.Bytes()
				if json.Unmarshal(step.Body, &text) == nil {
					body = []byte(text)
				}
				return status, body, step.NetworkError
			}
			err = ValidateKey(t.Context(), &http.Client{Transport: scripted{steps: steps, sent: &sent}}, key)
			if len(sent) != len(result.Requests) {
				t.Fatalf("probes = %d, NeoPi %d", len(sent), len(result.Requests))
			}
			for i, want := range result.Requests {
				got := sent[i]
				if got.url != want.URL || got.method != want.Method || got.body != want.Body {
					t.Errorf("probe %d = %s %s %s, NeoPi %s %s %s", i, got.method, got.url, got.body, want.Method, want.URL, want.Body)
				}
				for name, value := range want.Headers {
					if got.header.Get(name) != value {
						t.Errorf("probe %d header %s = %q, NeoPi %q", i, name, got.header.Get(name), value)
					}
				}
			}
			switch {
			case result.Value != nil:
				if err != nil || key != *result.Value {
					t.Fatalf("key %q, err %v; NeoPi accepted %q", key, err, *result.Value)
				}
			case result.ErrorName != nil && *result.ErrorName == "OAuthError":
				var failure *neopikeys.HTTPError
				if !errors.As(err, &failure) || failure.Error() != *result.Error {
					t.Fatalf("err = %v, NeoPi %q", err, *result.Error)
				}
			default:
				var failure *neopikeys.HTTPError
				if err == nil || errors.As(err, &failure) {
					t.Fatalf("err = %v, NeoPi failed to reach the host (%v)", err, *result.Error)
				}
			}
		})
	}
}
