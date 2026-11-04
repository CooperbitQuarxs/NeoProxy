// Package devinwire is NeoPi's Devin (Codeium Cascade) wire: the protobuf
// messages its provider, discovery and usage send and read, encoded the way
// NeoPi's protobuf runtime encodes them, plus Connect framing and the CLI
// identity metadata.
package devinwire

import (
	"fmt"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

// NeoPi's protobuf runtime writes fields in schema declaration order, skips a
// non-optional field holding its zero value, and always writes a set message.
// Every encoder here lists its fields in that declaration order.

func appendString(b []byte, num protowire.Number, v string) []byte {
	if v == "" {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, v)
}

func appendBool(b []byte, num protowire.Number, v bool) []byte {
	if !v {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, 1)
}

func appendUint(b []byte, num protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func appendDouble(b []byte, num protowire.Number, v float64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, num, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, math.Float64bits(v))
}

func appendMessage(b []byte, num protowire.Number, encoded []byte) []byte {
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, encoded)
}

// Enum values NeoPi's Devin provider sends.
const (
	SourceUser   = 1
	SourceSystem = 2
	SourceTool   = 4

	requestTypeCascade = 5
	plannerModeDefault = 1
	cacheEphemeral     = 1

	StopReasonUnspecified = 0
	StopReasonMaxTokens   = 3
)

// Metadata is exa.codeium_common_pb.Metadata, the fields NeoPi sets.
type Metadata struct {
	IDEName, IDEVersion, IDEType, ExtensionName, ExtensionVersion string
	APIKey, Locale, OS, UserJWT                                   string
	// SupportedModelDisplays is packed field 30, sent only by discovery.
	SupportedModelDisplays []int32
}

func (m Metadata) encode() []byte {
	var b []byte
	b = appendString(b, 1, m.IDEName)
	b = appendString(b, 7, m.IDEVersion)
	b = appendString(b, 28, m.IDEType)
	b = appendString(b, 12, m.ExtensionName)
	b = appendString(b, 2, m.ExtensionVersion)
	b = appendString(b, 3, m.APIKey)
	b = appendString(b, 4, m.Locale)
	b = appendString(b, 5, m.OS)
	b = appendString(b, 21, m.UserJWT)
	if len(m.SupportedModelDisplays) > 0 {
		var packed []byte
		for _, display := range m.SupportedModelDisplays {
			packed = protowire.AppendVarint(packed, uint64(int64(display)))
		}
		b = protowire.AppendTag(b, 30, protowire.BytesType)
		b = protowire.AppendBytes(b, packed)
	}
	return b
}

// Image is exa.codeium_common_pb.ImageData.
type Image struct{ Base64Data, MimeType string }

func (i Image) encode() []byte {
	b := appendString(nil, 1, i.Base64Data)
	return appendString(b, 2, i.MimeType)
}

// ToolCall is exa.codeium_common_pb.ChatToolCall.
type ToolCall struct{ ID, Name, ArgumentsJSON string }

func (c ToolCall) encode() []byte {
	b := appendString(nil, 1, c.ID)
	b = appendString(b, 2, c.Name)
	return appendString(b, 3, c.ArgumentsJSON)
}

// Prompt is exa.chat_pb.ChatMessagePrompt.
type Prompt struct {
	MessageID         string
	Source            int
	Prompt            string
	ToolCalls         []ToolCall
	ToolCallID        string
	ToolResultIsError bool
	Images            []Image
	Thinking          string
	Signature         string
}

// Encode is the prompt's protobuf bytes.
func (p Prompt) Encode() []byte {
	var b []byte
	b = appendString(b, 1, p.MessageID)
	b = appendUint(b, 2, uint64(p.Source))
	b = appendString(b, 3, p.Prompt)
	for _, call := range p.ToolCalls {
		b = appendMessage(b, 6, call.encode())
	}
	b = appendString(b, 7, p.ToolCallID)
	b = appendBool(b, 9, p.ToolResultIsError)
	for _, image := range p.Images {
		b = appendMessage(b, 10, image.encode())
	}
	b = appendString(b, 11, p.Thinking)
	return appendString(b, 12, p.Signature)
}

