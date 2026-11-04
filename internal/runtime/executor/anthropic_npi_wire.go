package executor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"unicode/utf16"

	"github.com/google/uuid"
	xxHash64 "github.com/pierrec/xxHash/xxHash64"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/anthropicnpi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// This is a separate wire implementation, not a mode of ClaudeExecutor. Its
// order-sensitive billing signature and request profile follow NeoPi's
// packages/ai/src/providers/anthropic.ts and anthropic-identity.ts.
const neoPiClaudeInstruction = "You are Claude Code, Anthropic's official CLI for Claude."

type neoPiWireIdentity struct {
	InstallID string
	AccountID string
	SessionID string
}

type neoPiField struct {
	name  string
	value json.RawMessage
}

func neoPiJSON(value any) json.RawMessage {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(value)
	return bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
}

func neoPiObject(fields ...neoPiField) json.RawMessage {
	out := []byte{'{'}
	for _, field := range fields {
		if len(field.value) == 0 {
			continue
		}
		if len(out) > 1 {
			out = append(out, ',')
		}
		out = append(out, neoPiJSON(field.name)...)
		out = append(out, ':')
		out = append(out, field.value...)
	}
	return append(out, '}')
}

func neoPiSet(body []byte, path string, value any) []byte {
	out, _ := sjson.SetRawBytes(body, path, neoPiJSON(value))
	return out
}

func neoPiSetRaw(body []byte, path string, value []byte) []byte {
	out, _ := sjson.SetRawBytes(body, path, value)
	return out
}

func neoPiDelete(body []byte, path string) []byte {
	out, _ := sjson.DeleteBytes(body, path)
	return out
}

func neoPiOfficialEndpoint(base string) bool {
	u, err := url.Parse(base)
	return err == nil && strings.EqualFold(u.Hostname(), "api.anthropic.com")
}

func neoPiToolName(name string) string {
	switch strings.ToLower(name) {
	case "web_search", "code_execution", "text_editor", "computer":
		return name
	default:
		return "_" + name
	}
}

func neoPiUserID(existing string, identity neoPiWireIdentity) string {
	if strings.HasPrefix(existing, "{") && gjson.Valid(existing) {
		session := gjson.Get(existing, "session_id")
		if session.Type == gjson.String && session.String() != "" {
			return existing
		}
	}
	// NeoPi accepts Claude Code's legacy user_<device>_account_<uuid>_session_<uuid> form.
	if len(existing) > 69 && strings.HasPrefix(existing, "user_") {
		device, rest, ok := strings.Cut(strings.TrimPrefix(existing, "user_"), "_account_")
		account, session, sessionOK := strings.Cut(rest, "_session_")
		if _, err := hex.DecodeString(device); ok && err == nil && len(device) == 64 && sessionOK {
			_, accountErr := uuid.Parse(account)
			_, sessionErr := uuid.Parse(session)
			if accountErr == nil && sessionErr == nil && len(account) == 36 && len(session) == 36 && account == strings.ToLower(account) && session == strings.ToLower(session) {
				return existing
			}
		}
	}
	seed := "omp-claude-device-id-v1:" + identity.InstallID
	if identity.AccountID != "" {
		seed = "omp-claude-device-id-v2\x00" + identity.InstallID + "\x00" + identity.AccountID
	}
	device := sha256.Sum256([]byte(seed))
	fields := []neoPiField{
		{"device_id", neoPiJSON(hex.EncodeToString(device[:]))},
		{"session_id", neoPiJSON(identity.SessionID)},
	}
	if identity.AccountID != "" {
		fields = append(fields, neoPiField{"account_uuid", neoPiJSON(identity.AccountID)})
	}
	return string(neoPiObject(fields...))
}

