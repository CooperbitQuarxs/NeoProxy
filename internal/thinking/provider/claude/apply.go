// Package claude projects normalized reasoning intent for NeoPi's wire layer.
// Model-specific disabled, display, binding and context rules are applied after
// dialect translation by the official Claude executor.
package claude

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/tidwall/sjson"
)

type Applier struct{}

func NewApplier() *Applier { return &Applier{} }

func init() { thinking.RegisterProvider("claude", NewApplier()) }

func (*Applier) Apply(body []byte, intent thinking.ThinkingConfig, model *registry.ModelInfo) ([]byte, error) {
	adaptive := model != nil && model.Thinking != nil && len(model.Thinking.Levels) > 0
	switch intent.Mode {
	case thinking.ModeNone:
		out, err := sjson.SetRawBytes(body, "thinking", []byte(`{"type":"disabled"}`))
		if err != nil {
			return nil, err
		}
		return sjson.DeleteBytes(out, "output_config.effort")
	case thinking.ModeLevel:
		if adaptive {
			out, err := sjson.SetRawBytes(body, "thinking", []byte(`{"type":"adaptive"}`))
			if err != nil {
				return nil, err
			}
			return sjson.SetBytes(out, "output_config.effort", string(intent.Level))
		}
		if budget, ok := thinking.ConvertLevelToBudget(string(intent.Level)); ok {
			intent.Mode = thinking.ModeBudget
			intent.Budget = budget
		} else {
			return body, nil
		}
		fallthrough
	case thinking.ModeBudget:
		if intent.Budget == 0 {
			return sjson.SetRawBytes(body, "thinking", []byte(`{"type":"disabled"}`))
		}
		out, err := sjson.SetRawBytes(body, "thinking", []byte(`{"type":"enabled"}`))
		if err != nil {
			return nil, err
		}
		return sjson.SetBytes(out, "thinking.budget_tokens", intent.Budget)
	case thinking.ModeAuto:
		if adaptive {
			return sjson.SetRawBytes(body, "thinking", []byte(`{"type":"adaptive"}`))
		}
		return sjson.SetRawBytes(body, "thinking", []byte(`{"type":"enabled","budget_tokens":10240}`))
	default:
		return body, nil
	}
}
