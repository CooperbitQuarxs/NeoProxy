package neoroute

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoparams"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Applied is a parameter value the virtual model set on a request.
type Applied = neoparams.Applied

// ParamRule is one value a parameter may take and the condition under which it
// does. A rule without When is the fallback and must come last.
type ParamRule struct {
	When  *Condition `json:"when,omitempty"`
	Value any        `json:"value"`
}

// ParamSetting is one always-set property of a stage's model. Mode is "force"
// (overrides whatever the caller sent) or "default" (applies only when the
// caller sent nothing for it). The first rule whose When matches wins.
type ParamSetting struct {
	Mode  string      `json:"mode"`
	Rules []ParamRule `json:"rules"`
}

const (
	maxParamRules = 16
	maxParams     = 32
)

// modelInfo returns the registry facts of a model. Tests replace it.
var modelInfo = func(m neopool.Model) *registry.ModelInfo {
	return registry.GetGlobalRegistry().GetModelInfo(m.Name, m.ProviderID)
}

// specsFor derives the parameters of model m from the inventory's models.
func specsFor(models []neopool.Model, m neopool.Model) []neoparams.Spec {
	return neoparams.Catalogue(m, modelInfo(m), models)
}

// modelParams is the parameter catalogue of one inventory model.
func modelParams(models []neopool.Model, id string) ([]neoparams.Spec, bool) {
	i := slices.IndexFunc(models, func(m neopool.Model) bool { return m.ID == id })
	if i < 0 {
		return nil, false
	}
	specs := specsFor(models, models[i])
	if specs == nil {
		specs = []neoparams.Spec{}
	}
	return specs, true
}

var versionRun = regexp.MustCompile(`\d+`)

// versionKey lists the numbers in a model name: "gpt-6.1-sol" is [6 1].
func versionKey(name string) []int {
	runs := versionRun.FindAllString(name, -1)
	out := make([]int, 0, len(runs))
	for _, r := range runs {
		n, err := strconv.Atoi(r)
		if err != nil {
			n = 0
		}
		out = append(out, n)
	}
	return out
}

func compareVersions(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return len(a) - len(b)
}

// newestMatch finds the exposed model whose ID matches the glob and has the
// highest version; ties go to the greater ID. `*` does not cross `/`.
func newestMatch(snap neopool.Snapshot, pattern string) (neopool.Model, bool) {
	var best neopool.Model
	found := false
	for _, m := range snap.Models {
		if !m.Exposed || !providerVisible(snap, m.ProviderID) {
			continue
		}
		if ok, err := path.Match(pattern, m.ID); err != nil || !ok {
			continue
		}
		if !found {
			best, found = m, true
			continue
		}
		c := compareVersions(versionKey(m.Name), versionKey(best.Name))
		if c > 0 || (c == 0 && m.ID > best.ID) {
			best = m
		}
	}
	return best, found
}

// resolveFollows fills in the model of every stage that follows a pattern with
// the newest model matching it. A stage whose pattern matches nothing keeps its
// last resolved model. The input is not modified.
func resolveFollows(vm VirtualModel, snap neopool.Snapshot) VirtualModel {
	if !slices.ContainsFunc(vm.Stages, func(st Stage) bool { return st.Follow != "" }) {
		return vm
	}
	vm.Stages = slices.Clone(vm.Stages)
	for i, st := range vm.Stages {
		if st.Follow == "" {
			continue
		}
		if m, ok := newestMatch(snap, st.Follow); ok {
			vm.Stages[i].Model = m.ID
		}
	}
	return vm
}

// conditionFieldsOK walks a params condition and rejects fields that need an
// account or pool, which do not exist before a stage picks its accounts.
func conditionFieldsOK(c *Condition) error {
	if c.Not != nil {
		return conditionFieldsOK(c.Not)
	}
	for _, group := range [][]Condition{c.All, c.Any} {
		for i := range group {
			if err := conditionFieldsOK(&group[i]); err != nil {
				return err
			}
		}
	}
	if c.Field == "" {
		return nil
	}
	if f := conditionFieldIndex[c.Field]; f.Scope != "request" || strings.HasPrefix(c.Field, "pool.") {
		return fmt.Errorf("field %q needs accounts; a parameter condition may read only request, user and time fields", c.Field)
	}
	return nil
}