func neoPiBillingHeader(firstUser, version string) string {
	chars := utf16.Encode([]rune(firstUser))
	var selected []uint16
	for _, index := range []int{4, 7, 20} {
		if index < len(chars) {
			selected = append(selected, chars[index])
		} else {
			selected = append(selected, '0')
		}
	}
	hash := sha256.Sum256([]byte("59cf53e54c78" + string(utf16.Decode(selected)) + version))
	return fmt.Sprintf("x-anthropic-billing-header: cc_version=%s.%s; cc_entrypoint=cli; cch=00000;", version, hex.EncodeToString(hash[:])[:3])
}

func neoPiSignBody(body []byte) []byte {
	// The hash covers the exact serialized body with the first billing block's
	// cch value zeroed. It must run after every transformation and on each retry.
	blocks := gjson.GetBytes(body, "system").Array()
	for index, block := range blocks {
		text := block.Get("text").String()
		if !strings.HasPrefix(text, "x-anthropic-billing-header:") {
			continue
		}
		start := strings.Index(text, "cch=")
		if start < 0 {
			return body
		}
		end := strings.IndexByte(text[start+4:], ';')
		if end < 0 {
			return body
		}
		path := fmt.Sprintf("system.%d.text", index)
		zeroed := text[:start+4] + "00000" + text[start+4+end:]
		body = neoPiSet(body, path, zeroed)
		hasher := xxHash64.New(0x4d659218e32a3268)
		_, _ = hasher.Write(body)
		return neoPiSet(body, path, zeroed[:start+4]+fmt.Sprintf("%05x", hasher.Sum64()&0xfffff)+zeroed[start+9:])
	}
	return body
}

func neoPiTextBlock(text string) json.RawMessage {
	return neoPiObject(neoPiField{"type", neoPiJSON("text")}, neoPiField{"text", neoPiJSON(text)})
}

