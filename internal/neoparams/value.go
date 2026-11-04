package neoparams

import (
	"fmt"
	"math"
	"slices"
	"strconv"
)

func toNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, !math.IsNaN(n) && !math.IsInf(n, 0)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func sameValue(a, b any) bool {
	if x, ok := toNumber(a); ok {
		y, ok := toNumber(b)
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

func (s Spec) hasOption(v any) bool {
	return slices.ContainsFunc(s.Options, func(o Option) bool { return sameValue(o.Value, v) })
}

// Check validates a value against the spec and returns its canonical form:
// string for choices, float64 for ranges and numbers, bool for toggles, and
// []string for sets of strings.
func (s Spec) Check(v any) (any, error) {
	switch s.Kind {
	case KindChoice:
		if !s.hasOption(v) {
			return nil, fmt.Errorf("%s: %v is not one of %s", s.ID, v, s.optionList())
		}
		return v, nil
	case KindToggle:
		b, ok := v.(bool)
		if !ok {
			return nil, fmt.Errorf("%s needs true or false", s.ID)
		}
		return b, nil
	case KindRange, KindNumber:
		n, ok := toNumber(v)
		if !ok {
			return nil, fmt.Errorf("%s needs a number", s.ID)
		}
		if n < s.Min || n > s.Max {
			return nil, fmt.Errorf("%s: %s is outside %s..%s", s.ID, formatNumber(n), formatNumber(s.Min), formatNumber(s.Max))
		}
		return n, nil
	case KindSet:
		var items []any
		switch list := v.(type) {
		case []any:
			items = list
		case []string:
			for _, x := range list {
				items = append(items, x)
			}
		default:
			return nil, fmt.Errorf("%s needs a list", s.ID)
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("%s needs at least one value", s.ID)
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			str, ok := item.(string)
			if !ok || !s.hasOption(item) {
				return nil, fmt.Errorf("%s: %v is not one of %s", s.ID, item, s.optionList())
			}
			if slices.Contains(out, str) {
				return nil, fmt.Errorf("%s lists %q twice", s.ID, str)
			}
			out = append(out, str)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s has unsupported kind %q", s.ID, s.Kind)
}

func (s Spec) optionList() string {
	out := ""
	for i, o := range s.Options {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprint(o.Value)
	}
	return out
}

func formatNumber(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

// Strings converts a stored set value to strings.
func Strings(v any) []string {
	switch list := v.(type) {
	case []string:
		return list
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