// validateParams checks a stage's settings against the specs of its model.
func validateParams(st Stage, inventory validationInventory) error {
	if len(st.Params) == 0 {
		return nil
	}
	if len(st.Params) > maxParams {
		return fmt.Errorf("stage %q sets more than %d parameters", st.ID, maxParams)
	}
	model := inventory.models[st.Model]
	if model == nil {
		return fmt.Errorf("unknown model %q", st.Model)
	}
	specs := specsFor(inventory.list, *model)
	ids := make([]string, 0, len(st.Params))
	for id := range st.Params {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		setting := st.Params[id]
		spec, ok := neoparams.ByID(specs, id)
		if !ok {
			return fmt.Errorf("stage %q: %s is not a parameter of %s", st.ID, id, st.Model)
		}
		if setting.Mode != "" && setting.Mode != neoparams.ModeForce && setting.Mode != neoparams.ModeDefault {
			return fmt.Errorf("stage %q: %s mode must be force or default", st.ID, id)
		}
		if !spec.AllowsMode(setting.Mode) {
			return fmt.Errorf("stage %q: %s is always enforced and cannot be a default", st.ID, id)
		}
		if len(setting.Rules) == 0 || len(setting.Rules) > maxParamRules {
			return fmt.Errorf("stage %q: %s needs 1..%d rules", st.ID, id, maxParamRules)
		}
		for i, rule := range setting.Rules {
			if _, err := spec.Check(rule.Value); err != nil {
				return fmt.Errorf("stage %q: %w", st.ID, err)
			}
			if rule.When == nil {
				if i != len(setting.Rules)-1 {
					return fmt.Errorf("stage %q: %s has a rule after its unconditional rule, which can never apply", st.ID, id)
				}
				continue
			}
			if err := validateCondition(rule.When); err != nil {
				return fmt.Errorf("stage %q: %s rule %d: %w", st.ID, id, i+1, err)
			}
			if err := conditionFieldsOK(rule.When); err != nil {
				return fmt.Errorf("stage %q: %s rule %d: %w", st.ID, id, i+1, err)
			}
		}
	}
	return nil
}

// ruleValue returns the value of the first rule that matches.
func (p ParamSetting) ruleValue(scope *conditionScope) (any, bool) {
	for _, r := range p.Rules {
		if r.When == nil || r.When.eval(scope) {
			return r.Value, true
		}
	}
	return nil, false
}

// stageParams is what a stage's parameters do to one request: the applied
// list, the model that serves it (a sibling when speed swaps models) and the
// imposed context window (0: the model's own).
type stageParams struct {
	applied []Applied
	model   neopool.Model
	window  int
}

func evalStageParams(state routeState, vm VirtualModel, st Stage, m neopool.Model, facts *requestFacts) stageParams {
	out := stageParams{model: m}
	if len(st.Params) == 0 || facts == nil {
		return out
	}
	specs := specsFor(state.snapshot.Models, m)
	caller := neoparams.Caller{Protocol: facts.req.Protocol, Body: facts.req.Body, Effort: facts.effort}
	scope := &conditionScope{facts: facts, state: &state, vm: vm.ID, stage: &st, model: m, pool: &poolFacts{}}
	effortApplied := false
	for _, spec := range specs {
		setting, ok := st.Params[spec.ID]
		if !ok {
			continue
		}
		value, ok := setting.ruleValue(scope)
		if !ok {
			continue
		}
		if spec.ID == neoparams.IDEfforts && effortApplied {
			continue
		}
		applied, ok := neoparams.Evaluate(spec, setting.Mode, value, caller)
		if !ok {
			continue
		}
		switch spec.ID {
		case neoparams.IDEffort:
			effortApplied = true
		case neoparams.IDContext:
			if n, isNumber := value.(float64); isNumber {
				out.window = int(n)
			}
		case neoparams.IDSpeed:
			if spec.Sibling() != "" && value == "fast" {
				if sibling, found := exposedModel(state.snapshot, spec.Sibling()); found {
					out.model = sibling
					applied.Realize = "switches to " + sibling.ID
				} else {
					continue
				}
			}
		}
		out.applied = append(out.applied, applied)
	}
	return out
}

// appliedEffort is the reasoning effort among applied parameters.
func appliedEffort(applied []Applied) *string {
	for _, a := range applied {
		if a.ID == neoparams.IDEffort || a.ID == neoparams.IDEfforts {
			if s, ok := a.Value.(string); ok {
				return &s
			}
		}
	}
	return nil
}

// servedWindow is the context window a virtual model advertises: the largest
// value its first stage's context setting can take (the model's standard
// window when no unconditional rule covers every request), or the model's own.
func servedWindow(vm VirtualModel, snap neopool.Snapshot) int {
	if len(vm.Stages) == 0 {
		return 0
	}
	st := resolveFollows(vm, snap).Stages[0]
	m, ok := exposedModel(snap, st.Model)
	if !ok {
		return 0
	}
	standard := m.Context
	if spec, found := neoparams.ByID(specsFor(snap.Models, m), neoparams.IDContext); found {
		if n, isNumber := spec.Default.(int); isNumber {
			standard = n
		}
		setting, set := st.Params[neoparams.IDContext]
		if !set {
			return standard
		}
		widest, covered := 0, false
		for _, r := range setting.Rules {
			if n, isNumber := r.Value.(float64); isNumber {
				widest = max(widest, int(n))
			}
			if r.When == nil {
				covered = true
			}
		}
		if !covered {
			widest = max(widest, standard)
		}
		return widest
	}
	return standard
}