// projectNeoPiRequest takes the dialect-translated Messages body. Client-side
// provenance and requestControls are not present in the HTTP dialect; explicit
// wire controls are preserved rather than inventing that missing session state.
func projectNeoPiRequest(input []byte, model, base, version string, identity neoPiWireIdentity) ([]byte, http.Header, error) {
	if !gjson.ValidBytes(input) {
		return nil, nil, fmt.Errorf("anthropic-npi: invalid Messages JSON")
	}
	if identity.InstallID == "" {
		return nil, nil, fmt.Errorf("anthropic-npi: credential has no installation identity")
	}
	headerSession := identity.SessionID
	if identity.SessionID == "" {
		identity.SessionID = uuid.NewString()
	}
	root := gjson.ParseBytes(input)
	profile, known := neoPiProfiles[model]
	if !known {
		return nil, nil, fmt.Errorf("anthropic-npi: no captured NeoPi capability profile for %q", model)
	}
	cache := json.RawMessage(`{"type":"ephemeral"}`)
	if neoPiOfficialEndpoint(base) && profile.Compat.SupportsLongCacheRetention {
		cache = json.RawMessage(`{"type":"ephemeral","ttl":"1h"}`)
	}
	var messages []json.RawMessage
	firstUser := ""
	for _, message := range root.Get("messages").Array() {
		role := message.Get("role").String()
		content := message.Get("content")
		var blocks []json.RawMessage
		if content.Type == gjson.String {
			blocks = append(blocks, neoPiTextBlock(content.String()))
		} else {
			for _, block := range content.Array() {
				raw := []byte(block.Raw)
				if block.Get("type").String() == "tool_use" {
					raw = neoPiSet(raw, "name", neoPiToolName(block.Get("name").String()))
				}
				blocks = append(blocks, raw)
			}
		}
		if firstUser == "" && role == "user" {
			for _, block := range blocks {
				if gjson.GetBytes(block, "type").String() == "text" {
					firstUser = gjson.GetBytes(block, "text").String()
					break
				}
			}
		}
		raw := neoPiObject(neoPiField{"role", neoPiJSON(role)}, neoPiField{"content", neoPiJSON(blocks)})
		for _, key := range []string{"clear_at", "output_config"} {
			if value := message.Get(key); value.Exists() {
				raw = neoPiSetRaw(raw, key, []byte(value.Raw))
			}
		}
		messages = append(messages, raw)
	}
	if len(messages) == 0 {
		return nil, nil, fmt.Errorf("anthropic-npi: messages must not be empty")
	}
	if gjson.GetBytes(messages[len(messages)-1], "role").String() == "assistant" {
		messages = append(messages, json.RawMessage(`{"role":"user","content":"Continue."}`))
	}
	var prompts []string
	if system := root.Get("system"); system.Type == gjson.String {
		prompts = append(prompts, system.String())
	} else {
		for _, block := range system.Array() {
			prompts = append(prompts, block.Get("text").String())
		}
	}
	var system []json.RawMessage
	hasBilling := false
	for _, prompt := range prompts {
		if strings.HasPrefix(strings.TrimSpace(prompt), "x-anthropic-billing-header:") {
			hasBilling = true
		}
	}
	if !hasBilling && profile.Compat.InjectClaudeCodeInstruction {
		system = append(system, neoPiTextBlock(neoPiBillingHeader(firstUser, version)), neoPiTextBlock(neoPiClaudeInstruction))
	}
	for _, prompt := range prompts {
		if text := strings.TrimSpace(prompt); text != "" {
			system = append(system, neoPiTextBlock(text))
		}
	}
	if len(system) > 0 {
		anchor := len(system) - 1
		for i, block := range system {
			text := gjson.GetBytes(block, "text").String()
			if strings.HasPrefix(text, "<memories>") || strings.HasPrefix(text, "<project-context>") {
				if i > 0 {
					anchor = i - 1
				}
				break
			}
		}
		system[anchor] = neoPiSetRaw(system[anchor], "cache_control", cache)
	}
	tools := make([]json.RawMessage, 0, len(root.Get("tools").Array()))
	strictBudget := neoPiStrictBudget{optional: 24, unions: 16}
	strictCount := 0
	for _, tool := range root.Get("tools").Array() {
		// Server tools already have an Anthropic type and no client input schema.
		if tool.Get("type").Exists() && !tool.Get("input_schema").Exists() {
			tools = append(tools, json.RawMessage(tool.Raw))
			continue
		}
		name := tool.Get("name").String()
		schema := []byte(tool.Get("input_schema").Raw)
		if len(schema) == 0 {
			schema = []byte(`{}`)
		}
		schema = neoPiSet(schema, "type", "object")
		if !gjson.GetBytes(schema, "properties").IsObject() {
			schema = neoPiSetRaw(schema, "properties", []byte(`{}`))
		}
		var required []string
		for _, value := range gjson.GetBytes(schema, "required").Array() {
			if value.Type == gjson.String {
				required = append(required, value.String())
			}
		}
		if required == nil {
			required = []string{}
		}
		schema = neoPiSet(schema, "required", required)
		normalized := neoPiNormalizeSchema(gjson.ParseBytes(schema), true)
		strict := false
		if (name == "python" || name == "edit" || name == "find") && tool.Get("strict").Raw != "false" && strictCount < 20 && !neoPiStrictIncompatible(gjson.ParseBytes(schema)) {
			budget := strictBudget
			if converted, ok := neoPiStrictSchema(gjson.ParseBytes(normalized), &budget); ok {
				normalized = converted
				strict = true
				strictBudget = budget
				strictCount++
			}
		}
		wire := neoPiObject(neoPiField{"name", neoPiJSON(neoPiToolName(name))}, neoPiField{"description", neoPiJSON(tool.Get("description").String())}, neoPiField{"input_schema", normalized})
		if neoPiOfficialEndpoint(base) && profile.Compat.SupportsEagerToolInputStreaming {
			wire = neoPiSet(wire, "eager_input_streaming", true)
		}
		if strict {
			wire = neoPiSet(wire, "strict", true)
		}
		if tool.Get("defer_loading").Bool() {
			wire = neoPiSet(wire, "defer_loading", true)
		}
		tools = append(tools, wire)
	}
	for i := len(tools) - 1; i >= 0; i-- {
		if !gjson.GetBytes(tools[i], "defer_loading").Bool() {
			tools[i] = neoPiSetRaw(tools[i], "cache_control", cache)
			break
		}
	}
	messages = neoPiCacheMessages(messages, system, tools, cache)
	maxTokens := root.Get("max_tokens").Int()
	if maxTokens <= 0 {
		maxTokens = 32000
	}
	thinking := root.Get("thinking")
	effort := root.Get("output_config.effort").String()
	off := thinking.Get("type").String() == "disabled" || effort == "none" || effort == "off"
	enabled := !off && (thinking.Get("type").String() == "enabled" || thinking.Get("type").String() == "adaptive" || effort != "")
	var wireThinking json.RawMessage
	if profile.Thinking.Mode == "anthropic-adaptive" {
		if enabled {
			wireThinking = json.RawMessage(`{"type":"adaptive"}`)
			if profile.Thinking.SupportsDisplay {
				wireThinking = neoPiSet(wireThinking, "display", "summarized")
			}
		} else if off {
			if profile.Compat.SupportsBetweenToolsThinking {
				wireThinking = json.RawMessage(`{"type":"between_tools"}`)
				effort = ""
			} else {
				effort = "low"
			}
		}
	} else if enabled {
		budget := thinking.Get("budget_tokens").Int()
		if budget <= 0 {
			budget = 10240
		}
		if budget >= maxTokens {
			budget = maxTokens - 1
		}
		wireThinking = neoPiObject(neoPiField{"type", neoPiJSON("enabled")}, neoPiField{"budget_tokens", neoPiJSON(budget)})
	} else if off {
		wireThinking = json.RawMessage(`{"type":"disabled"}`)
	}
	if profile.Thinking.PrefixBinding && profile.Compat.SupportsThinkingBindingControls {
		if len(wireThinking) == 0 && !(off && profile.Compat.DisabledThinking == "omit") {
			wireThinking = json.RawMessage(`{"type":"adaptive"}`)
		}
		if kind := gjson.GetBytes(wireThinking, "type").String(); kind == "adaptive" || kind == "enabled" {
			binding := thinking.Get("block_binding.prefix_mismatch_behavior").String()
			if binding == "" {
				binding = "drop_block"
			}
			wireThinking = neoPiSet(wireThinking, "block_binding.prefix_mismatch_behavior", binding)
		}
	}
	var contextManagement json.RawMessage
	if configured := root.Get("context_management"); configured.Exists() {
		contextManagement = []byte(configured.Raw)
	} else if kind := gjson.GetBytes(wireThinking, "type").String(); profile.Compat.SupportsContextManagement && (kind == "adaptive" || kind == "enabled") {
		contextManagement = json.RawMessage(`{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]}`)
	}
	var outputConfig json.RawMessage
	if output := root.Get("output_config"); output.IsObject() {
		outputConfig = []byte(output.Raw)
	}
	if effort != "" {
		if outputConfig == nil {
			outputConfig = []byte(`{}`)
		}
		outputConfig = neoPiSet(outputConfig, "effort", effort)
	}
	metadata := neoPiObject(neoPiField{"user_id", neoPiJSON(neoPiUserID(root.Get("metadata.user_id").String(), identity))})
	fields := []neoPiField{{"model", neoPiJSON(model)}, {"messages", neoPiJSON(messages)}, {"system", neoPiJSON(system)}, {"tools", neoPiJSON(tools)}, {"metadata", metadata}, {"max_tokens", neoPiJSON(maxTokens)}, {"thinking", wireThinking}, {"context_management", contextManagement}, {"output_config", outputConfig}, {"stream", neoPiJSON(true)}}
	for _, name := range []string{"stop_sequences", "service_tier", "speed", "fallbacks", "fallback_credit_token"} {
		if value := root.Get(name); value.Exists() {
			fields = append(fields, neoPiField{name, []byte(value.Raw)})
		}
	}
	if profile.Compat.SupportsSamplingParams {
		for _, name := range []string{"temperature", "top_p", "top_k"} {
			if value := root.Get(name); value.Exists() {
				fields = append(fields, neoPiField{name, []byte(value.Raw)})
			}
		}
	}
	if choice := root.Get("tool_choice"); choice.Exists() {
		raw := []byte(choice.Raw)
		if choice.Get("type").String() == "tool" {
			raw = neoPiSet(raw, "name", neoPiToolName(choice.Get("name").String()))
		}
		if !profile.Compat.SupportsForcedToolChoice && (choice.Get("type").String() == "tool" || choice.Get("type").String() == "any") {
			raw = []byte(`{"type":"auto"}`)
		}
		fields = append(fields, neoPiField{"tool_choice", raw})
	}
	body := neoPiSignBody(neoPiJSON(neoPiObject(fields...)))
	return body, neoPiHeaders(body, headerSession, version, enabled, len(tools) > 0, base, profile), nil
}

