package whitelabel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"

	"github.com/porsche/ai-gateway-go/internal/diagnostics"
)

// ProjectChatCompletionSSE consumes upstream SSE frames and invokes emit with
// client-safe OpenAI data frames only. Upstream SSE fields are never retained.
func (s *WhiteLabelService) ProjectChatCompletionSSE(reader io.Reader, logicalModelID string, emit func([]byte) error) *Error {
	return s.ProjectChatCompletionSSEContext(context.Background(), reader, logicalModelID, emit)
}

// ProjectChatCompletionSSEContext retains the public projection contract and records fixed diagnostics when present.
func (s *WhiteLabelService) ProjectChatCompletionSSEContext(ctx context.Context, reader io.Reader, logicalModelID string, emit func([]byte) error) *Error {
	end := diagnostics.From(ctx).Begin(diagnostics.Stream)
	fail := func(reason diagnostics.Reason, detail string) *Error {
		end(reason)
		return ErrUpstreamUnavailable(detail)
	}
	if !validModelID(logicalModelID) {
		return fail(diagnostics.Invalid, "invalid logical model")
	}
	buffered := bufio.NewReader(reader)
	var dataLines []string
	for {
		line, err := buffered.ReadString('\n')
		if err != nil && err != io.EOF {
			reason := diagnostics.NetworkReason(err)
			if reason == diagnostics.Network {
				reason = diagnostics.Read
			}
			if ctx.Err() != nil {
				reason = diagnostics.NetworkReason(ctx.Err())
			}
			return fail(reason, "stream read failed")
		}
		if len(line) > 0 {
			line = strings.TrimSuffix(line, "\n")
			line = strings.TrimSuffix(line, "\r")
			if line == "" {
				if len(dataLines) > 0 {
					payload := strings.Join(dataLines, "\n")
					dataLines = nil
					if payload == "[DONE]" {
						if emitErr := emit([]byte("data: [DONE]\n\n")); emitErr != nil {
							reason := diagnostics.Write
							if ctx.Err() != nil {
								reason = diagnostics.NetworkReason(ctx.Err())
							}
							return fail(reason, "stream write failed")
						}
						end(diagnostics.OK)
						return nil
					} else {
						projected, projectErr := projectChatCompletionChunk([]byte(payload), logicalModelID)
						if projectErr != nil {
							return fail(diagnostics.Malformed, "malformed chat completion chunk")
						}
						encoded, marshalErr := json.Marshal(projected)
						if marshalErr != nil {
							return fail(diagnostics.Invalid, "chunk encoding failed")
						}
						frame := append([]byte("data: "), encoded...)
						frame = append(frame, '\n', '\n')
						if emitErr := emit(frame); emitErr != nil {
							reason := diagnostics.Write
							if ctx.Err() != nil {
								reason = diagnostics.NetworkReason(ctx.Err())
							}
							return fail(reason, "stream write failed")
						}
					}
				}
			} else if strings.HasPrefix(line, "data:") {
				value := strings.TrimPrefix(line, "data:")
				dataLines = append(dataLines, strings.TrimPrefix(value, " "))
			}
		}
		if err == io.EOF {
			reason := diagnostics.Incomplete
			if ctx.Err() != nil {
				reason = diagnostics.NetworkReason(ctx.Err())
			}
			return fail(reason, "incomplete stream")
		}
	}
}

type ChatCompletionChunk struct {
	ID      string                      `json:"id"`
	Object  string                      `json:"object"`
	Created int64                       `json:"created"`
	Model   string                      `json:"model"`
	Choices []ChatCompletionChunkChoice `json:"choices"`
	Usage   *ChatCompletionUsage        `json:"usage,omitempty"`
}

type ChatCompletionChunkChoice struct {
	Index        int                      `json:"index"`
	Delta        ChatCompletionChunkDelta `json:"delta"`
	FinishReason *string                  `json:"finish_reason"`
}

