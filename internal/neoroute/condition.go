package neoroute

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neopool"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/tidwall/gjson"
)

// Condition is a stage's serving condition: a JSON expression tree evaluated
// natively. Exactly one form is set per node: All, Any, Not, or a leaf
// (Field, Op, Value).
type Condition struct {
	All   []Condition `json:"all,omitempty"`
	Any   []Condition `json:"any,omitempty"`
	Not   *Condition  `json:"not,omitempty"`
	Field string      `json:"field,omitempty"`
	Op    string      `json:"op,omitempty"`
	Value any         `json:"value,omitempty"`
}

// ConditionField documents one field a condition leaf may read. Request-scope
// fields (user, request, time, pool) are evaluated once per stage; account-scope
// fields once per account of the stage.
type ConditionField struct {
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	Scope string `json:"scope"`
	Doc   string `json:"doc"`
}

// ConditionOp documents one leaf operator. Kinds lists the field kinds it
// applies to; Value is what the leaf's value must be: "same" (a value of the
// field's kind), "list" (a non-empty array of values of the field's kind),
// "string", "regex" (an RE2 pattern) or "optional-bool" (omitted or a boolean).
type ConditionOp struct {
	Name  string   `json:"name"`
	Kinds []string `json:"kinds"`
	Value string   `json:"value"`
	Doc   string   `json:"doc"`
}

const (
	conditionMaxDepth  = 8
	conditionMaxLeaves = 64
	conditionMaxString = 4096
	conditionMaxList   = 256
)

// windowKinds are the stage window kinds, in the order meters are reported.
var windowKinds = []string{"5h", "day", "week", "month"}

// ConditionFields is the complete field catalogue, served to the dashboard.
var ConditionFields = []ConditionField{
	{"user.handle", "string", "request", "Handle of the person making the request."},
	{"user.role", "string", "request", "Role of the person making the request (admin, owner, member, service)."},
	{"user.id", "string", "request", "User ID of the person making the request."},
	{"request.model", "string", "request", "The model the caller asked for, e.g. neoproxy/route."},
	{"request.protocol", "string", "request", "Entry protocol: openai (Chat Completions), openai-response (Responses) or claude (Messages)."},
	{"request.effort", "string", "request", "The caller's reasoning effort (minimal, low, medium, high, xhigh, max, none or auto); absent when the caller set none."},
	{"request.stream", "bool", "request", "Whether the caller asked for a streamed response."},
	{"request.prompt", "string", "request", "Text of the last user message that carries text; empty when there is none."},
	{"request.tokens", "number", "request", "Estimated input tokens (request body bytes / 4, rounded up)."},
	{"time.weekday", "string", "request", "Day of the week in server local time: monday … sunday."},
	{"time.hour", "number", "request", "Hour of the day in server local time, 0-23."},
	{"time.date", "string", "request", "Date in server local time, YYYY-MM-DD (compare with lt/gt for ranges)."},
	{"account.label", "string", "account", "The account's label."},
	{"account.provider", "string", "account", "The account's provider ID."},
	{"account.plan", "string", "account", "The account's plan; absent when unknown."},
	{"account.state", "string", "account", "The account's state (healthy, …). Only healthy accounts reach a condition."},
	{"account.metered", "bool", "account", "Whether the account has a meter for the stage's window."},
	{"account.usage", "number", "account", "Used fraction (0-1) of the account's meter for the stage's window; absent when unmetered."},
	{"account.usage.5h", "number", "account", "Used fraction (0-1) of the account's 5-hour meter; absent when it has none."},
	{"account.usage.day", "number", "account", "Used fraction (0-1) of the account's daily meter; absent when it has none."},
	{"account.usage.week", "number", "account", "Used fraction (0-1) of the account's weekly meter; absent when it has none."},
	{"account.usage.month", "number", "account", "Used fraction (0-1) of the account's monthly meter; absent when it has none."},
	{"pool.size", "number", "request", "How many of the stage's accounts could serve the request before the condition is applied."},
	{"pool.average", "number", "request", "Average used fraction (0-1) across the stage's metered accounts for the stage's window (the value compared with cap); absent when none is metered."},
	{"pool.average.5h", "number", "request", "Average used fraction (0-1) across the stage's accounts with a 5-hour meter; absent when none has one."},
	{"pool.average.day", "number", "request", "Average used fraction (0-1) across the stage's accounts with a daily meter; absent when none has one."},
	{"pool.average.week", "number", "request", "Average used fraction (0-1) across the stage's accounts with a weekly meter; absent when none has one."},
	{"pool.average.month", "number", "request", "Average used fraction (0-1) across the stage's accounts with a monthly meter; absent when none has one."},
}