func neoPiCacheMessages(messages, system, tools []json.RawMessage, cache []byte) []json.RawMessage {
	budget := 4
	for _, group := range [][]json.RawMessage{system, tools} {
		for _, block := range group {
			if gjson.GetBytes(block, "cache_control").Exists() {
				budget--
			}
		}
	}
	// Incoming cache markers are client hints, not additional breakpoints. NeoPi
	// reconstructs content blocks before it selects its own four anchors.
	for i, message := range messages {
		for j := range gjson.GetBytes(message, "content").Array() {
			messages[i] = neoPiDelete(messages[i], fmt.Sprintf("content.%d.cache_control", j))
		}
	}
	end := len(messages) - 1
	if end > 0 && gjson.GetBytes(messages[end], "content").String() == "Continue." && gjson.GetBytes(messages[end-1], "role").String() == "assistant" {
		end--
	}
	mark, turns := -1, 0
	var trailing []int
	isUser := func(index int) bool {
		m := gjson.ParseBytes(messages[index])
		if m.Get("role").String() != "user" {
			return false
		}
		for _, b := range m.Get("content").Array() {
			if b.Get("type").String() == "tool_result" {
				return false
			}
		}
		return true
	}
	for i := end; i >= 0; i-- {
		m := gjson.ParseBytes(messages[i])
		if m.Get("clear_at").String() == "next_user_message" {
			mark = i
			turns = 0
			continue
		}
		control := m.Get("role").String() == "system"
		if control {
			for _, b := range m.Get("content").Array() {
				if t := b.Get("type").String(); t != "tool_addition" && t != "tool_removal" {
					control = false
					break
				}
			}
		}
		if len(trailing) < 2 && !control {
			trailing = append(trailing, i)
		}
		if isUser(i) {
			turns++
		}
	}
	apply := func(index int) {
		if budget <= 0 || index < 0 {
			return
		}
		blocks := gjson.GetBytes(messages[index], "content").Array()
		for j := len(blocks) - 1; j >= 0; j-- {
			switch blocks[j].Get("type").String() {
			case "thinking", "redacted_thinking", "fallback", "tool_addition", "tool_removal":
				continue
			}
			path := fmt.Sprintf("content.%d.cache_control", j)
			if gjson.GetBytes(messages[index], path).Exists() {
				return
			}
			messages[index] = neoPiSetRaw(messages[index], path, cache)
			budget--
			return
		}
	}
	if len(trailing) > 0 {
		apply(trailing[0])
	}
	stableEnd := end
	if mark >= 0 {
		stableEnd = mark - 1
	}
	if turns >= 15 {
		remaining := turns % 15
		for i := stableEnd; i >= 0 && budget > 0; i-- {
			if !isUser(i) {
				continue
			}
			if remaining > 0 {
				remaining--
				continue
			}
			remaining = 14
			apply(i)
		}
	}
	if stableEnd < end {
		apply(stableEnd)
	}
	for _, index := range trailing {
		apply(index)
	}
	return messages
}

