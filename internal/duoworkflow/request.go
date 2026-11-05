package duoworkflow

import (
	_ "embed"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/codexwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/jsjson"
	"github.com/tidwall/gjson"
)

const (
	// WorkflowDefinition is the inline ambient flow every NeoPi request runs.
	WorkflowDefinition = "ambient"
	clientType         = "node-websocket"
	// languageServerVersion is the GitLab language server the socket names.
	languageServerVersion = "8.104.0"
	inlineAgentName       = "omp_agent"
	inlinePromptID        = "omp_inline_prompt"
	chatMLStart           = "<|im_start|>"
	chatMLEnd             = "<|im_end|>"
	// defaultInputSchema is a tool's schema when its wire schema is no object.
	defaultInputSchema = `{"type":"object","properties":{},"required":[]}`
	// SettingsBody turns on the three namespace flags the inline flow needs.
	SettingsBody = `{"experiment_features_enabled":true,"ai_settings_attributes":{"duo_agent_platform_enabled":true,"duo_workflow_mcp_enabled":true}}`
	// StopBody stops a workflow.
	StopBody = `{"status_event":"stop"}`
)

var (
	clientCapabilities = []string{"incremental_streaming", "read_file_chunked", "shell_command", "command_timeout", "tool_call_approval"}
	inlineUILogEvents  = []string{"on_agent_reasoning", "on_agent_final_answer", "on_tool_execution_success", "on_tool_execution_failed"}
)

// chatMLHistoryNote is gitlab-duo-workflow-chatml-note.md, which NeoPi
// appends to the system prompt when the goal is a ChatML transcript.
//
//go:embed chatml_note.md
var chatMLHistoryNoteFile string

var chatMLHistoryNote = strings.TrimSpace(chatMLHistoryNoteFile)

// StartMetadata is GitLabDuoWorkflowStartMetadataOptions.
type StartMetadata struct {
	ProjectID       string
	NamespaceID     string
	RootNamespaceID string
}

// StartRequest is GitLabDuoWorkflowStartRequest, kept as its parts so the
// approval form can be derived from it.
type StartRequest struct {
	WorkflowID       string
	Goal             string
	WorkflowMetadata string
	MCPTools         []MCPTool
	SystemPrompt     string
}

// MCPTool is GitLabMcpToolDefinition: a client tool under its bare name.
type MCPTool struct {
	Name        string
	Description string
	InputSchema string
}

// BuildStartRequest is buildGitLabDuoWorkflowStartRequest: the conversation
// as the goal, NeoPi's system prompt in the inline flow, the tools as MCP
// tools, and the metadata naming the selected model (a pinned model wins).
func BuildStartRequest(workflowID, modelID string, ctx *cursorwire.Context, tools []cursorwire.Tool, pinnedModelRef string, metadata StartMetadata) (*StartRequest, error) {
	system, err := SanitizeFlowPrompt(systemPrompt(ctx))
	if err != nil {
		return nil, err
	}
	selected := modelID
	if pinnedModelRef != "" {
		selected = pinnedModelRef
	}
	fields := []jsjson.Field{
		{Name: "environment", Value: jsjson.Marshal("ide")},
		{Name: "client_type", Value: jsjson.Marshal(clientType)},
	}
	if metadata.ProjectID != "" {
		fields = append(fields, jsjson.Field{Name: "projectId", Value: jsjson.Marshal(metadata.ProjectID)})
	}
	if metadata.NamespaceID != "" {
		fields = append(fields, jsjson.Field{Name: "namespaceId", Value: jsjson.Marshal(RestNamespaceID(metadata.NamespaceID))})
	}
	if metadata.RootNamespaceID != "" {
		fields = append(fields, jsjson.Field{Name: "rootNamespaceId", Value: jsjson.Marshal(RestNamespaceID(metadata.RootNamespaceID))})
	}
	fields = append(fields, jsjson.Field{Name: "selectedModelIdentifier", Value: jsjson.Marshal(selected)})
	request := &StartRequest{WorkflowID: workflowID, Goal: goal(ctx), WorkflowMetadata: string(jsjson.Object(fields...)), SystemPrompt: system}
	for _, tool := range tools {
		request.MCPTools = append(request.MCPTools, mcpTool(tool))
	}
	return request, nil
}

