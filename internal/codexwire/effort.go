package codexwire

import (
	"sort"
	"strconv"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

const maxEffortControlStates = 16

type effortTransition struct {
	index  int
	anchor string
	effort string
}

// effortControl is OpenAIEffortControlState: the session's first effort is
// pinned at the request level and later changes ride configuration_update items.
type effortControl struct {
	baseEffort    string
	currentEffort string
	transitions   []effortTransition
}

// effortControls is the provider's bounded LRU map of effort states.
type effortControls struct {
	order  []string
	states map[string]*effortControl
}

func (c *effortControls) get(key string) *effortControl {
	if c.states == nil {
		c.states = map[string]*effortControl{}
	}
	if state, ok := c.states[key]; ok {
		for i, existing := range c.order {
			if existing == key {
				c.order = append(c.order[:i], c.order[i+1:]...)
				break
			}
		}
		c.order = append(c.order, key)
		return state
	}
	state := &effortControl{}
	c.states[key] = state
	c.order = append(c.order, key)
	if len(c.order) > maxEffortControlStates {
		delete(c.states, c.order[0])
		c.order = c.order[1:]
	}
	return state
}

// effortAnchor fingerprints input[index-1] without output-only id/status.
func effortAnchor(input []object, index int) string {
	if index == 0 || index > len(input) {
		return ""
	}
	stable := append(object(nil), input[index-1]...)
	stable.del("id")
	stable.del("status")
	return strconv.FormatUint(bunHash(stable.bytes()), 10)
}

// planStableEffort is planStableOpenAIEffort; input gains configuration_update items.
func (state *effortControl) plan(input []object, requested string) ([]object, string) {
	for _, transition := range state.transitions {
		if transition.index > len(input) || transition.anchor != effortAnchor(input, transition.index) {
			*state = effortControl{}
			break
		}
	}
	if state.baseEffort == "" {
		state.baseEffort, state.currentEffort = requested, requested
		return input, requested
	}
	if state.currentEffort != requested {
		index := len(input)
		if index > 0 && input[index-1].get("role").String() == "user" {
			index--
		}
		found := false
		for i := range state.transitions {
			if state.transitions[i].index == index {
				state.transitions[i].effort = requested
				found = true
				break
			}
		}
		if !found {
			state.transitions = append(state.transitions, effortTransition{index: index, anchor: effortAnchor(input, index), effort: requested})
		}
		preceding, precedingIndex := state.baseEffort, -1
		for _, transition := range state.transitions {
			if transition.index < index && transition.index > precedingIndex {
				preceding, precedingIndex = transition.effort, transition.index
			}
		}
		if requested == preceding {
			kept := state.transitions[:0]
			for _, transition := range state.transitions {
				if transition.index != index {
					kept = append(kept, transition)
				}
			}
			state.transitions = kept
		}
		state.currentEffort = requested
	}
	sort.SliceStable(state.transitions, func(a, b int) bool { return state.transitions[a].index < state.transitions[b].index })
	out := append([]object(nil), input...)
	for offset, transition := range state.transitions {
		update := object{
			{Name: "type", Value: jsjson.Marshal("configuration_update")},
			{Name: "reasoning", Value: jsjson.Object(jsjson.Field{Name: "effort", Value: jsjson.Marshal(transition.effort)})},
		}
		at := transition.index + offset
		out = append(out[:at], append([]object{update}, out[at:]...)...)
	}
	return out, state.baseEffort
}

// applyStableEffort is applyCodexStableEffort for a session-scoped request.
func applyStableEffort(controls *effortControls, modelID, sessionID string, body *object) {
	reasoning := body.get("reasoning")
	effort := reasoning.Get("effort")
	if sessionID == "" || effort.Type != gjson.String || effort.String() == "none" || !body.get("input").IsArray() {
		return
	}
	state := controls.get(modelID + "\x00" + sessionID)
	input, base := state.plan(parseItems(body.get("input")), effort.String())
	body.set("input", itemsJSON(input))
	updated := parseObject([]byte(reasoning.Raw))
	updated.set("effort", jsjson.Marshal(base))
	body.set("reasoning", updated.bytes())
}