// ConditionOps is the complete operator catalogue, served to the dashboard.
var ConditionOps = []ConditionOp{
	{"eq", []string{"string", "number", "bool"}, "same", "Equal (strings are case-sensitive)."},
	{"neq", []string{"string", "number", "bool"}, "same", "Present and not equal."},
	{"lt", []string{"string", "number"}, "same", "Less than (strings compare lexicographically)."},
	{"lte", []string{"string", "number"}, "same", "Less than or equal."},
	{"gt", []string{"string", "number"}, "same", "Greater than."},
	{"gte", []string{"string", "number"}, "same", "Greater than or equal."},
	{"in", []string{"string", "number"}, "list", "Equal to one of the listed values."},
	{"nin", []string{"string", "number"}, "list", "Present and equal to none of the listed values."},
	{"contains", []string{"string", "list"}, "string", "String contains the substring (case-sensitive), or list contains the value."},
	{"matches", []string{"string"}, "regex", "Matches the RE2 regular expression (use (?i) for case-insensitive)."},
	{"exists", []string{"string", "number", "bool", "list"}, "optional-bool", "The field has a value; value false asks for absence."},
}

// EffortLevels are the reasoning efforts a stage may force or allow, lowest first.
var EffortLevels = []string{"minimal", "low", "medium", "high", "xhigh", "max"}

var conditionFieldIndex = func() map[string]ConditionField {
	index := make(map[string]ConditionField, len(ConditionFields))
	for _, field := range ConditionFields {
		index[field.Name] = field
	}
	return index
}()

var conditionOpIndex = func() map[string]ConditionOp {
	index := make(map[string]ConditionOp, len(ConditionOps))
	for _, op := range ConditionOps {
		index[op.Name] = op
	}
	return index
}()

// Patterns come only from validated stored configuration.
var conditionRegexps sync.Map