// paramSummary describes a stage's unconditional parameters in a few words for
// model listings, for example "Fast, 300k context".
func paramSummary(vm VirtualModel, snap neopool.Snapshot) string {
	if len(vm.Stages) == 0 {
		return ""
	}
	st := resolveFollows(vm, snap).Stages[0]
	m, ok := exposedModel(snap, st.Model)
	if !ok {
		return ""
	}
	var parts []string
	for _, spec := range specsFor(snap.Models, m) {
		setting, set := st.Params[spec.ID]
		if !set || len(setting.Rules) != 1 || setting.Rules[0].When != nil {
			continue
		}
		value := setting.Rules[0].Value
		switch spec.ID {
		case neoparams.IDSpeed:
			for _, o := range spec.Options {
				if o.Value == value {
					parts = append(parts, o.Label+" speed")
				}
			}
		case neoparams.IDContext:
			if n, isNumber := value.(float64); isNumber {
				parts = append(parts, compactTokens(int(n))+" context")
			}
		case neoparams.IDEffort:
			parts = append(parts, fmt.Sprint(value)+" effort")
		case neoparams.IDEfforts:
			parts = append(parts, "efforts "+strings.Join(neoparams.Strings(value), "/"))
		}
	}
	return strings.Join(parts, ", ")
}

func compactTokens(n int) string {
	switch {
	case n >= 1000000 && n%100000 == 0:
		return strconv.FormatFloat(float64(n)/1e6, 'f', -1, 64) + "M"
	case n >= 1000 && n%1000 == 0:
		return strconv.Itoa(n/1000) + "k"
	}
	return strconv.Itoa(n)
}

// ModelDetail is what /v1/models reports about a served model beyond its ID.
type ModelDetail struct {
	ContextLength int
	Description   string
}

// modelDetails describes every model Models lists, keyed by served ID.
func (s *Store) modelDetails(snap neopool.Snapshot, vms []VirtualModel) map[string]ModelDetail {
	out := make(map[string]ModelDetail, len(snap.Models)+len(vms))
	for _, m := range snap.Models {
		if m.Context > 0 {
			out[m.Served] = ModelDetail{ContextLength: m.Context}
		}
	}
	for _, vm := range vms {
		d := ModelDetail{ContextLength: servedWindow(vm, snap), Description: vm.Description}
		if summary := paramSummary(vm, snap); summary != "" {
			if d.Description != "" {
				d.Description += " — "
			}
			d.Description += summary
		}
		out[vm.Served] = d
	}
	return out
}

// canonicalParams gives every setting an explicit mode and drops empty maps.
// The input is not modified.
func canonicalParams(vm VirtualModel) VirtualModel {
	vm.Stages = slices.Clone(vm.Stages)
	for i, st := range vm.Stages {
		if len(st.Params) == 0 {
			vm.Stages[i].Params = nil
			continue
		}
		params := make(map[string]ParamSetting, len(st.Params))
		for id, setting := range st.Params {
			if setting.Mode == "" {
				setting.Mode = neoparams.ModeForce
			}
			params[id] = setting
		}
		vm.Stages[i].Params = params
	}
	return vm
}

// migrateEffortsToParams rewrites stored virtual models from the v2 shape, where
// a stage forced one reasoning effort (effort) or allowed a set (efforts), to
// the v3 shape, where both are parameters.
func migrateEffortsToParams(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id,body FROM neoroute_virtual_models`)
	if err != nil {
		return err
	}
	type stored struct {
		id   string
		body []byte
	}
	var all []stored
	for rows.Next() {
		var r stored
		if err = rows.Scan(&r.id, &r.body); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range all {
		var doc map[string]json.RawMessage
		if err = json.Unmarshal(r.body, &doc); err != nil {
			return fmt.Errorf("virtual model %s: %w", r.id, err)
		}
		var stages []map[string]json.RawMessage
		if err = json.Unmarshal(doc["stages"], &stages); err != nil {
			return fmt.Errorf("virtual model %s stages: %w", r.id, err)
		}
		changed := false
		for _, st := range stages {
			params := map[string]ParamSetting{}
			if raw, ok := st["params"]; ok {
				if err = json.Unmarshal(raw, &params); err != nil {
					return fmt.Errorf("virtual model %s params: %w", r.id, err)
				}
			}
			if raw, ok := st["effort"]; ok {
				delete(st, "effort")
				changed = true
				var effort *string
				if err = json.Unmarshal(raw, &effort); err != nil {
					return fmt.Errorf("virtual model %s effort: %w", r.id, err)
				}
				if effort != nil {
					params[neoparams.IDEffort] = ParamSetting{Mode: neoparams.ModeForce, Rules: []ParamRule{{Value: *effort}}}
				}
			}
			if raw, ok := st["efforts"]; ok {
				delete(st, "efforts")
				changed = true
				var efforts []string
				if err = json.Unmarshal(raw, &efforts); err != nil {
					return fmt.Errorf("virtual model %s efforts: %w", r.id, err)
				}
				if len(efforts) > 0 {
					params[neoparams.IDEfforts] = ParamSetting{Mode: neoparams.ModeForce, Rules: []ParamRule{{Value: efforts}}}
				}
			}
			if len(params) > 0 {
				if st["params"], err = json.Marshal(params); err != nil {
					return err
				}
			}
		}
		if !changed {
			continue
		}
		if doc["stages"], err = json.Marshal(stages); err != nil {
			return err
		}
		body, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE neoroute_virtual_models SET body=? WHERE id=?`, body, r.id); err != nil {
			return err
		}
	}
	return nil
}
