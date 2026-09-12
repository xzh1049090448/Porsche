package openaicompat

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"unicode/utf8"
)

type responsesRequestDTO struct {
	Model             string            `json:"model"`
	Instructions      *string           `json:"instructions"`
	Input             json.RawMessage   `json:"input"`
	Tools             []json.RawMessage `json:"tools"`
	ToolChoice        json.RawMessage   `json:"tool_choice"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls"`
	MaxOutputTokens   *json.Number      `json:"max_output_tokens"`
	Temperature       *json.Number      `json:"temperature"`
	TopP              *json.Number      `json:"top_p"`
	Stream            *bool             `json:"stream"`
	Store             *bool             `json:"store"`
	PreviousResponse  json.RawMessage   `json:"previous_response_id"`
}

var responsesRequestFields = map[string]struct{}{
	"model": {}, "instructions": {}, "input": {}, "tools": {}, "tool_choice": {},
	"parallel_tool_calls": {}, "max_output_tokens": {}, "temperature": {}, "top_p": {},
	"stream": {}, "store": {}, "previous_response_id": {},
}

type responseMessageItemDTO struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type responseFunctionCallDTO struct {
	Type      string  `json:"type"`
	ID        *string `json:"id"`
	Status    *string `json:"status"`
	CallID    string  `json:"call_id"`
	Name      string  `json:"name"`
	Arguments string  `json:"arguments"`
}

type responseFunctionOutputDTO struct {
	Type   string  `json:"type"`
	ID     *string `json:"id"`
	Status *string `json:"status"`
	CallID string  `json:"call_id"`
	Output string  `json:"output"`
}

type responseTextPartDTO struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responseToolDTO struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict"`
}

var responseMessageItemFields = map[string]struct{}{"type": {}, "role": {}, "content": {}}
var responseFunctionCallFields = map[string]struct{}{"type": {}, "id": {}, "status": {}, "call_id": {}, "name": {}, "arguments": {}}
var responseFunctionOutputFields = map[string]struct{}{"type": {}, "id": {}, "status": {}, "call_id": {}, "output": {}}
var responseTextPartFields = map[string]struct{}{"type": {}, "text": {}}
var responseToolFields = map[string]struct{}{"type": {}, "name": {}, "description": {}, "parameters": {}, "strict": {}}
var responseToolChoiceFields = map[string]struct{}{"type": {}, "name": {}}

func DecodeResponses(body []byte) (Conversation, *Error) {
	if len(body) > MaxRequestBodyBytes {
		return Conversation{}, RequestTooLarge()
	}
	if hasUnknownFields(body, responsesRequestFields) {
		return Conversation{}, UnsupportedParameter()
	}
	var request responsesRequestDTO
	if decodeStrict(body, &request) != nil || strings.TrimSpace(request.Model) == "" || len(request.Input) == 0 {
		return Conversation{}, InvalidRequest()
	}
	if request.Store != nil && *request.Store || len(request.PreviousResponse) != 0 && !bytes.Equal(bytes.TrimSpace(request.PreviousResponse), []byte("null")) {
		return Conversation{}, UnsupportedParameter()
	}
	maxOutput, ok := numberInt(request.MaxOutputTokens, 1, math.MaxInt64)
	if !ok {
		return Conversation{}, InvalidRequest()
	}
	temperature, ok := numberFloat(request.Temperature, 0, 2, true)
	if !ok {
		return Conversation{}, InvalidRequest()
	}
	topP, ok := numberFloat(request.TopP, 0, 1, false)
	if !ok {
		return Conversation{}, InvalidRequest()
	}
	messages, err := decodeResponseInput(request.Input)
	if err != nil {
		return Conversation{}, err
	}
	tools, err := decodeResponseTools(request.Tools)
	if err != nil {
		return Conversation{}, err
	}
	choice, err := decodeResponseToolChoice(request.ToolChoice)
	if err != nil {
		return Conversation{}, err
	}
	parallel := true
	if request.ParallelToolCalls != nil {
		parallel = *request.ParallelToolCalls
	}
	conversation := Conversation{Model: request.Model, Messages: messages, Tools: tools, ToolChoice: choice, ParallelToolCalls: &parallel, MaxOutputTokens: maxOutput, Temperature: temperature, TopP: topP, Stream: request.Stream != nil && *request.Stream}
	if request.Instructions != nil {
		if !utf8.ValidString(*request.Instructions) || len(*request.Instructions) > MaxTextContentBytes {
			return Conversation{}, InvalidRequest()
		}
		conversation.Instructions = []Message{{Role: RoleDeveloper, Content: *request.Instructions}}
	}
	if err := validateConversation(conversation); err != nil {
		return Conversation{}, err
	}
	return conversation, nil
}