func conditionRegexp(pattern string) (*regexp.Regexp, error) {
	if re, ok := conditionRegexps.Load(pattern); ok {
		return re.(*regexp.Regexp), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	conditionRegexps.Store(pattern, re)
	return re, nil
}

// validateCondition checks shape, fields, operators, values, depth and size.
func validateCondition(c *Condition) error {
	leaves := 0
	return c.validate(1, &leaves)
}

func (c *Condition) validate(depth int, leaves *int) error {
	if depth > conditionMaxDepth {
		return fmt.Errorf("condition nests deeper than %d levels", conditionMaxDepth)
	}
	forms := 0
	for _, set := range []bool{c.All != nil, c.Any != nil, c.Not != nil, c.Field != "" || c.Op != "" || c.Value != nil} {
		if set {
			forms++
		}
	}
	if forms != 1 {
		return fmt.Errorf("condition node must be exactly one of all, any, not or a field/op/value leaf")
	}
	for name, group := range map[string][]Condition{"all": c.All, "any": c.Any} {
		if group == nil {
			continue
		}
		if len(group) == 0 {
			return fmt.Errorf("condition %q group must not be empty", name)
		}
		for i := range group {
			if err := group[i].validate(depth+1, leaves); err != nil {
				return err
			}
		}
		return nil
	}
	if c.Not != nil {
		return c.Not.validate(depth+1, leaves)
	}
	*leaves++
	if *leaves > conditionMaxLeaves {
		return fmt.Errorf("condition has more than %d comparisons", conditionMaxLeaves)
	}
	field, ok := conditionFieldIndex[c.Field]
	if !ok {
		return fmt.Errorf("condition: unknown field %q", c.Field)
	}
	op, ok := conditionOpIndex[c.Op]
	if !ok {
		return fmt.Errorf("condition: unknown op %q", c.Op)
	}
	if !slices.Contains(op.Kinds, field.Kind) {
		return fmt.Errorf("condition: op %q does not apply to %s field %q", c.Op, field.Kind, c.Field)
	}
	switch op.Value {
	case "same":
		if !valueOfKind(c.Value, field.Kind) {
			return fmt.Errorf("condition: %q %s needs a %s value", c.Field, c.Op, field.Kind)
		}
	case "list":
		list, ok := c.Value.([]any)
		if !ok || len(list) == 0 || len(list) > conditionMaxList {
			return fmt.Errorf("condition: %q %s needs a list of 1..%d %s values", c.Field, c.Op, conditionMaxList, field.Kind)
		}
		for _, item := range list {
			if !valueOfKind(item, field.Kind) {
				return fmt.Errorf("condition: %q %s needs a list of %s values", c.Field, c.Op, field.Kind)
			}
		}
	case "string":
		if !valueOfKind(c.Value, "string") {
			return fmt.Errorf("condition: %q %s needs a string value", c.Field, c.Op)
		}
	case "regex":
		pattern, ok := c.Value.(string)
		if !ok || len(pattern) > 1024 {
			return fmt.Errorf("condition: %q matches needs a regular expression string of at most 1024 bytes", c.Field)
		}
		if _, err := conditionRegexp(pattern); err != nil {
			return fmt.Errorf("condition: %q matches has an invalid regular expression: %w", c.Field, err)
		}
	case "optional-bool":
		if _, ok := c.Value.(bool); c.Value != nil && !ok {
			return fmt.Errorf("condition: %q exists takes no value or a boolean", c.Field)
		}
	}
	return nil
}

func valueOfKind(v any, kind string) bool {
	switch kind {
	case "string":
		s, ok := v.(string)
		return ok && len(s) <= conditionMaxString
	case "number":
		_, ok := conditionNumber(v)
		return ok
	case "bool":
		_, ok := v.(bool)
		return ok
	}
	return false
}

func conditionNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// usesAccount reports whether any leaf reads an account-scope field.
func (c *Condition) usesAccount() bool {
	if c.Not != nil {
		return c.Not.usesAccount()
	}
	for _, group := range [][]Condition{c.All, c.Any} {
		for i := range group {
			if group[i].usesAccount() {
				return true
			}
		}
	}
	return conditionFieldIndex[c.Field].Scope == "account"
}

func (c *Condition) eval(x *conditionScope) bool {
	switch {
	case c.All != nil:
		for i := range c.All {
			if !c.All[i].eval(x) {
				return false
			}
		}
		return true
	case c.Any != nil:
		for i := range c.Any {
			if c.Any[i].eval(x) {
				return true
			}
		}
		return false
	case c.Not != nil:
		return !c.Not.eval(x)
	}
	v, ok := x.lookup(c.Field)
	if c.Op == "exists" {
		want, isBool := c.Value.(bool)
		return ok == (want || !isBool)
	}
	if !ok {
		return false
	}
	switch c.Op {
	case "eq":
		return conditionEqual(v, c.Value)
	case "neq":
		return !conditionEqual(v, c.Value)
	case "lt", "lte", "gt", "gte":
		order, ok := conditionCompare(v, c.Value)
		if !ok {
			return false
		}
		switch c.Op {
		case "lt":
			return order < 0
		case "lte":
			return order <= 0
		case "gt":
			return order > 0
		}
		return order >= 0
	case "in", "nin":
		list, _ := c.Value.([]any)
		found := slices.ContainsFunc(list, func(item any) bool { return conditionEqual(v, item) })
		return found == (c.Op == "in")
	case "contains":
		switch v := v.(type) {
		case string:
			needle, ok := c.Value.(string)
			return ok && strings.Contains(v, needle)
		case []any:
			return slices.ContainsFunc(v, func(item any) bool { return conditionEqual(item, c.Value) })
		}
		return false
	case "matches":
		s, isString := v.(string)
		pattern, _ := c.Value.(string)
		re, err := conditionRegexp(pattern)
		return isString && err == nil && re.MatchString(s)
	}
	return false
}

func conditionEqual(a, b any) bool {
	if x, ok := conditionNumber(a); ok {
		y, ok := conditionNumber(b)
		return ok && x == y
	}
	switch a := a.(type) {
	case string:
		s, ok := b.(string)
		return ok && a == s
	case bool:
		v, ok := b.(bool)
		return ok && a == v
	}
	return false
}

func conditionCompare(a, b any) (int, bool) {
	if x, ok := conditionNumber(a); ok {
		y, ok := conditionNumber(b)
		if !ok {
			return 0, false
		}
		switch {
		case x < y:
			return -1, true
		case x > y:
			return 1, true
		}
		return 0, true
	}
	x, okA := a.(string)
	y, okB := b.(string)
	if !okA || !okB {
		return 0, false
	}
	return strings.Compare(x, y), true
}

// requestFacts are the request-scope inputs shared by every stage of one
// decision. The prompt is extracted only when a condition reads it.
type requestFacts struct {
	req      Request
	effort   string
	now      time.Time
	prompt   string
	prompted bool
}

func newRequestFacts(req Request, state routeState) *requestFacts {
	now := state.now
	if now.IsZero() {
		now = time.UnixMilli(state.at)
	}
	return &requestFacts{req: req, effort: thinking.ExtractReasoningEffort(req.Body, req.Protocol, req.Asked), now: now}
}

func (f *requestFacts) lastPrompt() string {
	if !f.prompted {
		f.prompt = PromptText(f.req.Body)
		f.prompted = true
	}
	return f.prompt
}

// PromptText returns the text of the last user message that carries text in a
// Chat Completions, Responses or Messages body; "" when there is none. Tool
// results and media parts are not text.
func PromptText(body []byte) string {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ""
	}
	root := gjson.ParseBytes(body)
	if input := root.Get("input"); input.Type == gjson.String {
		return input.String()
	} else if input.IsArray() {
		if text := lastUserText(input.Array()); text != "" {
			return text
		}
	}
	return lastUserText(root.Get("messages").Array())
}

