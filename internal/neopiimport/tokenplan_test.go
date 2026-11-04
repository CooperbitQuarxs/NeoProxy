package neopiimport

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseTokenPlanKey(t *testing.T) {
	for _, tc := range []struct {
		name, value, token, baseURL string
		ok                          bool
	}{
		{"bare international key", " sk-sp-abc123 ", "sk-sp-abc123", "", true},
		{"cookie bundle", `{"token":"sk-sp-abc123","cookie":"a=b; c=d"}`, "sk-sp-abc123", "", true},
		{"china bundle", `{"token":" sk-sp-abc123 ","baseUrl":"https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1/"}`, "sk-sp-abc123", "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1", true},
		{"not an sk key", "tp-abc123", "", "", false},
		{"bundle without token", `{"cookie":"a=b"}`, "", "", false},
		{"bundle with bad token", `{"token":"abc"}`, "", "", false},
		{"bundle with non-string cookie", `{"token":"sk-sp-abc123","cookie":1}`, "", "", false},
		{"bundle with non-string baseUrl", `{"token":"sk-sp-abc123","baseUrl":true}`, "", "", false},
		{"broken json", `{"token":`, "", "", false},
		{"empty", "  ", "", "", false},
	} {
		got, ok := parseTokenPlanKey(tc.value)
		if ok != tc.ok || (ok && (got.token != tc.token || got.baseURL != tc.baseURL)) {
			t.Errorf("%s: got %+v %v, want token %q baseURL %q ok %v", tc.name, got, ok, tc.token, tc.baseURL, tc.ok)
		}
	}
}

func TestTokenPlanImportMapping(t *testing.T) {
	const cnURL = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
	dir, _ := newProfile(t, []fixtureRow{
		{"alibaba-token-plan", "api_key", map[string]any{"key": "sk-sp-fixtureintl1111"}, ""},
		{"alibaba-token-plan", "api_key", map[string]any{"key": `{"token":"sk-sp-fixturechina2222","cookie":"login_aliyunid_csrf=fixturecookie","baseUrl":"` + cnURL + `"}`}, ""},
		{"alibaba-token-plan", "api_key", map[string]any{"key": `{"cookie":"login_aliyunid_csrf=fixturecookie"}`}, ""},
	})
	creds, err := Read(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, tc := range []struct {
		id, key, baseURL, label string
	}{
		{"1", "sk-sp-fixtureintl1111", "", "…1111"},
		{"2", "sk-sp-fixturechina2222", cnURL, "…2222"},
	} {
		var cred Credential
		for _, c := range creds {
			if c.ID == tc.id {
				cred = c
			}
		}
		candidate := Describe(cred, now)
		if !candidate.Importable || candidate.Kind != KindAPIKey || candidate.NeoProxyProvider == nil || *candidate.NeoProxyProvider != "alibaba-token-plan" || candidate.Label != tc.label {
			t.Fatalf("credential %s candidate = %+v", tc.id, candidate)
		}
		got, err := BuildAPIKey(cred, now)
		if err != nil || got.Provider != "alibaba-token-plan" || got.APIKey != tc.key || got.BaseURL != tc.baseURL {
			t.Fatalf("credential %s = %+v, %v", tc.id, got, err)
		}
	}
	broken := creds[2]
	if candidate := Describe(broken, now); candidate.Importable || !strings.Contains(candidate.Reason, "Token Plan") {
		t.Fatalf("broken bundle candidate = %+v", candidate)
	}
	if _, err = BuildAPIKey(broken, now); !errors.Is(err, ErrNotImportable) {
		t.Fatalf("broken bundle: err = %v, want ErrNotImportable", err)
	}
}
