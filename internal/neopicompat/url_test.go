package neopicompat

import "testing"

// The hostnames are what `new URL(baseURL).hostname` answers in Bun; "" marks
// a URL the WHATWG parser rejects.
func TestURLHostnameMatchesWHATWG(t *testing.T) {
	cases := map[string]string{
		"https://API.OPENAI.COM/v1":        "api.openai.com",
		"http://127.1:8080/v1":             "127.0.0.1",
		"http://2130706433:8080/v1":        "127.0.0.1",
		"http://0x7f.1/v1":                 "127.0.0.1",
		"http://0177.0.0.1/v1":             "127.0.0.1",
		"//localhost:8080/v1":              "",
		"http://LocalHost./v1":             "localhost.",
		"http://[0:0:0:0:0:0:0:1]:8080/v1": "[::1]",
		"http://08.0.0.1/v1":               "",
		"http://256.0.0.1/v1":              "",
		"http://1.2.3.4.5/v1":              "",
		"http://example.com./v1":           "example.com.",
		"http://0x/v1":                     "0.0.0.0",
		"http://1.2.3.0x/v1":               "1.2.3.0",
		"foo://BAR.com/x":                  "BAR.com",
		"http://4294967296/":               "",
		"http://10.0.0.1.":                 "10.0.0.1",
		"http://a.0x1g/":                   "a.0x1g",
	}
	for input, want := range cases {
		got, ok := urlHostname(input)
		if want == "" {
			if ok {
				t.Errorf("%s: hostname %q, want a parse failure", input, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s: hostname %q (ok=%v), want %q", input, got, ok, want)
		}
	}
	if !hasLocalLoopbackBaseURL("http://127.1:8080/v1") || hasLocalLoopbackBaseURL("//localhost:8080/v1") {
		t.Error("loopback detection does not follow the WHATWG hostname")
	}
	if !isOfficialOpenAIEndpoint("openai", "https://API.OPENAI.COM/v1") {
		t.Error("a mixed-case api.openai.com is not the official endpoint")
	}
}

// An explicit null override clears a rule-owned compat value, as NeoPi's
// applyCompatOverrides assigns every value but undefined.
func TestExplicitNullOverrideClearsRuleValue(t *testing.T) {
	spec := Spec{Provider: "deepseek", API: "openai-completions", ID: "deepseek-chat", BaseURL: "https://api.deepseek.com"}
	base, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if base.Compat["extraBody"] == nil {
		t.Fatal("deepseek-chat resolves no extraBody to clear")
	}
	spec.Compat = map[string]any{"extraBody": nil}
	cleared, err := Resolve(spec)
	if err != nil {
		t.Fatal(err)
	}
	if value, declared := cleared.Compat["extraBody"]; !declared || value != nil {
		t.Errorf("extraBody = %v (declared %v), want null", value, declared)
	}
}
