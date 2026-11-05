package helps

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/githubcopilot"
	"github.com/tidwall/gjson"
)

// GitHubCopilotDynamicHeaders is buildCopilotDynamicHeaders: the CLI
// identity with the request's initiator, its interaction type, the
// integration id (an explicit pin, else the id learned for the credential,
// else the CLI for GitHub Enterprise and the chat surface otherwise) and the
// vision flag when the conversation carries images.
func GitHubCopilotDynamicHeaders(initiator string, hasImages bool, explicit, cached string, enterprise bool) http.Header {
	headers := githubcopilot.IdentityHeaders()
	headers.Set("X-Initiator", initiator)
	headers.Set("X-Interaction-Type", "conversation-"+initiator)
	id := githubcopilot.NormalizeIntegrationID(explicit)
	if id == "" {
		id = githubcopilot.NormalizeIntegrationID(cached)
	}
	if id == "" {
		id = githubcopilot.ChatIntegrationID
		if enterprise {
			id = githubcopilot.CLIIntegrationID
		}
	}
	headers.Set("Copilot-Integration-Id", id)
	if hasImages {
		headers.Set("Copilot-Vision-Request", "true")
	}
	return headers
}

// GitHubCopilotInitiatorOverride is getCopilotInitiatorOverride: the last
// valid X-Initiator a caller configured, empty when none.
func GitHubCopilotInitiatorOverride(headers http.Header) string {
	override := ""
	for name, values := range headers {
		if !strings.EqualFold(name, "X-Initiator") {
			continue
		}
		for _, value := range values {
			if normalized := strings.ToLower(strings.TrimSpace(value)); normalized == "user" || normalized == "agent" {
				override = normalized
			}
		}
	}
	return override
}

// GitHubCopilotClaudeTurn is inferCopilotInitiator and hasCopilotVisionInput
// over a Claude Messages body: a turn ending in anything but a user message,
// or in a user message whose last block is a tool result, is the agent's;
// images in a user message or tool result ask for vision.
func GitHubCopilotClaudeTurn(body []byte) (initiator string, hasImages bool) {
	messages := gjson.GetBytes(body, "messages").Array()
	initiator = "user"
	if len(messages) > 0 {
		last := messages[len(messages)-1]
		if last.Get("role").String() != "user" {
			initiator = "agent"
		} else if blocks := last.Get("content").Array(); len(blocks) > 0 && blocks[len(blocks)-1].Get("type").String() == "tool_result" {
			initiator = "agent"
		}
	}
	for _, message := range messages {
		if message.Get("role").String() != "user" {
			continue
		}
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "image":
				hasImages = true
			case "tool_result":
				for _, inner := range block.Get("content").Array() {
					hasImages = hasImages || inner.Get("type").String() == "image"
				}
			}
		}
	}
	return initiator, hasImages
}

// GitHubCopilotChatTurn is inferCopilotInitiator and hasCopilotVisionInput
// over a Chat Completions body: a turn ending in anything but a user message
// is the agent's; images in a user message or tool result ask for vision.
func GitHubCopilotChatTurn(body []byte) (initiator string, hasImages bool) {
	messages := gjson.GetBytes(body, "messages").Array()
	initiator = "user"
	if len(messages) > 0 && messages[len(messages)-1].Get("role").String() != "user" {
		initiator = "agent"
	}
	for _, message := range messages {
		switch message.Get("role").String() {
		case "user", "tool":
			for _, part := range message.Get("content").Array() {
				hasImages = hasImages || part.Get("type").String() == "image_url"
			}
		}
	}
	return initiator, hasImages
}

// GitHubCopilotResponsesInitiator is inferCopilotInitiator over a Responses
// input: a turn ending in anything but a user message (a tool output, an
// assistant message, a call or reasoning) is the agent's.
func GitHubCopilotResponsesInitiator(input []json.RawMessage) string {
	if len(input) == 0 {
		return "user"
	}
	if gjson.GetBytes(input[len(input)-1], "role").String() == "user" {
		return "user"
	}
	return "agent"
}
