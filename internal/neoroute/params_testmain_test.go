package neoroute

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Tests build inventories of invented models; give them reasoning levels
// instead of reading the global registry.
func init() {
	modelInfo = func(m neopool.Model) *registry.ModelInfo {
		return &registry.ModelInfo{ID: m.Name, ContextLength: m.Context, Thinking: &registry.ThinkingSupport{Levels: []string{"minimal", "low", "medium", "high", "xhigh", "max"}}}
	}
}