type ChatCompletionChunkDelta struct {
	Role      *string                       `json:"role,omitempty"`
	Content   *string                       `json:"content,omitempty"`
	Refusal   *string                       `json:"refusal,omitempty"`
	ToolCalls []ChatCompletionChunkToolCall `json:"tool_calls,omitempty"`
}

type ChatCompletionChunkToolCall struct {
	Index    int                              `json:"index"`
	ID       *string                          `json:"id,omitempty"`
	Type     *string                          `json:"type,omitempty"`
	Function *ChatCompletionChunkFunctionCall `json:"function,omitempty"`
}

type ChatCompletionChunkFunctionCall struct {
	Name      *string `json:"name,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
}

func projectChatCompletionChunk(data []byte, logicalModelID string) (ChatCompletionChunk, error) {
	var upstream struct {
		ID      string                `json:"id"`
		Object  string                `json:"object"`
		Created int64                 `json:"created"`
		Choices []upstreamChunkChoice `json:"choices"`
		Usage   *ChatCompletionUsage  `json:"usage"`
	}
	if json.Unmarshal(data, &upstream) != nil || upstream.ID == "" || upstream.Object != "chat.completion.chunk" || upstream.Created < 0 || !validCompletionUsage(upstream.Usage) || (len(upstream.Choices) == 0 && upstream.Usage == nil) {
		return ChatCompletionChunk{}, errMalformedCompletion
	}
	choices := make([]ChatCompletionChunkChoice, 0, len(upstream.Choices))
	for _, choice := range upstream.Choices {
		delta, err := projectChunkDelta(choice.Delta)
		if err != nil || choice.Index < 0 {
			return ChatCompletionChunk{}, errMalformedCompletion
		}
		choices = append(choices, ChatCompletionChunkChoice{Index: choice.Index, Delta: delta, FinishReason: choice.FinishReason})
	}
	return ChatCompletionChunk{ID: upstream.ID, Object: upstream.Object, Created: upstream.Created, Model: logicalModelID, Choices: choices, Usage: upstream.Usage}, nil
}

type upstreamChunkChoice struct {
	Index        int             `json:"index"`
	Delta        json.RawMessage `json:"delta"`
	FinishReason *string         `json:"finish_reason"`
}

func projectChunkDelta(raw json.RawMessage) (ChatCompletionChunkDelta, error) {
	var upstream struct {
		Role      *string         `json:"role"`
		Content   *string         `json:"content"`
		Refusal   *string         `json:"refusal"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &upstream) != nil || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return ChatCompletionChunkDelta{}, errMalformedCompletion
	}
	toolCalls, err := projectChunkToolCalls(upstream.ToolCalls)
	if err != nil {
		return ChatCompletionChunkDelta{}, errMalformedCompletion
	}
	return ChatCompletionChunkDelta{Role: upstream.Role, Content: upstream.Content, Refusal: upstream.Refusal, ToolCalls: toolCalls}, nil
}

func projectChunkToolCalls(raw json.RawMessage) ([]ChatCompletionChunkToolCall, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var upstream []struct {
		Index    int             `json:"index"`
		ID       *string         `json:"id"`
		Type     *string         `json:"type"`
		Function json.RawMessage `json:"function"`
	}
	if json.Unmarshal(raw, &upstream) != nil {
		return nil, errMalformedCompletion
	}
	projected := make([]ChatCompletionChunkToolCall, 0, len(upstream))
	for _, call := range upstream {
		if call.Index < 0 {
			return nil, errMalformedCompletion
		}
		var function *ChatCompletionChunkFunctionCall
		if len(call.Function) != 0 && !bytes.Equal(bytes.TrimSpace(call.Function), []byte("null")) {
			var upstreamFunction ChatCompletionChunkFunctionCall
			if json.Unmarshal(call.Function, &upstreamFunction) != nil || !bytes.HasPrefix(bytes.TrimSpace(call.Function), []byte("{")) {
				return nil, errMalformedCompletion
			}
			function = &upstreamFunction
		}
		projected = append(projected, ChatCompletionChunkToolCall{Index: call.Index, ID: call.ID, Type: call.Type, Function: function})
	}
	return projected, nil
}
