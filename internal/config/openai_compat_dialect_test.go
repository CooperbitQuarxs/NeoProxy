package config

import "testing"

func TestResolveModelDialect(t *testing.T) {
	entry := &OpenAICompatibility{
		Dialect: "openai-responses",
		Models: []OpenAICompatibilityModel{
			{Name: "claude-haiku-5-5", Dialect: "messages"},
			{Name: "gemini-4-argon", Alias: "argon", Dialect: "generate-content", BaseURL: "https://gw.example/google/v1beta/"},
			{Name: "fugu"},
			{Name: "odd", Dialect: "websocket"},
		},
	}
	cases := []struct {
		upstream, requested, dialect, base string
	}{
		{"claude-haiku-5-5", "", DialectMessages, ""},
		{"unknown", "argon", DialectGenerateContent, "https://gw.example/google/v1beta"},
		{"fugu", "", DialectResponses, ""},
		{"not-listed", "", DialectResponses, ""},
		{"odd", "", "websocket", ""},
	}
	for _, tc := range cases {
		dialect, base := entry.ResolveModelDialect(tc.upstream, tc.requested)
		if dialect != tc.dialect || base != tc.base {
			t.Errorf("%s/%s = (%q, %q), want (%q, %q)", tc.upstream, tc.requested, dialect, base, tc.dialect, tc.base)
		}
	}
	if dialect, _ := (&OpenAICompatibility{}).ResolveModelDialect("any", ""); dialect != DialectChatCompletions {
		t.Errorf("default dialect = %q", dialect)
	}
	if dialect, _ := (*OpenAICompatibility)(nil).ResolveModelDialect("any", ""); dialect != DialectChatCompletions {
		t.Errorf("nil entry dialect = %q", dialect)
	}
}
