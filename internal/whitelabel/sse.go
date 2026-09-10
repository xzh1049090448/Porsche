package whitelabel

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	return s.consumeChatCompletionSSEContext(ctx, reader, logicalModelID, func(chunk ChatCompletionChunk) error {
		encoded, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		frame := append([]byte("data: "), encoded...)
		frame = append(frame, '\n', '\n')
		return emit(frame)
	}, func() error {
		return emit([]byte("data: [DONE]\n\n"))
	})
}

// ConsumeChatCompletionSSEContext consumes upstream SSE frames and invokes emit
// with each client-safe projected chunk. The exact [DONE] frame terminates the
// stream but is not exposed as a synthetic chunk.
func (s *WhiteLabelService) ConsumeChatCompletionSSEContext(ctx context.Context, reader io.Reader, logicalModelID string, emit func(ChatCompletionChunk) error) *Error {
	return s.consumeChatCompletionSSEContext(ctx, reader, logicalModelID, emit, nil)
}

func (s *WhiteLabelService) consumeChatCompletionSSEContext(
	ctx context.Context,
	reader io.Reader,
	logicalModelID string,
	emitChunk func(ChatCompletionChunk) error,
	emitDone func() error,
) *Error {
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
						if emitDone != nil {
							if emitErr := emitDone(); emitErr != nil {
								reason := diagnostics.Write
								if ctx.Err() != nil {
									reason = diagnostics.NetworkReason(ctx.Err())
								}
								return fail(reason, "stream write failed")
							}
						}
						end(diagnostics.OK)
						return nil
					} else {
						projected, failure := projectChatCompletionChunkDetail([]byte(payload), logicalModelID)
						if failure != nil {
							if trace := diagnostics.From(ctx); trace != nil {
								enrichObjectFailure([]byte(payload), failure)
								trace.MalformedChunk(failure.Reason, failure.Field, failure.Object)
							}
							return fail(diagnostics.Malformed, "malformed chat completion chunk")
						}
						if emitErr := emitChunk(projected); emitErr != nil {
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

// Legacy helpers preserve the original sentinel contract. Detailed helpers add only
// the fixed classification at the same rejection branch, in the same order.
func projectChatCompletionChunk(data []byte, logicalModelID string) (ChatCompletionChunk, error) {
	projected, failure := projectChatCompletionChunkDetail(data, logicalModelID)
	if failure != nil {
		return ChatCompletionChunk{}, errMalformedCompletion
	}
	return projected, nil
}
func projectChatCompletionChunkDetail(data []byte, logicalModelID string) (ChatCompletionChunk, *diagnostics.ChunkFailure) {
	var upstream struct {
		ID      string                `json:"id"`
		Object  string                `json:"object"`
		Created int64                 `json:"created"`
		Choices []upstreamChunkChoice `json:"choices"`
		Usage   *ChatCompletionUsage  `json:"usage"`
	}
	if err := json.Unmarshal(data, &upstream); err != nil {
		return ChatCompletionChunk{}, chunkJSONFailure(err, diagnostics.ChunkRoot)
	}
	if upstream.ID == "" {
		return ChatCompletionChunk{}, chunkFailure(diagnostics.ChunkMissing, diagnostics.ChunkID)
	}
	if upstream.Object != "chat.completion.chunk" {
		kind := diagnostics.ObjectDecodedOther
		switch upstream.Object {
		case "":
			kind = diagnostics.ObjectDecodedEmpty
		case "chat.completion":
			kind = diagnostics.ObjectDecodedKnownChatCompletion
		}
		failure := chunkFailure(diagnostics.ChunkInvalidValue, diagnostics.ChunkObject)
		failure.Object = &diagnostics.ObjectDetail{DecodedKind: kind, FieldShape: diagnostics.ObjectShapeUnknown, KeyMatch: diagnostics.ObjectKeyUnknown}
		return ChatCompletionChunk{}, failure
	}
	if upstream.Created < 0 {
		return ChatCompletionChunk{}, chunkFailure(diagnostics.ChunkNegative, diagnostics.ChunkCreated)
	}
	if !validCompletionUsage(upstream.Usage) {
		field := diagnostics.ChunkUsageTotal
		if upstream.Usage.PromptTokens < 0 {
			field = diagnostics.ChunkUsagePrompt
		} else if upstream.Usage.CompletionTokens < 0 {
			field = diagnostics.ChunkUsageCompletion
		}
		return ChatCompletionChunk{}, chunkFailure(diagnostics.ChunkNegative, field)
	}
	if len(upstream.Choices) == 0 && upstream.Usage == nil {
		return ChatCompletionChunk{}, chunkFailure(diagnostics.ChunkMissing, diagnostics.ChunkChoices)
	}
	choices := make([]ChatCompletionChunkChoice, 0, len(upstream.Choices))
	for _, choice := range upstream.Choices {
		delta, failure := projectChunkDeltaDetail(choice.Delta)
		if failure != nil {
			return ChatCompletionChunk{}, failure
		}
		if choice.Index < 0 {
			return ChatCompletionChunk{}, chunkFailure(diagnostics.ChunkNegative, diagnostics.ChunkChoiceIndex)
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
	delta, failure := projectChunkDeltaDetail(raw)
	if failure != nil {
		return ChatCompletionChunkDelta{}, errMalformedCompletion
	}
	return delta, nil
}
func projectChunkDeltaDetail(raw json.RawMessage) (ChatCompletionChunkDelta, *diagnostics.ChunkFailure) {
	var upstream struct {
		Role      *string         `json:"role"`
		Content   *string         `json:"content"`
		Refusal   *string         `json:"refusal"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	if len(raw) == 0 {
		return ChatCompletionChunkDelta{}, chunkFailure(diagnostics.ChunkMissing, diagnostics.ChunkDelta)
	}
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return ChatCompletionChunkDelta{}, chunkJSONFailure(err, diagnostics.ChunkDelta)
	}
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return ChatCompletionChunkDelta{}, chunkFailure(diagnostics.ChunkInvalidShape, diagnostics.ChunkDelta)
	}
	tools, failure := projectChunkToolCallsDetail(upstream.ToolCalls)
	if failure != nil {
		return ChatCompletionChunkDelta{}, failure
	}
	return ChatCompletionChunkDelta{Role: upstream.Role, Content: upstream.Content, Refusal: upstream.Refusal, ToolCalls: tools}, nil
}
func projectChunkToolCalls(raw json.RawMessage) ([]ChatCompletionChunkToolCall, error) {
	calls, failure := projectChunkToolCallsDetail(raw)
	if failure != nil {
		return nil, errMalformedCompletion
	}
	return calls, nil
}
func projectChunkToolCallsDetail(raw json.RawMessage) ([]ChatCompletionChunkToolCall, *diagnostics.ChunkFailure) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var upstream []struct {
		Index    int             `json:"index"`
		ID       *string         `json:"id"`
		Type     *string         `json:"type"`
		Function json.RawMessage `json:"function"`
	}
	if err := json.Unmarshal(raw, &upstream); err != nil {
		return nil, chunkJSONFailure(err, diagnostics.ChunkTools)
	}
	projected := make([]ChatCompletionChunkToolCall, 0, len(upstream))
	for _, call := range upstream {
		if call.Index < 0 {
			return nil, chunkFailure(diagnostics.ChunkNegative, diagnostics.ChunkToolIndex)
		}
		var function *ChatCompletionChunkFunctionCall
		if len(call.Function) != 0 && !bytes.Equal(bytes.TrimSpace(call.Function), []byte("null")) {
			var upstreamFunction ChatCompletionChunkFunctionCall
			if err := json.Unmarshal(call.Function, &upstreamFunction); err != nil {
				return nil, chunkJSONFailure(err, diagnostics.ChunkFunction)
			}
			if !bytes.HasPrefix(bytes.TrimSpace(call.Function), []byte("{")) {
				return nil, chunkFailure(diagnostics.ChunkInvalidShape, diagnostics.ChunkFunction)
			}
			function = &upstreamFunction
		}
		projected = append(projected, ChatCompletionChunkToolCall{Index: call.Index, ID: call.ID, Type: call.Type, Function: function})
	}
	return projected, nil
}
func chunkFailure(reason diagnostics.ChunkReason, field diagnostics.ChunkField) *diagnostics.ChunkFailure {
	return &diagnostics.ChunkFailure{Reason: reason, Field: field}
}

// UnmarshalTypeError.Field is only an input to this exact allowlist lookup. No
// upstream field name, value, dynamic index, or error text reaches diagnostics.
func chunkJSONFailure(err error, scope diagnostics.ChunkField) *diagnostics.ChunkFailure {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		return chunkFailure(diagnostics.ChunkJSONSyntax, scope)
	}
	var typeError *json.UnmarshalTypeError
	if errors.As(err, &typeError) {
		if typeError.Field == "" {
			return chunkFailure(diagnostics.ChunkJSONType, scope)
		}
		fields := map[diagnostics.ChunkField]map[string]diagnostics.ChunkField{
			diagnostics.ChunkRoot:     {"id": diagnostics.ChunkID, "object": diagnostics.ChunkObject, "created": diagnostics.ChunkCreated, "choices": diagnostics.ChunkChoices, "choices.index": diagnostics.ChunkChoiceIndex, "choices.finish_reason": diagnostics.ChunkFinishReason, "usage": diagnostics.ChunkUsage, "usage.prompt_tokens": diagnostics.ChunkUsagePrompt, "usage.completion_tokens": diagnostics.ChunkUsageCompletion, "usage.total_tokens": diagnostics.ChunkUsageTotal},
			diagnostics.ChunkDelta:    {"role": diagnostics.ChunkDeltaRole, "content": diagnostics.ChunkDeltaContent, "refusal": diagnostics.ChunkDeltaRefusal},
			diagnostics.ChunkTools:    {"index": diagnostics.ChunkToolIndex, "id": diagnostics.ChunkToolID, "type": diagnostics.ChunkToolType},
			diagnostics.ChunkFunction: {"name": diagnostics.ChunkFunctionName, "arguments": diagnostics.ChunkFunctionArguments},
		}
		field, ok := fields[scope][typeError.Field]
		if !ok {
			field = diagnostics.ChunkFieldUnknown
		}
		return chunkFailure(diagnostics.ChunkJSONType, field)
	}
	return chunkFailure(diagnostics.ChunkUnknown, diagnostics.ChunkFieldUnknown)
}