func lastUserText(messages []gjson.Result) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Get("role").String() != "user" {
			continue
		}
		if text := textOf(messages[i].Get("content")); text != "" {
			return text
		}
	}
	return ""
}

// poolFacts holds the stage-level account aggregates a condition may read.
type poolFacts struct {
	size    int
	average [4]float64
	known   [4]uint8 // 0 not computed, 1 absent, 2 present
}

// conditionScope resolves fields for one stage; account is nil at stage scope.
type conditionScope struct {
	facts   *requestFacts
	state   *routeState
	vm      string
	stage   *Stage
	model   neopool.Model
	pool    *poolFacts
	account *neopool.Account
}

func windowIndex(kind string) int { return slices.Index(windowKinds, kind) }

func (x *conditionScope) inScope(a neopool.Account) bool {
	return inStageScope(*x.state, *x.stage, x.model, a)
}

// usage is the account's stage-semantics used fraction of its meter of kind.
func (x *conditionScope) usage(a neopool.Account, kind string) (float64, bool) {
	w, provider, metered := windowFor(a, kind, x.state.at)
	if !metered {
		return 0, false
	}
	st := *x.stage
	st.Window = kind
	return stageUsage(*x.state, x.vm, st, a, w, provider), true
}

func (x *conditionScope) poolAverage(kind string) (float64, bool) {
	i := windowIndex(kind)
	if i < 0 {
		return 0, false
	}
	if x.pool.known[i] == 0 {
		x.pool.known[i] = 1
		sum, n := float64(0), 0
		for _, a := range x.state.snapshot.Accounts {
			if !x.inScope(a) {
				continue
			}
			if used, ok := x.usage(a, kind); ok {
				sum += used
				n++
			}
		}
		if n > 0 {
			x.pool.average[i] = sum / float64(n)
			x.pool.known[i] = 2
		}
	}
	return x.pool.average[i], x.pool.known[i] == 2
}