func decodeResponseInput(raw json.RawMessage) ([]Message, *Error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if !utf8.ValidString(text) || len(text) > MaxTextContentBytes {
			return nil, InvalidRequest()
		}
		return []Message{{Role: RoleUser, Content: text}}, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 || len(items) > MaxMessages*2 {
		return nil, InvalidRequest()
	}
	messages := make([]Message, 0, len(items))
	for _, item := range items {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &kind) != nil {
			return nil, InvalidRequest()
		}
		switch kind.Type {
		case "message":
			if hasUnknownFields(item, responseMessageItemFields) {
				return nil, UnsupportedParameter()
			}
			var dto responseMessageItemDTO
			if decodeStrict(item, &dto) != nil {
				return nil, InvalidRequest()
			}
			if responseTextHasUnknownFields(dto.Content) {
				return nil, UnsupportedParameter()
			}
			content, ok := decodeResponseText(dto.Content)
			if !ok {
				return nil, InvalidRequest()
			}
			messages = append(messages, Message{Role: Role(dto.Role), Content: content})
		case "function_call":
			if hasUnknownFields(item, responseFunctionCallFields) {
				return nil, UnsupportedParameter()
			}
			var dto responseFunctionCallDTO
			if decodeStrict(item, &dto) != nil || !validOptionalItemMetadata(dto.ID, dto.Status) {
				return nil, InvalidRequest()
			}
			if !utf8.ValidString(dto.Arguments) {
				return nil, InvalidRequest()
			}
			if len(dto.Arguments) > MaxArgumentsBytes {
				return nil, RequestTooLarge()
			}
			call := ToolCall{ID: dto.CallID, Name: dto.Name, Arguments: dto.Arguments}
			if len(messages) > 0 && messages[len(messages)-1].Role == RoleAssistant && messages[len(messages)-1].Content == nil && len(messages[len(messages)-1].ToolCalls) > 0 {
				messages[len(messages)-1].ToolCalls = append(messages[len(messages)-1].ToolCalls, call)
			} else {
				messages = append(messages, Message{Role: RoleAssistant, ToolCalls: []ToolCall{call}})
			}
		case "function_call_output":
			if hasUnknownFields(item, responseFunctionOutputFields) {
				return nil, UnsupportedParameter()
			}
			var dto responseFunctionOutputDTO
			if decodeStrict(item, &dto) != nil || !validOptionalItemMetadata(dto.ID, dto.Status) || !utf8.ValidString(dto.Output) {
				return nil, InvalidRequest()
			}
			if len(dto.Output) > MaxToolOutputBytes {
				return nil, RequestTooLarge()
			}
			messages = append(messages, Message{Role: RoleTool, ToolCallID: dto.CallID, Content: dto.Output})
		default:
			return nil, UnsupportedParameter()
		}
	}
	return messages, nil
}

func decodeResponseText(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, utf8.ValidString(text) && len(text) <= MaxTextContentBytes
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return "", false
	}
	var builder strings.Builder
	for _, part := range parts {
		if hasUnknownFields(part, responseTextPartFields) {
			return "", false
		}
		var dto responseTextPartDTO
		if decodeStrict(part, &dto) != nil || dto.Type != "input_text" && dto.Type != "output_text" || !utf8.ValidString(dto.Text) || builder.Len()+len(dto.Text) > MaxTextContentBytes {
			return "", false
		}
		builder.WriteString(dto.Text)
	}
	return builder.String(), true
}

func responseTextHasUnknownFields(raw json.RawMessage) bool {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return false
	}
	for _, part := range parts {
		if hasUnknownFields(part, responseTextPartFields) {
			return true
		}
	}
	return false
}

func decodeResponseTools(raw []json.RawMessage) ([]ToolDefinition, *Error) {
	if len(raw) > MaxTools {
		return nil, InvalidRequest()
	}
	out := make([]ToolDefinition, 0, len(raw))
	for _, item := range raw {
		var kind struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(item, &kind) != nil {
			return nil, InvalidRequest()
		}
		if kind.Type != "function" {
			return nil, UnsupportedParameter()
		}
		if hasUnknownFields(item, responseToolFields) {
			return nil, UnsupportedParameter()
		}
		var dto responseToolDTO
		if decodeStrict(item, &dto) != nil || !validFunctionName(dto.Name) || !validJSONObject(dto.Parameters) {
			return nil, InvalidRequest()
		}
		description := ""
		if dto.Description != nil {
			description = *dto.Description
		}
		if !utf8.ValidString(description) || len(description) > MaxTextContentBytes {
			return nil, InvalidRequest()
		}
		out = append(out, ToolDefinition{Name: dto.Name, Description: description, Parameters: cloneRaw(dto.Parameters), Strict: dto.Strict})
	}
	return out, nil
}

func decodeResponseToolChoice(raw json.RawMessage) (ToolChoice, *Error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ToolChoice{Mode: "auto"}, nil
	}
	var mode string
	if json.Unmarshal(raw, &mode) == nil {
		if mode == "none" || mode == "auto" || mode == "required" {
			return ToolChoice{Mode: mode}, nil
		}
		return ToolChoice{}, InvalidRequest()
	}
	var dto struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if hasUnknownFields(raw, responseToolChoiceFields) {
		return ToolChoice{}, UnsupportedParameter()
	}
	if decodeStrict(raw, &dto) != nil || dto.Type != "function" || !validFunctionName(dto.Name) {
		return ToolChoice{}, InvalidRequest()
	}
	return ToolChoice{Mode: "function", Name: dto.Name}, nil
}

func validOptionalItemMetadata(id, status *string) bool {
	if id != nil && (!utf8.ValidString(*id) || len(*id) == 0 || len(*id) > MaxCallIDBytes) {
		return false
	}
	if status == nil {
		return true
	}
	switch *status {
	case "in_progress", "completed", "incomplete":
		return true
	default:
		return false
	}
}
