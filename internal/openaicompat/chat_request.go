package openaicompat

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"unicode/utf8"
)

type chatRequestDTO struct {
	Model               string            `json:"model"`
	Messages            []json.RawMessage `json:"messages"`
	MaxTokens           *json.Number      `json:"max_tokens"`
	MaxCompletionTokens *json.Number      `json:"max_completion_tokens"`
	Temperature         *json.Number      `json:"temperature"`
	TopP                *json.Number      `json:"top_p"`
	FrequencyPenalty    *json.Number      `json:"frequency_penalty"`
	PresencePenalty     *json.Number      `json:"presence_penalty"`
	Stop                json.RawMessage   `json:"stop"`
	Seed                *json.Number      `json:"seed"`
	N                   *json.Number      `json:"n"`
	Tools               []chatToolDTO     `json:"tools"`
	ToolChoice          json.RawMessage   `json:"tool_choice"`
	ParallelToolCalls   *bool             `json:"parallel_tool_calls"`
	ResponseFormat      json.RawMessage   `json:"response_format"`
	Stream              *bool             `json:"stream"`
	StreamOptions       *streamOptionsDTO `json:"stream_options"`
}

type chatMessageDTO struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCalls  []toolCallDTO   `json:"tool_calls"`
	ToolCallID string          `json:"tool_call_id"`
}

type toolCallDTO struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	Function functionCallDTO `json:"function"`
}

type functionCallDTO struct{ Name, Arguments string }

type chatToolDTO struct {
	Type     string           `json:"type"`
	Function *functionToolDTO `json:"function"`
}

type functionToolDTO struct {
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict"`
}

type streamOptionsDTO struct {
	IncludeUsage *bool `json:"include_usage"`
}

func DecodeChat(body []byte) (Conversation, *Error) {
	if len(body) > MaxRequestBodyBytes {
		return Conversation{}, RequestTooLarge()
	}
	if hasUnknownFields(body, chatRequestFields) {
		return Conversation{}, UnsupportedParameter()
	}
	var request chatRequestDTO
	if decodeStrict(body, &request) != nil || request.Messages == nil || len(request.Messages) > MaxMessages || strings.TrimSpace(request.Model) == "" {
		return Conversation{}, InvalidRequest()
	}
	if request.MaxTokens != nil && request.MaxCompletionTokens != nil {
		return Conversation{}, InvalidRequest()
	}
	limit := request.MaxTokens
	if limit == nil {
		limit = request.MaxCompletionTokens
	}
	maxOutput, ok := numberInt(limit, 1, math.MaxInt64)
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
	frequency, ok := numberFloat(request.FrequencyPenalty, -2, 2, true)
	if !ok {
		return Conversation{}, InvalidRequest()
	}
	presence, ok := numberFloat(request.PresencePenalty, -2, 2, true)
	if !ok {
		return Conversation{}, InvalidRequest()
	}
	seed, ok := numberInt(request.Seed, math.MinInt64, math.MaxInt64)
	if !ok {
		return Conversation{}, InvalidRequest()
	}
	n, ok := numberInt(request.N, 1, 128)
	if !ok {
		return Conversation{}, InvalidRequest()
	}
	messages := make([]Message, 0, len(request.Messages))
	for _, raw := range request.Messages {
		message, err := decodeChatMessage(raw)
		if err != nil {
			return Conversation{}, err
		}
		messages = append(messages, message)
	}
	tools, err := decodeChatTools(request.Tools)
	if err != nil {
		return Conversation{}, err
	}
	choice, err := decodeToolChoice(request.ToolChoice)
	if err != nil {
		return Conversation{}, err
	}
	responseFormat, responseFormatOK := decodeResponseFormat(request.ResponseFormat)
	if !validStop(request.Stop) || !responseFormatOK || request.StreamOptions != nil && request.StreamOptions.IncludeUsage == nil {
		return Conversation{}, InvalidRequest()
	}
	conversation := Conversation{Model: request.Model, Messages: messages, Tools: tools, ToolChoice: choice, ParallelToolCalls: request.ParallelToolCalls, MaxOutputTokens: maxOutput, Temperature: temperature, TopP: topP, FrequencyPenalty: frequency, PresencePenalty: presence, Stop: cloneRaw(request.Stop), Seed: seed, N: n, ResponseFormat: responseFormat, Stream: request.Stream != nil && *request.Stream, IncludeUsage: request.StreamOptions != nil && request.StreamOptions.IncludeUsage != nil && *request.StreamOptions.IncludeUsage}
	if err := validateConversation(conversation); err != nil {
		return Conversation{}, err
	}
	return conversation, nil
}

