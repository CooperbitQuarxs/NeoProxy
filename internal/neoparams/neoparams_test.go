package neoparams

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/tidwall/gjson"
)

func ids(specs []Spec) []string {
	out := make([]string, len(specs))
	for i, s := range specs {
		out[i] = s.ID
	}
	return out
}

func optionValues(s Spec) []any {
	out := make([]any, len(s.Options))
	for i, o := range s.Options {
		out[i] = o.Value
	}
	return out
}

func codexModel(name string) (neopool.Model, *registry.ModelInfo) {
	return neopool.Model{ID: "codex/" + name, ProviderID: "codex", Name: name, Exposed: true, Context: 272000},
		&registry.ModelInfo{ID: name, ContextLength: 272000, Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh", "max"}}, SupportedParameters: []string{"tools"}}
}

// A model Codex discovery describes: its tiers and windows win over the client
// template, and sampling knobs NeoPi strips for it are not offered.
func TestCatalogueCodexFastAndUltrafast(t *testing.T) {
	model, info := codexModel("gpt-6.1-sol")
	info.Codex = &registry.CodexModel{ID: "gpt-6.1-sol", Compat: &registry.CodexModelCompat{SupportsSamplingParams: false}}
	info.ServiceTiers = []string{"priority", "ultrafast"}
	info.MaxContextLength = 922000
	info.SupportedParameters = []string{"tools", "temperature", "top_p"}
	specs := Catalogue(model, info, nil)
	speed, ok := ByID(specs, IDSpeed)
	if !ok {
		t.Fatalf("no speed spec: %v", ids(specs))
	}
	if got := optionValues(speed); !reflect.DeepEqual(got, []any{"standard", "fast", "ultrafast"}) {
		t.Fatalf("speed options = %v", got)
	}
	if speed.Options[1].Multiplier != 2.5 || speed.Options[2].Multiplier != 8 {
		t.Fatalf("multipliers = %+v", speed.Options)
	}
	if speed.Sibling() != "" || !strings.Contains(speed.Realize, "service_tier=priority") || !strings.Contains(speed.Realize, "service_tier=ultrafast") {
		t.Fatalf("realize = %q sibling = %q", speed.Realize, speed.Sibling())
	}
	ctx, ok := ByID(specs, IDContext)
	if !ok || ctx.Kind != KindRange || ctx.Max != 922000 || ctx.Unit != "tokens" || !reflect.DeepEqual(ctx.Modes, []string{ModeForce}) {
		t.Fatalf("context spec = %+v", ctx)
	}
	standard := ctx.Default.(int)
	if standard != 272000 || ctx.Marks[0].Value != float64(standard) || ctx.Marks[len(ctx.Marks)-1].Value != 922000 {
		t.Fatalf("context marks = %+v default = %v", ctx.Marks, ctx.Default)
	}
	if len(ctx.Bands) != 1 || ctx.Bands[0].From != float64(standard) || ctx.Bands[0].To != 922000 || ctx.Bands[0].Multiplier != 2 {
		t.Fatalf("context bands = %+v", ctx.Bands)
	}
	effort, _ := ByID(specs, IDEffort)
	if got := optionValues(effort); !reflect.DeepEqual(got, []any{"low", "medium", "high", "xhigh", "max"}) || effort.Default == nil {
		t.Fatalf("effort = %+v", effort)
	}
	if efforts, ok := ByID(specs, IDEfforts); !ok || efforts.Kind != KindSet || efforts.AllowsMode(ModeDefault) || !efforts.AllowsMode(ModeForce) {
		t.Fatalf("efforts = %+v", efforts)
	}
	for _, id := range []string{IDVerbosity, IDReasoningSummary} {
		if _, ok := ByID(specs, id); !ok {
			t.Fatalf("missing %s from catalogue facts: %v", id, ids(specs))
		}
	}
	// Sampling is absent although the registry lists it: discovery says NeoPi strips it upstream.
	for _, id := range []string{IDTemperature, IDTopP, IDMaxOutput} {
		if _, ok := ByID(specs, id); ok {
			t.Fatalf("%s offered without facts", id)
		}
	}
}

func TestCatalogueLunaIsFastOnly(t *testing.T) {
	model, info := codexModel("gpt-6-luna")
	speed, ok := ByID(Catalogue(model, info, nil), IDSpeed)
	if !ok || !reflect.DeepEqual(optionValues(speed), []any{"standard", "fast"}) {
		t.Fatalf("luna speed = %+v", speed)
	}
}

func TestCatalogueGrokSiblingAndPriceStep(t *testing.T) {
	base := neopool.Model{ID: "xai/grok-4.7", ProviderID: "xai", Name: "grok-4.7", Exposed: true, Context: 500000}
	fast := neopool.Model{ID: "xai/grok-4.7-build-fast", ProviderID: "xai", Name: "grok-4.7-build-fast", Exposed: true}
	other := neopool.Model{ID: "other/grok-4.7-build-fast", ProviderID: "other", Name: "grok-4.7-build-fast", Exposed: true}
	info := &registry.ModelInfo{ID: "grok-4.7", ContextLength: 500000}

	specs := Catalogue(base, info, []neopool.Model{base, other, fast})
	speed, ok := ByID(specs, IDSpeed)
	if !ok || speed.Sibling() != "xai/grok-4.7-build-fast" || speed.Options[1].Multiplier != 2 {
		t.Fatalf("speed = %+v sibling = %q", speed, speed.Sibling())
	}
	if got := optionValues(speed); !reflect.DeepEqual(got, []any{"standard", "fast"}) {
		t.Fatalf("options = %v", got)
	}
	ctx, _ := ByID(specs, IDContext)
	if ctx.Max != 500000 || len(ctx.Bands) != 1 || ctx.Bands[0].From != 256000 || ctx.Bands[0].Multiplier != 2 {
		t.Fatalf("context = %+v", ctx)
	}
	// A sibling in another provider, or one that is not exposed, is no sibling.
	if _, ok := ByID(Catalogue(base, info, []neopool.Model{base, other}), IDSpeed); ok {
		t.Fatal("speed offered for a sibling of another provider")
	}
	fast.Exposed = false
	if _, ok := ByID(Catalogue(base, info, []neopool.Model{base, fast}), IDSpeed); ok {
		t.Fatal("speed offered for a hidden sibling")
	}
}

func TestCatalogueNoFactsNoParams(t *testing.T) {
	model := neopool.Model{ID: "mystery/m", ProviderID: "mystery", Name: "m", Exposed: true}
	if specs := Catalogue(model, nil, []neopool.Model{model}); len(specs) != 0 {
		t.Fatalf("specs without facts = %v", ids(specs))
	}
	if specs := Catalogue(model, &registry.ModelInfo{ID: "m"}, nil); len(specs) != 0 {
		t.Fatalf("specs from an empty registry entry = %v", ids(specs))
	}
}

func TestCatalogueSamplingFromSupportedParameters(t *testing.T) {
	model := neopool.Model{ID: "p/m", ProviderID: "p", Name: "m", Exposed: true}
	info := &registry.ModelInfo{ID: "m", Type: "claude", SupportedParameters: []string{"temperature", "top_p", "max_tokens"}, MaxCompletionTokens: 8192}
	specs := Catalogue(model, info, nil)
	if got := ids(specs); !reflect.DeepEqual(got, []string{IDTemperature, IDTopP, IDMaxOutput}) {
		t.Fatalf("specs = %v", got)
	}
	temperature, _ := ByID(specs, IDTemperature)
	maxOutput, _ := ByID(specs, IDMaxOutput)
	if temperature.Max != 1 || maxOutput.Max != 8192 || maxOutput.Unit != "tokens" {
		t.Fatalf("temperature = %+v max output = %+v", temperature, maxOutput)
	}
	// max_output needs a limit to bound it.
	info.MaxCompletionTokens = 0
	if _, ok := ByID(Catalogue(model, info, nil), IDMaxOutput); ok {
		t.Fatal("max_output offered without a limit")
	}
}

func TestSpecJSONShape(t *testing.T) {
	model, info := codexModel("gpt-6-sol")
	raw, err := json.Marshal(Catalogue(model, info, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range gjson.ParseBytes(raw).Array() {
		kind := spec.Get("kind").String()
		for _, field := range []string{"id", "label", "help", "group", "kind", "default", "realize"} {
			if !spec.Get(field).Exists() {
				t.Fatalf("%s has no %s: %s", spec.Get("id"), field, spec.Raw)
			}
		}
		if kind == KindRange && (!spec.Get("min").Exists() || !spec.Get("max").Exists() || !spec.Get("step").Exists()) {
			t.Fatalf("range without bounds: %s", spec.Raw)
		}
		if kind == KindChoice && len(spec.Get("options").Array()) == 0 {
			t.Fatalf("choice without options: %s", spec.Raw)
		}
	}
	zero, _ := json.Marshal(Spec{ID: "t", Kind: KindRange, Max: 2})
	if !strings.Contains(string(zero), `"min":0`) {
		t.Fatalf("a zero minimum must still be emitted: %s", zero)
	}
}

func TestSpecCheck(t *testing.T) {
	model, info := codexModel("gpt-6.1-sol")
	specs := Catalogue(model, info, nil)
	speed, _ := ByID(specs, IDSpeed)
	ctx, _ := ByID(specs, IDContext)
	efforts, _ := ByID(specs, IDEfforts)
	for _, test := range []struct {
		name  string
		spec  Spec
		value any
		ok    bool
	}{
		{"choice", speed, "fast", true},
		{"choice unknown", speed, "turbo", false},
		{"choice wrong type", speed, 3.0, false},
		{"range in bounds", ctx, 300000.0, true},
		{"range int", ctx, 300000, true},
		{"range below", ctx, 100.0, false},
		{"range above", ctx, 9000000.0, false},
		{"range string", ctx, "big", false},
		{"set", efforts, []any{"low", "high"}, true},
		{"set unknown member", efforts, []any{"low", "turbo"}, false},
		{"set duplicate", efforts, []any{"low", "low"}, false},
		{"set empty", efforts, []any{}, false},
		{"set scalar", efforts, "low", false},
		{"toggle", Spec{ID: "t", Kind: KindToggle}, true, true},
		{"toggle wrong", Spec{ID: "t", Kind: KindToggle}, "yes", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := test.spec.Check(test.value); (err == nil) != test.ok {
				t.Fatalf("Check(%v) error = %v, want ok=%v", test.value, err, test.ok)
			}
		})
	}
}

func specsOf(t *testing.T, name string) []Spec {
	t.Helper()
	model, info := codexModel(name)
	return Catalogue(model, info, nil)
}

func TestEvaluateForceAndDefault(t *testing.T) {
	speed, _ := ByID(specsOf(t, "gpt-6-luna"), IDSpeed)
	sent := Caller{Protocol: ProtocolResponses, Body: []byte(`{"service_tier":"priority"}`)}
	bare := Caller{Protocol: ProtocolResponses, Body: []byte(`{}`)}

	if _, ok := Evaluate(speed, ModeDefault, "fast", sent); ok {
		t.Fatal("default must yield to what the caller sent")
	}
	a, ok := Evaluate(speed, ModeDefault, "fast", bare)
	if !ok || a.Via != "tier" || !strings.Contains(a.Realize, "service_tier=priority") {
		t.Fatalf("default with no caller value = %+v ok=%v", a, ok)
	}
	if _, ok := Evaluate(speed, ModeForce, "fast", sent); !ok {
		t.Fatal("force must apply even when the caller sent a value")
	}
	if _, ok := Evaluate(speed, "", "fast", sent); !ok {
		t.Fatal("an empty mode means force")
	}
}

func TestEvaluateEffortsClampToHighestAllowed(t *testing.T) {
	efforts, _ := ByID(specsOf(t, "gpt-6-sol"), IDEfforts)
	allowed := []any{"low", "high", "medium"}
	for _, test := range []struct {
		caller string
		want   string // "" = pass through
	}{
		{"low", ""}, {"high", ""}, {"max", "high"}, {"xhigh", "high"}, {"", "high"}, {"none", "high"},
	} {
		a, ok := Evaluate(efforts, ModeForce, allowed, Caller{Effort: test.caller})
		if (test.want == "") == ok || (ok && a.Value != test.want) {
			t.Fatalf("caller %q: applied=%+v ok=%v want %q", test.caller, a, ok, test.want)
		}
	}
}

func TestApplyChatResponsesAndClaude(t *testing.T) {
	fast := []Applied{{ID: IDSpeed, Value: "fast", Via: "tier"}}
	for _, test := range []struct {
		protocol string
		in       string
		applied  []Applied
		path     string
		want     string
	}{
		{ProtocolOpenAI, `{"messages":[]}`, fast, "service_tier", "priority"},
		{ProtocolResponses, `{"input":"hi","service_tier":"auto"}`, fast, "service_tier", "priority"},
		{ProtocolClaude, `{"messages":[]}`, fast, "speed", "fast"},
		{ProtocolClaude, `{"messages":[]}`, fast, "service_tier", "priority"},
		{ProtocolResponses, `{}`, []Applied{{ID: IDSpeed, Value: "ultrafast", Via: "tier"}}, "service_tier", "ultrafast"},
		{ProtocolResponses, `{}`, []Applied{{ID: IDVerbosity, Value: "low"}}, "text.verbosity", "low"},
		{ProtocolOpenAI, `{}`, []Applied{{ID: IDVerbosity, Value: "low"}}, "verbosity", "low"},
		{ProtocolResponses, `{}`, []Applied{{ID: IDReasoningSummary, Value: "concise"}}, "reasoning.summary", "concise"},
		{ProtocolClaude, `{}`, []Applied{{ID: IDTemperature, Value: 0.2}}, "temperature", "0.2"},
		{ProtocolOpenAI, `{"max_completion_tokens":9}`, []Applied{{ID: IDMaxOutput, Value: 64.0}}, "max_tokens", "64"},
		{ProtocolResponses, `{}`, []Applied{{ID: IDMaxOutput, Value: 64.0}}, "max_output_tokens", "64"},
	} {
		got, _, err := Apply([]byte(test.in), test.protocol, test.applied)
		if err != nil {
			t.Fatal(err)
		}
		if v := gjson.GetBytes(got, test.path).String(); v != test.want {
			t.Fatalf("%s %s: %s = %q, want %q (%s)", test.protocol, test.in, test.path, v, test.want, got)
		}
	}
	// Forced standard speed removes whatever tier the caller asked for.
	got, _, _ := Apply([]byte(`{"service_tier":"priority","speed":"fast"}`), ProtocolClaude, []Applied{{ID: IDSpeed, Value: "standard", Via: "tier"}})
	if gjson.GetBytes(got, "service_tier").Exists() || gjson.GetBytes(got, "speed").Exists() {
		t.Fatalf("standard left a tier: %s", got)
	}
	// A forced max output replaces the legacy spelling too.
	got, _, _ = Apply([]byte(`{"max_completion_tokens":9}`), ProtocolOpenAI, []Applied{{ID: IDMaxOutput, Value: 64.0}})
	if gjson.GetBytes(got, "max_completion_tokens").Exists() {
		t.Fatalf("max_completion_tokens survived: %s", got)
	}
	// What a protocol cannot carry is reported, not silently dropped.
	_, skipped, _ := Apply([]byte(`{}`), ProtocolClaude, []Applied{{ID: IDVerbosity, Value: "low"}, {ID: IDReasoningSummary, Value: "auto"}})
	if len(skipped) != 2 {
		t.Fatalf("skipped = %v", skipped)
	}
	// Realized elsewhere: window, suffix and model swap leave the body alone.
	got, _, _ = Apply([]byte(`{"a":1}`), ProtocolOpenAI, []Applied{{ID: IDContext, Value: 300000.0, Via: "window"}, {ID: IDEffort, Value: "high", Via: "suffix"}, {ID: IDSpeed, Value: "fast", Via: "model"}})
	if string(got) != `{"a":1}` {
		t.Fatalf("body changed: %s", got)
	}
}
