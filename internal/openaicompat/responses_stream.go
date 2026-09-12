package openaicompat

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/whitelabel"
)

type ResponseEvent struct {
	Type           string              `json:"type"`
	SequenceNumber int64               `json:"sequence_number"`
	Response       *Response           `json:"response,omitempty"`
	OutputIndex    *int                `json:"output_index,omitempty"`
	ContentIndex   *int                `json:"content_index,omitempty"`
	ItemID         string              `json:"item_id,omitempty"`
	Item           *ResponseOutputItem `json:"item,omitempty"`
	Part           *ResponseContent    `json:"part,omitempty"`
	Delta          string              `json:"delta,omitempty"`
	Text           string              `json:"text,omitempty"`
	Arguments      string              `json:"arguments,omitempty"`
}

type ResponsesStream struct {
	model      string
	parallel   bool
	ids        IDSource
	responseID string
	createdAt  int64
	sequence   int64
	started    bool
	terminal   bool
	output     []ResponseOutputItem
	text       *streamTextState
	tools      map[int]*streamToolState
	callIDs    map[string]int
	usage      *ResponseUsage
}

type streamTextState struct {
	itemID      string
	outputIndex int
	text        strings.Builder
}

type streamToolState struct {
	itemID      string
	outputIndex int
	callID      string
	name        string
	arguments   strings.Builder
	started     bool
}

func NewResponsesStream(model string, parallel bool, ids IDSource) *ResponsesStream {
	if ids == nil {
		ids = randomID
	}
	return &ResponsesStream{model: model, parallel: parallel, ids: ids, tools: make(map[int]*streamToolState), callIDs: make(map[string]int)}
}

func (s *ResponsesStream) Accept(chunk whitelabel.ChatCompletionChunk, emit func(ResponseEvent) error) error {
	if s.terminal || emit == nil || validateResponseChunk(chunk, s.model) != nil {
		return errors.New("invalid Responses stream chunk")
	}
	if !s.started {
		if err := s.start(chunk.Created, emit); err != nil {
			return err
		}
	}
	if chunk.Usage != nil {
		s.usage = &ResponseUsage{InputTokens: chunk.Usage.PromptTokens, OutputTokens: chunk.Usage.CompletionTokens, TotalTokens: chunk.Usage.TotalTokens}
	}
	if len(chunk.Choices) == 0 {
		return nil
	}
	choice := chunk.Choices[0]
	if choice.Delta.Content != nil {
		if err := s.acceptText(*choice.Delta.Content, emit); err != nil {
			return err
		}
	}
	for _, call := range choice.Delta.ToolCalls {
		if err := s.acceptTool(call, emit); err != nil {
			return err
		}
	}
	return nil
}

func (s *ResponsesStream) Complete(emit func(ResponseEvent) error) error {
	if !s.started || s.terminal || emit == nil {
		return errors.New("invalid Responses stream completion")
	}
	if s.text != nil {
		index, contentIndex := s.text.outputIndex, 0
		text := s.text.text.String()
		if err := s.emit(ResponseEvent{Type: "response.output_text.done", OutputIndex: &index, ContentIndex: &contentIndex, ItemID: s.text.itemID, Text: text}, emit); err != nil {
			return err
		}
		part := ResponseContent{Type: "output_text", Text: text, Annotations: []any{}}
		if err := s.emit(ResponseEvent{Type: "response.content_part.done", OutputIndex: &index, ContentIndex: &contentIndex, ItemID: s.text.itemID, Part: &part}, emit); err != nil {
			return err
		}
		item := ResponseOutputItem{ID: s.text.itemID, Type: "message", Status: "completed", Role: "assistant", Content: []ResponseContent{part}}
		s.output[index] = item
		if err := s.emit(ResponseEvent{Type: "response.output_item.done", OutputIndex: &index, Item: &item}, emit); err != nil {
			return err
		}
	}
	tools := make([]*streamToolState, 0, len(s.tools))
	for _, tool := range s.tools {
		if !tool.started {
			return errors.New("incomplete streamed tool identity")
		}
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].outputIndex < tools[j].outputIndex })
	for _, tool := range tools {
		index := tool.outputIndex
		arguments := tool.arguments.String()
		if err := s.emit(ResponseEvent{Type: "response.function_call_arguments.done", OutputIndex: &index, ItemID: tool.itemID, Arguments: arguments}, emit); err != nil {
			return err
		}
		item := ResponseOutputItem{ID: tool.itemID, Type: "function_call", Status: "completed", CallID: tool.callID, Name: tool.name, Arguments: arguments}
		s.output[index] = item
		if err := s.emit(ResponseEvent{Type: "response.output_item.done", OutputIndex: &index, Item: &item}, emit); err != nil {
			return err
		}
	}
	response := s.snapshot("completed")
	s.terminal = true
	return s.emit(ResponseEvent{Type: "response.completed", Response: &response}, emit)
}

func (s *ResponsesStream) Failed(code string, emit func(ResponseEvent) error) error {
	if !s.started || s.terminal || emit == nil {
		return errors.New("invalid Responses stream failure")
	}
	response := s.snapshot("failed")
	response.Error = map[string]any{"code": code, "message": "The upstream service is unavailable."}
	s.terminal = true
	return s.emit(ResponseEvent{Type: "response.failed", Response: &response}, emit)
}

func (s *ResponsesStream) Started() bool { return s.started }

func (s *ResponsesStream) start(createdAt int64, emit func(ResponseEvent) error) error {
	id, err := s.ids("resp")
	if err != nil {
		return err
	}
	s.responseID, s.createdAt, s.started = id, createdAt, true
	created := s.snapshot("in_progress")
	if err := s.emit(ResponseEvent{Type: "response.created", Response: &created}, emit); err != nil {
		return err
	}
	inProgress := s.snapshot("in_progress")
	return s.emit(ResponseEvent{Type: "response.in_progress", Response: &inProgress}, emit)
}

