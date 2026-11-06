package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// An Amazon Bedrock key the dashboard adds (an OpenAI-compatibility entry
// named after the catalog provider, suffixed for a second endpoint) runs on
// NeoPi's native Converse executor under its runtime key; other
// compatibility accounts keep their own executors.
func TestAmazonBedrockAccountsBindTheConverseExecutor(t *testing.T) {
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	for _, tc := range []struct{ name, key string }{
		{"amazon-bedrock", "openai-compatible-amazon-bedrock"},
		{"amazon-bedrock-2", "openai-compatible-amazon-bedrock-2"},
	} {
		auth := &coreauth.Auth{ID: tc.name, Provider: "openai-compatibility", Status: coreauth.StatusActive,
			Attributes: map[string]string{"compat_name": tc.name, "provider_key": tc.key, "api_key": "ABSK-key"}}
		if key, ok := amazonBedrockIdentity(auth); !ok || key != tc.key {
			t.Fatalf("%s identity = %q, %v", tc.name, key, ok)
		}
		service.ensureExecutorsForAuth(auth)
		bound, ok := service.coreManager.Executor(tc.key)
		if _, native := bound.(*executor.AmazonBedrockExecutor); !ok || !native {
			t.Fatalf("%s executor = %T, want the Converse executor", tc.name, bound)
		}
	}
	cerebras := &coreauth.Auth{ID: "c", Provider: "openai-compatibility", Status: coreauth.StatusActive,
		Attributes: map[string]string{"compat_name": "cerebras", "provider_key": "openai-compatible-cerebras", "api_key": "k"}}
	if _, ok := amazonBedrockIdentity(cerebras); ok {
		t.Fatal("cerebras is not an Amazon Bedrock account")
	}
	service.ensureExecutorsForAuth(cerebras)
	if bound, _ := service.coreManager.Executor("openai-compatible-cerebras"); bound == nil {
		t.Fatal("cerebras lost its executor")
	} else if _, native := bound.(*executor.AmazonBedrockExecutor); native {
		t.Fatal("cerebras bound the Converse executor")
	}
}