func (x *conditionScope) lookup(field string) (any, bool) {
	p := x.state.principal
	f := x.facts
	switch field {
	case "user.handle":
		return x.state.handle, x.state.handle != ""
	case "user.role":
		return p.Role, p.Role != ""
	case "user.id":
		return p.UserID, p.UserID != ""
	case "request.model":
		return f.req.Asked, true
	case "request.protocol":
		return f.req.Protocol, f.req.Protocol != ""
	case "request.effort":
		return f.effort, f.effort != ""
	case "request.stream":
		return f.req.Stream, true
	case "request.prompt":
		return f.lastPrompt(), true
	case "request.tokens":
		return float64(f.tokens()), true
	case "time.weekday":
		return strings.ToLower(f.now.Weekday().String()), true
	case "time.hour":
		return float64(f.now.Hour()), true
	case "time.date":
		return f.now.Format(time.DateOnly), true
	case "pool.size":
		return float64(x.pool.size), true
	case "pool.average":
		return x.poolAverage(x.stage.Window)
	}
	if kind, ok := strings.CutPrefix(field, "pool.average."); ok {
		return x.poolAverage(kind)
	}
	a := x.account
	if a == nil {
		return nil, false
	}
	switch field {
	case "account.label":
		return a.Label, true
	case "account.provider":
		return a.ProviderID, true
	case "account.plan":
		return a.Plan, a.Plan != ""
	case "account.state":
		return a.State, true
	case "account.metered":
		_, _, metered := windowFor(*a, x.stage.Window, x.state.at)
		return metered, true
	case "account.usage":
		return x.usage(*a, x.stage.Window)
	}
	if kind, ok := strings.CutPrefix(field, "account.usage."); ok && windowIndex(kind) >= 0 {
		return x.usage(*a, kind)
	}
	return nil, false
}

func effortRank(level string) int { return slices.Index(EffortLevels, level) }

// stageEffort is the effort a stage applies: Effort forces one; otherwise a
// caller effort within Efforts passes through unchanged (nil: the body keeps
// it) and any other, or none, becomes the highest allowed.
func stageEffort(st Stage, caller string) *string {
	if st.Effort != nil || len(st.Efforts) == 0 {
		return st.Effort
	}
	if slices.Contains(st.Efforts, caller) {
		return nil
	}
	highest := st.Efforts[0]
	for _, level := range st.Efforts[1:] {
		if effortRank(level) > effortRank(highest) {
			highest = level
		}
	}
	return &highest
}

func validateEfforts(st Stage) error {
	if st.Effort != nil && effortRank(*st.Effort) < 0 {
		return fmt.Errorf("stage %q effort %q is not one of %s", st.ID, *st.Effort, strings.Join(EffortLevels, ", "))
	}
	seen := make(map[string]bool, len(st.Efforts))
	for _, level := range st.Efforts {
		if effortRank(level) < 0 {
			return fmt.Errorf("stage %q effort %q is not one of %s", st.ID, level, strings.Join(EffortLevels, ", "))
		}
		if seen[level] {
			return fmt.Errorf("stage %q lists effort %q twice", st.ID, level)
		}
		seen[level] = true
	}
	return nil
}
