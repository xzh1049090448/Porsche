package openaicompat

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

type IDSource func(prefix string) (string, error)

type Response struct {
	ID                 string               `json:"id"`
	Object             string               `json:"object"`
	CreatedAt          int64                `json:"created_at"`
	Status             string               `json:"status"`
	Error              any                  `json:"error"`
	IncompleteDetails  any                  `json:"incomplete_details"`
	Model              string               `json:"model"`
	Output             []ResponseOutputItem `json:"output"`
	ParallelToolCalls  bool                 `json:"parallel_tool_calls"`
	PreviousResponseID any                  `json:"previous_response_id"`
	Store              bool                 `json:"store"`
	Usage              *ResponseUsage       `json:"usage,omitempty"`
}

type ResponseOutputItem struct {
	ID        string            `json:"id"`
	Type      string            `json:"type"`
	Status    string            `json:"status"`
	Role      string            `json:"role,omitempty"`
	Content   []ResponseContent `json:"content,omitempty"`
	CallID    string            `json:"call_id,omitempty"`
	Name      string            `json:"name,omitempty"`
	Arguments string            `json:"arguments,omitempty"`
}

func (item ResponseOutputItem) MarshalJSON() ([]byte, error) {
	type wireItem struct {
		ID        string            `json:"id"`
		Type      string            `json:"type"`
		Status    string            `json:"status"`
		Role      string            `json:"role,omitempty"`
		Content   []ResponseContent `json:"content,omitempty"`
		CallID    string            `json:"call_id,omitempty"`
		Name      string            `json:"name,omitempty"`
		Arguments *string           `json:"arguments,omitempty"`
	}
	var arguments *string
	if item.Type == "function_call" {
		arguments = &item.Arguments
	}
	return json.Marshal(wireItem{ID: item.ID, Type: item.Type, Status: item.Status, Role: item.Role, Content: item.Content, CallID: item.CallID, Name: item.Name, Arguments: arguments})
}

type ResponseContent struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	Annotations []any  `json:"annotations"`
}

type ResponseUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func ProjectResponse(completion whitelabel.ChatCompletion, parallel bool, ids IDSource) (Response, error) {
	if ids == nil {
		ids = randomID
	}
	if completion.Model == "" || completion.Created < 0 || len(completion.Choices) != 1 || completion.Choices[0].Message.Role != "assistant" {
		return Response{}, errors.New("invalid projected completion")
	}
	responseID, err := ids("resp")
	if err != nil {
		return Response{}, err
	}
	output := make([]ResponseOutputItem, 0, 1+len(completion.Choices[0].Message.ToolCalls))
	message := completion.Choices[0].Message
	if text, ok := responseText(message.Content); ok {
		messageID, idErr := ids("msg")
		if idErr != nil {
			return Response{}, idErr
		}
		output = append(output, ResponseOutputItem{ID: messageID, Type: "message", Status: "completed", Role: "assistant", Content: []ResponseContent{{Type: "output_text", Text: text, Annotations: []any{}}}})
	}
	for _, call := range message.ToolCalls {
		itemID, idErr := ids("fc")
		if idErr != nil {
			return Response{}, idErr
		}
		output = append(output, ResponseOutputItem{ID: itemID, Type: "function_call", Status: "completed", CallID: call.ID, Name: call.Function.Name, Arguments: call.Function.Arguments})
	}
	if len(output) == 0 {
		return Response{}, errors.New("empty projected completion")
	}
	response := Response{ID: responseID, Object: "response", CreatedAt: completion.Created, Status: "completed", Model: completion.Model, Output: output, ParallelToolCalls: parallel, Store: false}
	if completion.Usage != nil {
		response.Usage = &ResponseUsage{InputTokens: completion.Usage.PromptTokens, OutputTokens: completion.Usage.CompletionTokens, TotalTokens: completion.Usage.TotalTokens}
	}
	return response, nil
}

func responseText(content any) (string, bool) {
	switch value := content.(type) {
	case string:
		return value, true
	case []whitelabel.ChatCompletionContentPart:
		var text strings.Builder
		for _, part := range value {
			text.WriteString(part.Text)
		}
		return text.String(), true
	case nil:
		return "", false
	default:
		return "", false
	}
}

func randomID(prefix string) (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buffer), nil
}
