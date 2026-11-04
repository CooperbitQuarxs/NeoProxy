package neoparams

import (
	"fmt"
	"slices"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Client protocols a request body can be in.
const (
	ProtocolOpenAI    = "openai"
	ProtocolResponses = "openai-response"
	ProtocolClaude    = "claude"
)

// Caller is what the client sent: the protocol and body, and the reasoning
// effort already extracted from it ("" when none).
type Caller struct {
	Protocol string
	Body     []byte
	Effort   string
}

func (c Caller) exists(path string) bool {
	r := gjson.GetBytes(c.Body, path)
	return r.Exists() && r.Type != gjson.Null && r.String() != ""
}

// Sent reports whether the caller set the parameter itself.
func (c Caller) Sent(id string) bool {
	switch id {
	case IDEffort, IDEfforts:
		return c.Effort != ""
	case IDSpeed:
		return c.exists("service_tier") || (c.Protocol == ProtocolClaude && c.exists("speed"))
	case IDVerbosity:
		if c.Protocol == ProtocolResponses {
			return c.exists("text.verbosity")
		}
		return c.exists("verbosity")
	case IDReasoningSummary:
		return c.exists("reasoning.summary")
	case IDTemperature:
		return c.exists("temperature")
	case IDTopP:
		return c.exists("top_p")
	case IDMaxOutput:
		return c.exists(maxOutputPath(c.Protocol)) || (c.Protocol == ProtocolOpenAI && c.exists("max_completion_tokens"))
	}
	return false
}

func maxOutputPath(protocol string) string {
	if protocol == ProtocolResponses {
		return "max_output_tokens"
	}
	return "max_tokens"
}

func tierWire(value string) string {
	switch value {
	case "fast":
		return "priority"
	case "ultrafast":
		return "ultrafast"
	}
	return ""
}

// Evaluate decides whether a setting's chosen value takes effect for this
// caller and, when it does, what the router will do. mode is force or default:
// default yields to a value the caller sent. A set of efforts only acts when
// the caller's effort is outside it.
func Evaluate(spec Spec, mode string, value any, caller Caller) (Applied, bool) {
	if mode == "" {
		mode = ModeForce
	}
	if mode == ModeDefault && caller.Sent(spec.ID) {
		return Applied{}, false
	}
	switch spec.ID {
	case IDContext:
		n, _ := toNumber(value)
		return Applied{ID: spec.ID, Value: value, Realize: fmt.Sprintf("advertises a %s-token window and refuses larger prompts", formatNumber(n)), Via: "window"}, true
	case IDEffort:
		s, _ := value.(string)
		return Applied{ID: spec.ID, Value: value, Realize: "runs at reasoning effort " + s, Via: "suffix"}, true
	case IDEfforts:
		allowed := Strings(value)
		if len(allowed) == 0 || slices.Contains(allowed, caller.Effort) {
			return Applied{}, false
		}
		highest := allowed[0]
		rank := func(level string) int {
			return slices.IndexFunc(spec.Options, func(o Option) bool { return o.Value == level })
		}
		for _, level := range allowed[1:] {
			if rank(level) > rank(highest) {
				highest = level
			}
		}
		from := "no effort"
		if caller.Effort != "" {
			from = caller.Effort
		}
		return Applied{ID: spec.ID, Value: highest, Realize: fmt.Sprintf("runs at %s (caller asked for %s; allowed: %s)", highest, from, strings.Join(allowed, ", ")), Via: "suffix"}, true
	case IDSpeed:
		s, _ := value.(string)
		if spec.sibling != "" {
			if s == "fast" {
				return Applied{ID: spec.ID, Value: value, Realize: "switches to the fast variant " + spec.sibling, Via: "model"}, true
			}
			return Applied{ID: spec.ID, Value: value, Realize: "serves the standard model", Via: "model"}, true
		}
		if wire := tierWire(s); wire != "" {
			return Applied{ID: spec.ID, Value: value, Realize: "sends service_tier=" + wire, Via: "tier"}, true
		}
		return Applied{ID: spec.ID, Value: value, Realize: "removes any service tier (standard speed)", Via: "tier"}, true
	}
	return Applied{ID: spec.ID, Value: value, Realize: spec.Realize, Via: "body"}, true
}

// Apply writes the applied parameters into the client-protocol request body.
// It runs before translation, so everything downstream, the payload rules last
// of all, still gets the final word. Parameters realized elsewhere (window,
// reasoning suffix, model swap) and fields a protocol cannot carry are left
// out; the second result lists what was skipped and why.
func Apply(body []byte, protocol string, applied []Applied) ([]byte, []string, error) {
	var skipped []string
	set := func(path string, v any) error {
		var err error
		body, err = sjson.SetBytes(body, path, v)
		return err
	}
	del := func(path string) error {
		var err error
		body, err = sjson.DeleteBytes(body, path)
		return err
	}
	for _, a := range applied {
		var err error
		switch a.ID {
		case IDSpeed:
			if a.Via != "tier" {
				continue
			}
			s, _ := a.Value.(string)
			wire := tierWire(s)
			switch {
			case wire == "":
				err = del("service_tier")
				if err == nil && protocol == ProtocolClaude {
					err = del("speed")
				}
			case protocol == ProtocolClaude:
				// The Messages protocol has no tier field of its own: the
				// translator reads service_tier and speed=fast.
				if err = set("service_tier", wire); err == nil {
					err = set("speed", "fast")
				}
				if s == "ultrafast" {
					skipped = append(skipped, "ultrafast is not carried by the Messages protocol; sent as fast")
				}
			default:
				err = set("service_tier", wire)
			}
		case IDVerbosity:
			switch protocol {
			case ProtocolResponses:
				err = set("text.verbosity", a.Value)
			case ProtocolOpenAI:
				err = set("verbosity", a.Value)
			default:
				skipped = append(skipped, "verbosity is not carried by the Messages protocol")
			}
		case IDReasoningSummary:
			if protocol == ProtocolResponses {
				err = set("reasoning.summary", a.Value)
			} else {
				skipped = append(skipped, "reasoning summary is only carried by the Responses protocol")
			}
		case IDTemperature, IDTopP:
			err = set(a.ID, a.Value)
		case IDMaxOutput:
			if protocol == ProtocolOpenAI {
				if err = del("max_completion_tokens"); err != nil {
					return body, skipped, err
				}
			}
			err = set(maxOutputPath(protocol), a.Value)
		}
		if err != nil {
			return body, skipped, fmt.Errorf("apply %s: %w", a.ID, err)
		}
	}
	return body, skipped, nil
}