var chatRequestFields = map[string]struct{}{
	"model": {}, "messages": {}, "max_tokens": {}, "max_completion_tokens": {},
	"temperature": {}, "top_p": {}, "frequency_penalty": {}, "presence_penalty": {},
	"stop": {}, "seed": {}, "n": {}, "tools": {}, "tool_choice": {},
	"parallel_tool_calls": {}, "response_format": {}, "stream": {}, "stream_options": {},
}

func decodeChatMessage(raw json.RawMessage) (Message, *Error) {
	var dto chatMessageDTO
	if decodeStrict(raw, &dto) != nil {
		return Message{}, InvalidRequest()
	}
	message := Message{Role: Role(dto.Role), ToolCallID: dto.ToolCallID}
	switch message.Role {
	case RoleSystem, RoleDeveloper:
		content, ok := decodeContent(dto.Content, false, false, MaxTextContentBytes)
		if !ok {
			return Message{}, InvalidRequest()
		}
		message.Content = content
	case RoleUser:
		content, ok := decodeContent(dto.Content, false, true, MaxTextContentBytes)
		if !ok {
			return Message{}, InvalidRequest()
		}
		message.Content = content
	case RoleAssistant:
		content, ok := decodeContent(dto.Content, true, false, MaxTextContentBytes)
		if !ok && len(dto.ToolCalls) == 0 {
			return Message{}, InvalidRequest()
		}
		message.Content = content
		for _, call := range dto.ToolCalls {
			if call.Type != "function" || !utf8.ValidString(call.Function.Arguments) {
				return Message{}, InvalidRequest()
			}
			message.ToolCalls = append(message.ToolCalls, ToolCall{ID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
		}
	case RoleTool:
		content, ok := decodeContent(dto.Content, false, false, MaxToolOutputBytes)
		if !ok {
			return Message{}, InvalidRequest()
		}
		message.Content = content
	default:
		return Message{}, InvalidRequest()
	}
	return message, nil
}

func decodeChatTools(input []chatToolDTO) ([]ToolDefinition, *Error) {
	if len(input) > MaxTools {
		return nil, InvalidRequest()
	}
	out := make([]ToolDefinition, 0, len(input))
	for _, item := range input {
		if item.Type != "function" || item.Function == nil || !validFunctionName(item.Function.Name) || !validJSONObject(item.Function.Parameters) {
			return nil, InvalidRequest()
		}
		description := ""
		if item.Function.Description != nil {
			description = *item.Function.Description
		}
		if !utf8.ValidString(description) || len(description) > MaxTextContentBytes {
			return nil, InvalidRequest()
		}
		out = append(out, ToolDefinition{Name: item.Function.Name, Description: description, Parameters: cloneRaw(item.Function.Parameters), Strict: item.Function.Strict})
	}
	return out, nil
}

func decodeToolChoice(raw json.RawMessage) (ToolChoice, *Error) {
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
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if decodeStrict(raw, &dto) != nil || dto.Type != "function" || !validFunctionName(dto.Function.Name) {
		return ToolChoice{}, InvalidRequest()
	}
	return ToolChoice{Mode: "function", Name: dto.Function.Name}, nil
}

func validStop(raw json.RawMessage) bool {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return true
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return utf8.ValidString(one)
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil || len(many) > 4 {
		return false
	}
	for _, value := range many {
		if !utf8.ValidString(value) {
			return false
		}
	}
	return true
}

func decodeResponseFormat(raw json.RawMessage) (json.RawMessage, bool) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var dto struct {
		Type       string          `json:"type"`
		JSONSchema json.RawMessage `json:"json_schema"`
	}
	if decodeStrict(raw, &dto) != nil {
		return nil, false
	}
	switch dto.Type {
	case "text", "json_object":
		if len(dto.JSONSchema) != 0 {
			return nil, false
		}
		encoded, err := json.Marshal(struct {
			Type string `json:"type"`
		}{Type: dto.Type})
		return encoded, err == nil
	case "json_schema":
		var schema struct {
			Name        string          `json:"name"`
			Description *string         `json:"description"`
			Schema      json.RawMessage `json:"schema"`
			Strict      *bool           `json:"strict"`
		}
		if decodeStrict(dto.JSONSchema, &schema) != nil || strings.TrimSpace(schema.Name) == "" || !validJSONObject(schema.Schema) {
			return nil, false
		}
		if schema.Description != nil && (!utf8.ValidString(*schema.Description) || len(*schema.Description) > MaxTextContentBytes) {
			return nil, false
		}
		encoded, err := json.Marshal(struct {
			Type       string `json:"type"`
			JSONSchema any    `json:"json_schema"`
		}{Type: "json_schema", JSONSchema: schema})
		return encoded, err == nil
	default:
		return nil, false
	}
}

func cloneRaw(raw json.RawMessage) json.RawMessage { return append(json.RawMessage(nil), raw...) }