// JSON is the `{startRequest}` message; approval gives
// buildGitLabDuoWorkflowApprovalStartRequest's form (empty goal and context,
// an approval appended).
func (r *StartRequest) JSON(approval bool) json.RawMessage {
	goalText := r.Goal
	if approval {
		goalText = ""
	}
	tools := make([]json.RawMessage, len(r.MCPTools))
	names := make([]string, len(r.MCPTools))
	for i, tool := range r.MCPTools {
		names[i] = tool.Name
		tools[i] = jsjson.Object(
			jsjson.Field{Name: "name", Value: jsjson.Marshal(tool.Name)},
			jsjson.Field{Name: "originalToolName", Value: jsjson.Marshal(tool.Name)},
			jsjson.Field{Name: "serverName", Value: jsjson.Marshal("omp")},
			jsjson.Field{Name: "description", Value: jsjson.Marshal(tool.Description)},
			jsjson.Field{Name: "inputSchema", Value: jsjson.Marshal(tool.InputSchema)},
			jsjson.Field{Name: "isApproved", Value: json.RawMessage("true")},
		)
	}
	fields := []jsjson.Field{
		{Name: "workflowID", Value: jsjson.Marshal(r.WorkflowID)},
		{Name: "clientVersion", Value: jsjson.Marshal("1.0")},
		{Name: "workflowDefinition", Value: jsjson.Marshal(WorkflowDefinition)},
		{Name: "goal", Value: jsjson.Marshal(goalText)},
		{Name: "workflowMetadata", Value: jsjson.Marshal(r.WorkflowMetadata)},
		{Name: "additional_context", Value: json.RawMessage("[]")},
		{Name: "clientCapabilities", Value: jsjson.Marshal(clientCapabilities)},
		{Name: "mcpTools", Value: jsonArray(tools)},
		{Name: "preapproved_tools", Value: jsjson.Marshal(names)},
		{Name: "flowConfigSchemaVersion", Value: jsjson.Marshal("v1")},
		{Name: "flowConfig", Value: inlineFlowConfig(r.SystemPrompt)},
	}
	if approval {
		fields = append(fields, jsjson.Field{Name: "approval", Value: json.RawMessage(`{"approval":{}}`)})
	}
	return jsjson.Object(jsjson.Field{Name: "startRequest", Value: jsjson.Object(fields...)})
}

// inlineFlowConfig is buildGitLabDuoWorkflowInlineFlowConfig: one agent
// component whose system slot is NeoPi's prompt and whose user slot is the goal.
func inlineFlowConfig(system string) json.RawMessage {
	field := func(name string, value any) jsjson.Field {
		return jsjson.Field{Name: name, Value: jsjson.Marshal(value)}
	}
	return jsjson.Object(
		field("version", "v1"),
		field("environment", "ambient"),
		jsjson.Field{Name: "flow", Value: jsjson.Object(field("entry_point", inlineAgentName))},
		jsjson.Field{Name: "components", Value: jsonArray([]json.RawMessage{jsjson.Object(
			field("name", inlineAgentName),
			field("type", "AgentComponent"),
			field("prompt_id", inlinePromptID),
			jsjson.Field{Name: "toolset", Value: json.RawMessage("[]")},
			jsjson.Field{Name: "inputs", Value: jsonArray([]json.RawMessage{jsjson.Object(field("from", "context:goal"), field("as", "goal"))})},
			field("ui_log_events", inlineUILogEvents),
		)})},
		jsjson.Field{Name: "routers", Value: jsonArray([]json.RawMessage{jsjson.Object(field("from", inlineAgentName), field("to", "end"))})},
		jsjson.Field{Name: "prompts", Value: jsonArray([]json.RawMessage{jsjson.Object(
			field("name", inlinePromptID),
			field("prompt_id", inlinePromptID),
			field("unit_primitives", []string{"duo_agent_platform"}),
			jsjson.Field{Name: "prompt_template", Value: jsjson.Object(field("system", system), field("user", "{{goal}}"), field("placeholder", "history"))},
		)})},
	)
}

func jsonArray(items []json.RawMessage) json.RawMessage {
	out := []byte{'['}
	for i, item := range items {
		if i > 0 {
			out = append(out, ',')
		}
		out = append(out, item...)
	}
	return append(out, ']')
}

// mcpTool is buildGitLabMcpToolDefinition: the tool's wire schema, or an
// empty object schema when that is no object.
func mcpTool(tool cursorwire.Tool) MCPTool {
	schema := defaultInputSchema
	if tool.Parameters != nil {
		if wire := codexwire.ToolWireSchema(gjson.Parse(cursorwire.StringifyJSON(tool.Parameters))); gjson.ParseBytes(wire).IsObject() {
			schema = string(wire)
		}
	}
	return MCPTool{Name: tool.Name, Description: tool.Description, InputSchema: schema}
}

