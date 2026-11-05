package helps

import (
	"bytes"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cursorwire"
	"github.com/tidwall/gjson"
)

// Cursor payload rules address the run request's business fields as a JSON
// view: the requested model and its parameters, the model details, the
// custom system prompt and the conversation id. Conversation history, blob
// ids and Connect framing stay out of reach.

type cursorPayloadParameter struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

type cursorPayloadDetails struct {
	ModelID        string `json:"model_id"`
	DisplayModelID string `json:"display_model_id"`
	DisplayName    string `json:"display_name"`
	MaxMode        bool   `json:"max_mode"`
}

type cursorPayloadView struct {
	Model              string                   `json:"model"`
	MaxMode            bool                     `json:"max_mode"`
	Parameters         []cursorPayloadParameter `json:"parameters"`
	ModelDetails       cursorPayloadDetails     `json:"model_details"`
	CustomSystemPrompt *string                  `json:"custom_system_prompt,omitempty"`
	ConversationID     string                   `json:"conversation_id"`
}

// FinalizeCursorPayload applies user payload rules to the run request's
// business view, writing back only the fields the rules changed. It is the
// last semantic step before the request is serialized.
func FinalizeCursorPayload(run *cursorwire.AgentRunRequest, finalize PayloadFinalizer) {
	if run == nil || finalize == nil {
		return
	}
	if run.RequestedModel == nil {
		run.RequestedModel = &cursorwire.RequestedModel{}
	}
	if run.ModelDetails == nil {
		run.ModelDetails = &cursorwire.ModelDetails{}
	}
	view := cursorPayloadView{Model: run.RequestedModel.ModelId, MaxMode: run.RequestedModel.MaxMode, Parameters: []cursorPayloadParameter{},
		ModelDetails: cursorPayloadDetails{ModelID: run.ModelDetails.ModelId, DisplayModelID: run.ModelDetails.DisplayModelId,
			DisplayName: run.ModelDetails.DisplayName, MaxMode: run.ModelDetails.MaxMode != nil && *run.ModelDetails.MaxMode},
		CustomSystemPrompt: run.CustomSystemPrompt}
	for _, parameter := range run.RequestedModel.Parameters {
		view.Parameters = append(view.Parameters, cursorPayloadParameter{ID: parameter.Id, Value: parameter.Value})
	}
	if run.ConversationId != nil {
		view.ConversationID = *run.ConversationId
	}
	body, err := json.Marshal(view)
	if err != nil {
		return
	}
	configured := finalize(body)
	if bytes.Equal(configured, body) {
		return
	}
	result := gjson.ParseBytes(configured)
	if value := result.Get("model"); value.Exists() {
		run.RequestedModel.ModelId = value.String()
	}
	if value := result.Get("max_mode"); value.Exists() {
		run.RequestedModel.MaxMode = value.Bool()
	}
	if value := result.Get("parameters"); value.Exists() {
		run.RequestedModel.Parameters = nil
		for _, item := range value.Array() {
			run.RequestedModel.Parameters = append(run.RequestedModel.Parameters,
				&cursorwire.RequestedModel_ModelParameterbytes{Id: item.Get("id").String(), Value: item.Get("value").String()})
		}
	}
	if value := result.Get("model_details.model_id"); value.Exists() {
		run.ModelDetails.ModelId = value.String()
	}
	if value := result.Get("model_details.display_model_id"); value.Exists() {
		run.ModelDetails.DisplayModelId = value.String()
	}
	if value := result.Get("model_details.display_name"); value.Exists() {
		run.ModelDetails.DisplayName = value.String()
	}
	if value := result.Get("model_details.max_mode"); value.Exists() {
		if value.Bool() {
			enabled := true
			run.ModelDetails.MaxMode = &enabled
		} else {
			run.ModelDetails.MaxMode = nil
		}
	}
	if value := result.Get("custom_system_prompt"); value.Exists() {
		prompt := value.String()
		run.CustomSystemPrompt = &prompt
	}
	if value := result.Get("conversation_id"); value.Exists() {
		id := value.String()
		run.ConversationId = &id
	}
}