// Tool is exa.chat_pb.ChatToolDefinition.
type Tool struct {
	Name, Description, JSONSchema string
	Strict                        bool
}

func (t Tool) encode() []byte {
	b := appendString(nil, 1, t.Name)
	b = appendString(b, 2, t.Description)
	b = appendString(b, 3, t.JSONSchema)
	return appendBool(b, 12, t.Strict)
}

// Configuration is exa.codeium_common_pb.CompletionConfiguration.
type Configuration struct {
	MaxTokens    uint64
	Temperature  float64
	TopP         float64
	StopPatterns []string
}

func (c Configuration) encode() []byte {
	var b []byte
	b = appendUint(b, 1, 1)
	b = appendUint(b, 2, c.MaxTokens)
	b = appendUint(b, 3, 400)
	b = appendDouble(b, 5, c.Temperature)
	b = appendUint(b, 7, 40)
	b = appendDouble(b, 8, c.TopP)
	for _, pattern := range c.StopPatterns {
		b = protowire.AppendTag(b, 9, protowire.BytesType)
		b = protowire.AppendString(b, pattern)
	}
	return b
}

// ChatRequest is exa.api_server_pb.GetChatMessageRequest as NeoPi builds it.
type ChatRequest struct {
	Metadata                 Metadata
	Prompt                   string
	Prompts                  []Prompt
	ChatModelUID             string
	Configuration            Configuration
	Tools                    []Tool
	DisableParallelToolCalls bool
	CascadeID                string
	ExecutionID              string
	// ModelAssignmentJWT is set only for router models.
	ModelAssignmentJWT *string
}

// Encode is the request's protobuf bytes.
func (r ChatRequest) Encode() []byte {
	var b []byte
	b = appendMessage(b, 1, r.Metadata.encode())
	b = appendString(b, 2, r.Prompt)
	for _, prompt := range r.Prompts {
		b = appendMessage(b, 3, prompt.Encode())
	}
	b = appendString(b, 21, r.ChatModelUID)
	b = appendUint(b, 7, requestTypeCascade)
	b = appendMessage(b, 8, r.Configuration.encode())
	for _, tool := range r.Tools {
		b = appendMessage(b, 10, tool.encode())
	}
	b = appendBool(b, 11, r.DisableParallelToolCalls)
	b = appendMessage(b, 12, appendString(nil, 1, "auto"))
	b = appendMessage(b, 13, appendUint(nil, 1, cacheEphemeral))
	b = appendString(b, 16, r.CascadeID)
	b = appendUint(b, 20, plannerModeDefault)
	b = appendString(b, 22, r.ExecutionID)
	if r.ModelAssignmentJWT != nil {
		b = protowire.AppendTag(b, 26, protowire.BytesType)
		b = protowire.AppendString(b, *r.ModelAssignmentJWT)
	}
	return b
}

// HistoryBytes is the size of a request carrying only prompts, the measure
// NeoPi's large-history recovery compares.
func HistoryBytes(prompts []Prompt) int {
	return len(ChatRequest{Prompts: prompts}.encodeHistoryOnly())
}

func (r ChatRequest) encodeHistoryOnly() []byte {
	var b []byte
	for _, prompt := range r.Prompts {
		b = appendMessage(b, 3, prompt.Encode())
	}
	return b
}

// EncodeUserJWTRequest is exa.auth_pb.GetUserJwtRequest.
func EncodeUserJWTRequest(metadata Metadata) []byte {
	return appendMessage(nil, 1, metadata.encode())
}

// EncodeAssignModelRequest is exa.api_server_pb.AssignModelRequest.
func EncodeAssignModelRequest(metadata Metadata, routerUID, cascadeID string, prompt *Prompt) []byte {
	b := appendMessage(nil, 1, metadata.encode())
	b = appendString(b, 2, routerUID)
	b = appendString(b, 3, cascadeID)
	if prompt != nil {
		b = appendMessage(b, 5, prompt.Encode())
	}
	return b
}