func (s *ResponsesStream) acceptText(delta string, emit func(ResponseEvent) error) error {
	buffered := 0
	if s.text != nil {
		buffered = s.text.text.Len()
	}
	if len(delta) > MaxTextContentBytes-buffered {
		return errors.New("streamed text too large")
	}
	if s.text == nil {
		id, err := s.ids("msg")
		if err != nil {
			return err
		}
		index := len(s.output)
		s.text = &streamTextState{itemID: id, outputIndex: index}
		item := ResponseOutputItem{ID: id, Type: "message", Status: "in_progress", Role: "assistant", Content: []ResponseContent{}}
		s.output = append(s.output, item)
		if err := s.emit(ResponseEvent{Type: "response.output_item.added", OutputIndex: &index, Item: &item}, emit); err != nil {
			return err
		}
		contentIndex := 0
		part := ResponseContent{Type: "output_text", Text: "", Annotations: []any{}}
		if err := s.emit(ResponseEvent{Type: "response.content_part.added", OutputIndex: &index, ContentIndex: &contentIndex, ItemID: id, Part: &part}, emit); err != nil {
			return err
		}
	}
	_, _ = s.text.text.WriteString(delta)
	index, contentIndex := s.text.outputIndex, 0
	return s.emit(ResponseEvent{Type: "response.output_text.delta", OutputIndex: &index, ContentIndex: &contentIndex, ItemID: s.text.itemID, Delta: delta}, emit)
}

func (s *ResponsesStream) acceptTool(call whitelabel.ChatCompletionChunkToolCall, emit func(ResponseEvent) error) error {
	state := s.tools[call.Index]
	if state == nil {
		if len(s.tools) >= MaxParallelCalls {
			return errors.New("too many streamed tool calls")
		}
		state = &streamToolState{outputIndex: -1}
		s.tools[call.Index] = state
	}
	if call.ID != nil {
		if state.callID != "" && state.callID != *call.ID {
			return errors.New("streamed tool call id changed")
		}
		if otherIndex, duplicate := s.callIDs[*call.ID]; duplicate && otherIndex != call.Index {
			return errors.New("streamed tool call id reused")
		}
		state.callID = *call.ID
		s.callIDs[*call.ID] = call.Index
	}
	if call.Type != nil && *call.Type != "function" {
		return errors.New("unsupported streamed tool type")
	}
	argumentDelta := ""
	if call.Function != nil {
		if call.Function.Name != nil {
			if state.name != "" && state.name != *call.Function.Name {
				return errors.New("streamed function name changed")
			}
			state.name = *call.Function.Name
		}
		if call.Function.Arguments != nil {
			argumentDelta = *call.Function.Arguments
			if len(argumentDelta) > MaxArgumentsBytes-state.arguments.Len() {
				return errors.New("streamed function arguments too large")
			}
			_, _ = state.arguments.WriteString(argumentDelta)
		}
	}
	if !state.started && state.callID != "" && state.name != "" {
		id, err := s.ids("fc")
		if err != nil {
			return err
		}
		state.itemID, state.outputIndex, state.started = id, len(s.output), true
		item := ResponseOutputItem{ID: id, Type: "function_call", Status: "in_progress", CallID: state.callID, Name: state.name, Arguments: ""}
		s.output = append(s.output, item)
		index := state.outputIndex
		if err := s.emit(ResponseEvent{Type: "response.output_item.added", OutputIndex: &index, Item: &item}, emit); err != nil {
			return err
		}
		argumentDelta = state.arguments.String()
	}
	if state.started && argumentDelta != "" {
		index := state.outputIndex
		return s.emit(ResponseEvent{Type: "response.function_call_arguments.delta", OutputIndex: &index, ItemID: state.itemID, Delta: argumentDelta}, emit)
	}
	return nil
}

func validateResponseChunk(chunk whitelabel.ChatCompletionChunk, model string) error {
	if chunk.Model != model || chunk.Created < 0 || len(chunk.Choices) > 1 {
		return errors.New("invalid chunk envelope")
	}
	if len(chunk.Choices) == 0 {
		return nil
	}
	choice := chunk.Choices[0]
	if choice.Index != 0 {
		return errors.New("unsupported choice index")
	}
	for _, call := range choice.Delta.ToolCalls {
		if call.Index < 0 || call.Index >= MaxParallelCalls || call.Type != nil && *call.Type != "function" || call.ID != nil && !validCallID(*call.ID) {
			return errors.New("invalid tool delta")
		}
		if call.Function == nil {
			continue
		}
		if call.Function.Name != nil && !validFunctionName(*call.Function.Name) {
			return errors.New("invalid function name delta")
		}
		if call.Function.Arguments != nil && (!utf8.ValidString(*call.Function.Arguments) || len(*call.Function.Arguments) > MaxArgumentsBytes) {
			return errors.New("invalid function arguments delta")
		}
	}
	return nil
}

func (s *ResponsesStream) snapshot(status string) Response {
	output := append([]ResponseOutputItem(nil), s.output...)
	if output == nil {
		output = []ResponseOutputItem{}
	}
	return Response{ID: s.responseID, Object: "response", CreatedAt: s.createdAt, Status: status, Model: s.model, Output: output, ParallelToolCalls: s.parallel, Store: false, Usage: s.usage}
}

func (s *ResponsesStream) emit(event ResponseEvent, emit func(ResponseEvent) error) error {
	s.sequence++
	event.SequenceNumber = s.sequence
	return emit(event)
}