// systemPrompt is buildGitLabDuoWorkflowSystemPrompt: the system prompts,
// with the history note when the goal is a ChatML transcript.
func systemPrompt(ctx *cursorwire.Context) string {
	base := strings.Join(cursorwire.NormalizeSystemPrompts(ctx.SystemPrompt), "\n\n")
	if len(conversationHistory(ctx.Messages)) <= 1 {
		return base
	}
	if base == "" {
		return chatMLHistoryNote
	}
	return base + "\n\n" + chatMLHistoryNote
}

type replayMessage struct {
	role      string
	content   string
	toolCalls []cursorwire.Block
	// toolHeader marks a tool result naming its call or tool.
	toolHeader bool
	isError    bool
}

// goal is buildGitLabDuoWorkflowGoal: a lone turn as the latest user prompt,
// a longer conversation as a flat ChatML transcript.
func goal(ctx *cursorwire.Context) string {
	history := conversationHistory(ctx.Messages)
	if len(history) <= 1 {
		for i := len(ctx.Messages) - 1; i >= 0; i-- {
			if role := ctx.Messages[i].Role; role == "user" || role == "developer" {
				return userContentText(ctx.Messages[i])
			}
		}
		return ""
	}
	turns := make([]string, len(history))
	for i, message := range history {
		turns[i] = chatMLStart + message.role + "\n" + chatMLBody(message) + chatMLEnd
	}
	return strings.Join(turns, "\n")
}

func chatMLBody(message replayMessage) string {
	if message.role == "tool" {
		if !message.toolHeader {
			return message.content + "\n"
		}
		status := ""
		if message.isError {
			status = " status=error"
		}
		return "<ran:result" + status + ">\n" + message.content + "\n"
	}
	var parts []string
	if message.content != "" {
		parts = append(parts, message.content)
	}
	for _, call := range message.toolCalls {
		parts = append(parts, "<ran "+call.Name+">"+replayArguments(call.Arguments)+"</ran>")
	}
	return strings.Join(parts, "\n") + "\n"
}

// replayArguments is the call's arguments without its `i` (intent) key.
func replayArguments(args *cursorwire.JSONObject) string {
	if args == nil {
		return "null"
	}
	if _, ok := args.Get("i"); !ok {
		return cursorwire.StringifyJSON(args)
	}
	rest := &cursorwire.JSONObject{}
	for _, key := range args.Keys() {
		if key != "i" {
			value, _ := args.Get(key)
			rest.Set(key, value)
		}
	}
	return cursorwire.StringifyJSON(rest)
}

func conversationHistory(messages []cursorwire.Message) []replayMessage {
	var history []replayMessage
	for _, message := range messages {
		switch message.Role {
		case "toolResult":
			history = append(history, replayMessage{role: "tool", content: userContentText(message), toolHeader: message.ToolName != "" || message.ToolCallID != "", isError: message.IsError})
		case "assistant":
			texts := make([]string, len(message.Blocks))
			var calls []cursorwire.Block
			for i, block := range message.Blocks {
				switch block.Type {
				case "text":
					texts[i] = block.Text
				case "toolCall":
					calls = append(calls, block)
				}
			}
			content := strings.Join(texts, "\n")
			if content == "" && len(calls) == 0 {
				continue
			}
			history = append(history, replayMessage{role: "assistant", content: content, toolCalls: calls})
		default:
			if content := userContentText(message); content != "" {
				history = append(history, replayMessage{role: "user", content: content})
			}
		}
	}
	return history
}

// userContentText is a user, developer or tool result message's text, an
// image as `[mime image]`.
func userContentText(message cursorwire.Message) string {
	if message.Plain {
		return message.Text
	}
	parts := make([]string, len(message.Parts))
	for i, part := range message.Parts {
		if part.Image {
			parts[i] = "[" + part.MimeType + " image]"
		} else {
			parts[i] = part.Text
		}
	}
	return strings.Join(parts, "\n")
}

// ToolResultAnswer is buildGitLabPlainTextFromToolResult: the result's text
// as the response, or as the error when the tool failed.
func ToolResultAnswer(message cursorwire.Message) json.RawMessage {
	key := "response"
	if message.IsError {
		key = "error"
	}
	return jsjson.Object(jsjson.Field{Name: key, Value: jsjson.Marshal(userContentText(message))})
}