// field walks one message's top-level fields.
func fields(b []byte, visit func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		var value []byte
		var varint uint64
		switch typ {
		case protowire.VarintType:
			varint, n = protowire.ConsumeVarint(b)
		case protowire.Fixed64Type:
			varint, n = protowire.ConsumeFixed64(b)
		case protowire.Fixed32Type:
			var v uint32
			v, n = protowire.ConsumeFixed32(b)
			varint = uint64(v)
		case protowire.BytesType:
			value, n = protowire.ConsumeBytes(b)
		default:
			return fmt.Errorf("unsupported protobuf wire type %d", typ)
		}
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if err := visit(num, typ, value, varint); err != nil {
			return err
		}
	}
	return nil
}

// UserJWTResponse is exa.auth_pb.GetUserJwtResponse.
type UserJWTResponse struct{ UserJWT, CustomAPIServerURL string }

// DecodeUserJWTResponse decodes a GetUserJwt response.
func DecodeUserJWTResponse(b []byte) (UserJWTResponse, error) {
	var out UserJWTResponse
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1:
			out.UserJWT = string(value)
		case 2:
			out.CustomAPIServerURL = string(value)
		}
		return nil
	})
	return out, err
}

// Assignment is exa.api_server_pb.ModelAssignment.
type Assignment struct{ JWT, ModelUID string }

// DecodeAssignModelResponse decodes an AssignModel response's assignment.
func DecodeAssignModelResponse(b []byte) (Assignment, error) {
	var out Assignment
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
		if num != 1 || typ != protowire.BytesType {
			return nil
		}
		return fields(value, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
			if typ != protowire.BytesType {
				return nil
			}
			switch num {
			case 1:
				out.JWT = string(value)
			case 2:
				out.ModelUID = string(value)
			}
			return nil
		})
	})
	return out, err
}

// Usage is exa.codeium_common_pb.ModelUsageStats's token counts.
type Usage struct{ Input, Output, CacheWrite, CacheRead uint64 }

// ChatResponse is one exa.api_server_pb.GetChatMessageResponse frame, the
// fields NeoPi's stream reads.
type ChatResponse struct {
	MessageID           string
	DeltaText           string
	StopReason          int
	DeltaToolCalls      []ToolCall
	Usage               *Usage
	CreditCost          int32
	DeltaThinking       string
	DeltaSignature      string
	CommittedCreditCost int32
	CommittedAcuCost    float64
	ActualModelUID      string
}

// DecodeChatResponse decodes one stream frame.
func DecodeChatResponse(b []byte) (ChatResponse, error) {
	var out ChatResponse
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch {
		case typ == protowire.BytesType && num == 1:
			out.MessageID = string(value)
		case typ == protowire.BytesType && num == 3:
			out.DeltaText = string(value)
		case typ == protowire.VarintType && num == 5:
			out.StopReason = int(int32(varint))
		case typ == protowire.BytesType && num == 6:
			var call ToolCall
			if err := fields(value, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
				if typ != protowire.BytesType {
					return nil
				}
				switch num {
				case 1:
					call.ID = string(value)
				case 2:
					call.Name = string(value)
				case 3:
					call.ArgumentsJSON = string(value)
				}
				return nil
			}); err != nil {
				return err
			}
			out.DeltaToolCalls = append(out.DeltaToolCalls, call)
		case typ == protowire.BytesType && num == 7:
			usage := &Usage{}
			if err := fields(value, func(num protowire.Number, typ protowire.Type, _ []byte, varint uint64) error {
				if typ != protowire.VarintType {
					return nil
				}
				switch num {
				case 2:
					usage.Input = varint
				case 3:
					usage.Output = varint
				case 4:
					usage.CacheWrite = varint
				case 5:
					usage.CacheRead = varint
				}
				return nil
			}); err != nil {
				return err
			}
			out.Usage = usage
		case typ == protowire.VarintType && num == 14:
			out.CreditCost = int32(varint)
		case typ == protowire.BytesType && num == 9:
			out.DeltaThinking = string(value)
		case typ == protowire.BytesType && num == 10:
			out.DeltaSignature = string(value)
		case typ == protowire.VarintType && num == 18:
			out.CommittedCreditCost = int32(varint)
		case typ == protowire.Fixed64Type && num == 22:
			out.CommittedAcuCost = math.Float64frombits(varint)
		case typ == protowire.BytesType && num == 23:
			out.ActualModelUID = string(value)
		}
		return nil
	})
	return out, err
}
