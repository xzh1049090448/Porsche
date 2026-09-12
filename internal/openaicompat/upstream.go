package openaicompat

import "encoding/json"

type upstreamRequest struct {
	Model             string                 `json:"model"`
	Messages          []upstreamMessage      `json:"messages"`
	MaxTokens         *int64                 `json:"max_tokens,omitempty"`
	Temperature       *float64               `json:"temperature,omitempty"`
	TopP              *float64               `json:"top_p,omitempty"`
	FrequencyPenalty  *float64               `json:"frequency_penalty,omitempty"`
	PresencePenalty   *float64               `json:"presence_penalty,omitempty"`
	Stop              json.RawMessage        `json:"stop,omitempty"`
	Seed              *int64                 `json:"seed,omitempty"`
	N                 *int64                 `json:"n,omitempty"`
	Tools             []upstreamTool         `json:"tools,omitempty"`
	ToolChoice        any                    `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool                  `json:"parallel_tool_calls,omitempty"`
	ResponseFormat    json.RawMessage        `json:"response_format,omitempty"`
	Stream            bool                   `json:"stream,omitempty"`
	StreamOptions     *upstreamStreamOptions `json:"stream_options,omitempty"`
}

type upstreamMessage struct {
	Role       string             `json:"role"`
	Content    any                `json:"content"`
	ToolCalls  []upstreamToolCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
}

type upstreamToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"`
	Function upstreamFunctionCall `json:"function"`
}

type upstreamFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type upstreamTool struct {
	Type     string               `json:"type"`
	Function upstreamFunctionTool `json:"function"`
}

type upstreamFunctionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type upstreamStreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type upstreamNamedToolChoice struct {
	Type     string                     `json:"type"`
	Function upstreamNamedToolReference `json:"function"`
}

type upstreamNamedToolReference struct {
	Name string `json:"name"`
}

func EncodeUpstream(conversation Conversation) ([]byte, error) {
	if err := validateConversation(conversation); err != nil {
		return nil, err
	}
	messages := make([]upstreamMessage, 0, len(conversation.Instructions)+len(conversation.Messages))
	for _, message := range append(append([]Message(nil), conversation.Instructions...), conversation.Messages...) {
		role := string(message.Role)
		if message.Role == RoleDeveloper {
			role = string(RoleSystem)
		}
		encoded := upstreamMessage{Role: role, Content: message.Content, ToolCallID: message.ToolCallID}
		for _, call := range message.ToolCalls {
			encoded.ToolCalls = append(encoded.ToolCalls, upstreamToolCall{ID: call.ID, Type: "function", Function: upstreamFunctionCall{Name: call.Name, Arguments: call.Arguments}})
		}
		messages = append(messages, encoded)
	}
	tools := make([]upstreamTool, 0, len(conversation.Tools))
	for _, tool := range conversation.Tools {
		tools = append(tools, upstreamTool{Type: "function", Function: upstreamFunctionTool{Name: tool.Name, Description: tool.Description, Parameters: cloneRaw(tool.Parameters), Strict: tool.Strict}})
	}
	var choice any
	switch conversation.ToolChoice.Mode {
	case "":
	case "none", "auto", "required":
		choice = conversation.ToolChoice.Mode
	case "function":
		choice = upstreamNamedToolChoice{Type: "function", Function: upstreamNamedToolReference{Name: conversation.ToolChoice.Name}}
	default:
		return nil, InvalidRequest()
	}
	request := upstreamRequest{Model: conversation.Model, Messages: messages, MaxTokens: conversation.MaxOutputTokens, Temperature: conversation.Temperature, TopP: conversation.TopP, FrequencyPenalty: conversation.FrequencyPenalty, PresencePenalty: conversation.PresencePenalty, Stop: cloneRaw(conversation.Stop), Seed: conversation.Seed, N: conversation.N, Tools: tools, ToolChoice: choice, ParallelToolCalls: conversation.ParallelToolCalls, ResponseFormat: cloneRaw(conversation.ResponseFormat), Stream: conversation.Stream}
	if conversation.IncludeUsage {
		request.StreamOptions = &upstreamStreamOptions{IncludeUsage: true}
	}
	return json.Marshal(request)
}