// CreateBody is buildGitLabDuoWorkflowCreateBody: an IDE workflow with tool
// privileges, scoped to the project when there is one, else the namespace.
func CreateBody(namespaceID, projectID, goal string) json.RawMessage {
	field := func(name string, value any) jsjson.Field {
		return jsjson.Field{Name: name, Value: jsjson.Marshal(value)}
	}
	fields := []jsjson.Field{
		field("workflow_definition", WorkflowDefinition),
		field("environment", "ide"),
		field("allow_agent_to_request_user", false),
		field("agent_privileges", []int{6}),
		field("pre_approved_agent_privileges", []int{6}),
		field("requires_duo_cli_enabled", false),
	}
	if namespaceID != "" && projectID == "" {
		fields = append(fields, field("namespace_id", namespaceID))
	}
	if projectID != "" {
		fields = append(fields, field("project_id", projectID))
	}
	return jsjson.Object(append(fields, field("goal", goal))...)
}

// DirectAccessBody is buildGitLabDuoWorkflowDirectAccessBody.
func DirectAccessBody(rootNamespaceID, projectID string) json.RawMessage {
	fields := []jsjson.Field{
		{Name: "workflow_definition", Value: jsjson.Marshal(WorkflowDefinition)},
		{Name: "root_namespace_id", Value: jsjson.Marshal(graphQLRootNamespaceID(rootNamespaceID))},
	}
	if projectID != "" {
		fields = append(fields, jsjson.Field{Name: "project_id", Value: jsjson.Marshal(projectID)})
	}
	return jsjson.Object(fields...)
}

// SocketOptions are what the workflow WebSocket is scoped to.
type SocketOptions struct {
	ProjectID               string
	NamespaceID             string
	RootNamespaceID         string
	SelectedModelIdentifier string
	WorkflowDefinition      string
	ServiceEndpoint         bool
}

// WebSocketURL is buildGitLabDuoWorkflowWebSocketURL: GitLab's workflow
// socket under the instance's path, or a service endpoint's root.
func WebSocketURL(baseURL string, options SocketOptions) string {
	base := trimTrailingSlashes(baseURL)
	var target string
	if options.ServiceEndpoint {
		parsed, err := url.Parse(base)
		if err != nil {
			return ""
		}
		target = parsed.Scheme + "://" + parsed.Host + "/"
	} else {
		target = base + "/api/v4/ai/duo_workflows/ws"
	}
	if strings.HasPrefix(target, "http:") {
		target = "ws:" + strings.TrimPrefix(target, "http:")
	} else if index := strings.Index(target, ":"); index >= 0 {
		target = "wss:" + target[index+1:]
	}
	var query []string
	add := func(name, value string) { query = append(query, formEscape(name)+"="+formEscape(value)) }
	if options.ProjectID != "" {
		add("project_id", options.ProjectID)
	}
	if options.NamespaceID != "" && !options.ServiceEndpoint {
		add("namespace_id", RestNamespaceID(options.NamespaceID))
	}
	if options.RootNamespaceID != "" {
		add("root_namespace_id", RestNamespaceID(options.RootNamespaceID))
	}
	if options.SelectedModelIdentifier != "" {
		add("user_selected_model_identifier", options.SelectedModelIdentifier)
	}
	if options.WorkflowDefinition != "" {
		add("workflow_definition", options.WorkflowDefinition)
	}
	if len(query) == 0 {
		return target
	}
	return target + "?" + strings.Join(query, "&")
}

// WebSocketHeaders is buildGitLabDuoWorkflowWebSocketHeaders: the minted
// headers, then the workflow token and the language server's identity.
func WebSocketHeaders(token, baseURL string, options SocketOptions, extra map[string]string) map[string]string {
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	headers := map[string]string{}
	for name, value := range extra {
		headers[strings.ToLower(name)] = value
	}
	headers["authorization"] = "Bearer " + token
	headers["x-gitlab-client-type"] = clientType
	headers["x-gitlab-language-server-version"] = languageServerVersion
	headers["user-agent"] = "unknown/unknown unknown/unknown gitlab-language-server/" + languageServerVersion
	if parsed, err := url.Parse(trimTrailingSlashes(baseURL)); err == nil {
		headers["origin"] = parsed.Scheme + "://" + parsed.Host
	}
	if options.ProjectID != "" {
		headers["x-gitlab-project-id"] = options.ProjectID
	}
	if options.NamespaceID != "" {
		headers["x-gitlab-namespace-id"] = RestNamespaceID(options.NamespaceID)
	}
	if options.RootNamespaceID != "" {
		headers["x-gitlab-root-namespace-id"] = RestNamespaceID(options.RootNamespaceID)
	}
	return headers
}

