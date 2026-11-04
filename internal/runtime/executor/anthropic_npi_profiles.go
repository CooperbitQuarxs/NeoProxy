package executor

import (
	_ "embed"
	"encoding/json"
)

// The comparison fixture is also the experimental provider's capability pin.
// It is captured by buildModel in NeoPi, rather than inheriting claude's model
// heuristics. Updating it requires repeating the reference capture.
//
//go:embed testdata/anthropic_npi_profiles.json
var neoPiProfileJSON []byte

type neoPiProfile struct {
	Thinking struct {
		Mode            string `json:"mode"`
		SupportsDisplay bool   `json:"supportsDisplay"`
		PrefixBinding   bool   `json:"prefixBinding"`
	} `json:"thinking"`
	Compat struct {
		SupportsContextManagement          bool   `json:"supportsContextManagement"`
		SupportsEagerToolInputStreaming    bool   `json:"supportsEagerToolInputStreaming"`
		SupportsLongCacheRetention         bool   `json:"supportsLongCacheRetention"`
		SupportsTurnScopedSystem           bool   `json:"supportsTurnScopedSystem"`
		SupportsMidConversationToolChanges bool   `json:"supportsMidConversationToolChanges"`
		SupportsPerMessageEffort           bool   `json:"supportsPerMessageEffort"`
		SupportsThinkingBindingControls    bool   `json:"supportsThinkingBindingControls"`
		SupportsBetweenToolsThinking       bool   `json:"supportsBetweenToolsThinking"`
		SupportsForcedToolChoice           bool   `json:"supportsForcedToolChoice"`
		SupportsSamplingParams             bool   `json:"supportsSamplingParams"`
		InjectClaudeCodeInstruction        bool   `json:"injectClaudeCodeInstruction"`
		DisabledThinking                   string `json:"disabledThinking"`
	} `json:"compat"`
}

var neoPiProfiles = func() map[string]neoPiProfile {
	var profiles map[string]neoPiProfile
	if err := json.Unmarshal(neoPiProfileJSON, &profiles); err != nil {
		panic(err)
	}
	return profiles
}()