func neoPiHeaders(body []byte, session, version string, thinking, tools bool, base string, profile neoPiProfile) http.Header {
	betas := []string{"oauth-2025-04-20", "interleaved-thinking-2025-05-14", "thinking-token-count-2026-05-13", "context-management-2025-06-27", "prompt-caching-scope-2026-01-05"}
	if thinking || tools {
		betas = append([]string{"claude-code-20250219"}, betas...)
		betas = append(betas, "mid-conversation-system-2026-04-07")
		if thinking {
			betas = append(betas, "effort-2025-11-24")
		}
	} else {
		betas = append(betas, "structured-outputs-2025-12-15")
	}
	if thinking || tools {
		betas = append(betas, "fallback-credit-2026-06-01")
	}
	add := func(beta string) {
		for _, existing := range betas {
			if existing == beta {
				return
			}
		}
		betas = append(betas, beta)
	}
	if gjson.GetBytes(body, "speed").String() == "fast" {
		add("fast-mode-2026-02-01")
	}
	if gjson.GetBytes(body, "output_config.task_budget").Exists() {
		add("task-budgets-2026-03-13")
	}
	if gjson.GetBytes(body, "output_config.effort").Exists() {
		add("effort-2025-11-24")
	}
	if profile.Compat.SupportsTurnScopedSystem {
		add("mid-conversation-system-clear-at-2026-08-21")
	}
	if profile.Compat.SupportsMidConversationToolChanges {
		add("mid-conversation-tool-changes-2026-07-01")
	}
	if profile.Compat.SupportsPerMessageEffort {
		add("mid-conversation-output-config-2026-07-01")
	}
	if tools && neoPiOfficialEndpoint(base) && !profile.Compat.SupportsEagerToolInputStreaming {
		add("fine-grained-tool-streaming-2025-05-14")
	}
	if gjson.GetBytes(body, "thinking.block_binding").Exists() {
		add("thinking-binding-controls-2026-08-01")
	}
	if gjson.GetBytes(body, "fallbacks.#").Int() > 0 {
		add("server-side-fallback-2026-06-01")
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	osName := map[string]string{"linux": "Linux", "darwin": "MacOS", "windows": "Windows"}[runtime.GOOS]
	if osName == "" {
		osName = "Other:" + runtime.GOOS
	}
	h := make(http.Header)
	for key, value := range map[string]string{
		"Accept": "application/json", "Accept-Encoding": "gzip, deflate, br, zstd", "Connection": "keep-alive", "Content-Type": "application/json",
		"anthropic-beta": strings.Join(betas, ","), "anthropic-dangerous-direct-browser-access": "true", "anthropic-version": "2023-06-01",
		"User-Agent": "claude-cli/" + version + " (external, cli)", "x-app": "cli", "X-Claude-Code-Session-Id": session,
		"X-Stainless-Arch": arch, "X-Stainless-Lang": "js", "X-Stainless-OS": osName, "X-Stainless-Package-Version": anthropicnpi.SDKVersion,
		"X-Stainless-Retry-Count": "0", "X-Stainless-Runtime": "node", "X-Stainless-Runtime-Version": "v26.3.0", "X-Stainless-Timeout": "600",
	} {
		h.Set(key, value)
	}
	if session == "" {
		h.Del("X-Claude-Code-Session-Id")
	}
	return h
}

func neoPiStripToolPrefix(data []byte) []byte {
	root := gjson.ParseBytes(data)
	if block := root.Get("content_block"); block.Get("type").String() == "tool_use" {
		data = neoPiSet(data, "content_block.name", strings.TrimPrefix(block.Get("name").String(), "_"))
	}
	for index, block := range root.Get("content").Array() {
		if block.Get("type").String() == "tool_use" {
			data = neoPiSet(data, fmt.Sprintf("content.%d.name", index), strings.TrimPrefix(block.Get("name").String(), "_"))
		}
	}
	return data
}