// RestNamespaceID is toGitLabRestNamespaceId: a GraphQL group id as its
// number, anything else as given.
func RestNamespaceID(namespaceID string) string {
	if match := restGroupID.FindStringSubmatch(namespaceID); match != nil {
		return match[1]
	}
	return namespaceID
}

// trimTrailingSlashes is the workflow file's normalizeGitLabBaseUrl.
func trimTrailingSlashes(baseURL string) string {
	if trimmed := strings.TrimRight(baseURL, "/"); trimmed != "" {
		return trimmed
	}
	return defaultBaseURL
}

// jsSpace is JavaScript's \s.
const jsSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

// tagSpace is GITLAB_DUO_WORKFLOW_TAG_WS: Python's \s on str.
const tagSpace = `[` + jsSpace + `\x1c-\x1f\x{85}]`

var (
	escapedTagCharacter = regexp.MustCompile(`(?i)\\udb40\\ud[c-f][0-9a-f]{2}`)
	htmlComment         = regexp.MustCompile(`<!--[\x00-\x{10ffff}]*?-->`)
	commentOpener       = regexp.MustCompile(`(?i)\\+u003c!--|<!--`)
	commentMarker       = regexp.MustCompile(`(?i)\[(?://|comment)\]:`)
	markdownComment     = regexp.MustCompile(`(?i)(^|\\n|\n)[` + jsSpace + `]*\[(?://|comment)\]:[` + jsSpace + `]*(?:<[^>]*>|#|[^` + jsSpace + `]*)[` + jsSpace + `]*\((?:[^)\\]|\\[^\n\r\x{2028}\x{2029}])*\)`)
	markerName          = regexp.MustCompile(`(?i)\[(//|comment)\]:`)
	dangerousTags       = buildTagPatterns("goal", "system", "truncated_tool_output", "truncation_notice")
)

// tagPattern finds one reserved tag, plain or with \u003c-escaped brackets.
type tagPattern struct {
	tag            string
	plain, escaped *regexp.Regexp
}

// buildTagPatterns is GITLAB_DUO_WORKFLOW_TAG_PATTERNS: whitespace inside the
// brackets, an optional slash, and the folds Python's IGNORECASE adds.
func buildTagPatterns(tags ...string) []tagPattern {
	patterns := make([]tagPattern, len(tags))
	for i, tag := range tags {
		name := strings.ReplaceAll(strings.ReplaceAll(tag, "s", "[sſ]"), "i", "[iıİ]")
		patterns[i] = tagPattern{
			tag:     tag,
			plain:   regexp.MustCompile(`(?i)<` + tagSpace + `*(/?)` + tagSpace + `*` + name + tagSpace + `*>`),
			escaped: regexp.MustCompile(`(?i)\\{1,2}u003c` + tagSpace + `*(/?)` + tagSpace + `*` + name + tagSpace + `*\\{1,2}u003e`),
		}
	}
	return patterns
}

// SanitizeFlowPrompt is sanitizeGitLabDuoWorkflowFlowPrompt: the Duo Workflow
// Service's own prompt rewrites (Unicode tag characters, HTML and markdown
// comments, the tags its prompts reserve), applied to a fixed point so its
// validator finds nothing to change.
func SanitizeFlowPrompt(prompt string) (string, error) {
	text := strings.ToValidUTF8(prompt, "\uFFFD")
	passes := utf16Length(text) + 2
	for pass := 0; pass < passes; pass++ {
		next := strings.Map(func(r rune) rune {
			if r >= 0xE0000 && r <= 0xE007F || r >= 0xE0100 && r <= 0xE01EF {
				return -1
			}
			return r
		}, text)
		next = escapedTagCharacter.ReplaceAllString(next, "")
		next = htmlComment.ReplaceAllString(next, "")
		next = commentOpener.ReplaceAllString(next, "&lt;!--")
		if commentMarker.MatchString(next) {
			next = markdownComment.ReplaceAllString(next, "${1}")
			next = markerName.ReplaceAllString(next, "[${1}]\u200b:")
		}
		for _, pattern := range dangerousTags {
			next = pattern.plain.ReplaceAllString(next, "&lt;${1}"+pattern.tag+"&gt;")
			next = pattern.escaped.ReplaceAllString(next, "&lt;${1}"+pattern.tag+"&gt;")
		}
		if next == text {
			return text, nil
		}
		text = next
	}
	return "", errors.New("GitLab Duo flow prompt sanitization did not converge")
}

func utf16Length(text string) int {
	n := 0
	for _, r := range text {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}
