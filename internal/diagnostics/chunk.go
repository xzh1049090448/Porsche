package diagnostics

// ChunkReason and ChunkField are closed diagnostic vocabularies, never upstream text.
type ChunkReason string

const (
	ChunkUnknown      ChunkReason = "unknown"
	ChunkJSONSyntax   ChunkReason = "json_syntax"
	ChunkJSONType     ChunkReason = "json_type"
	ChunkMissing      ChunkReason = "missing_required"
	ChunkInvalidValue ChunkReason = "invalid_value"
	ChunkNegative     ChunkReason = "negative_value"
	ChunkInvalidShape ChunkReason = "invalid_shape"
)

type ChunkField string

const (
	ChunkFieldUnknown      ChunkField = "unknown"
	ChunkRoot              ChunkField = "chunk"
	ChunkID                ChunkField = "id"
	ChunkObject            ChunkField = "object"
	ChunkCreated           ChunkField = "created"
	ChunkUsage             ChunkField = "usage"
	ChunkUsagePrompt       ChunkField = "usage.prompt_tokens"
	ChunkUsageCompletion   ChunkField = "usage.completion_tokens"
	ChunkUsageTotal        ChunkField = "usage.total_tokens"
	ChunkChoices           ChunkField = "choices"
	ChunkChoiceIndex       ChunkField = "choices[].index"
	ChunkFinishReason      ChunkField = "choices[].finish_reason"
	ChunkDelta             ChunkField = "choices[].delta"
	ChunkDeltaRole         ChunkField = "choices[].delta.role"
	ChunkDeltaContent      ChunkField = "choices[].delta.content"
	ChunkDeltaRefusal      ChunkField = "choices[].delta.refusal"
	ChunkTools             ChunkField = "choices[].delta.tool_calls"
	ChunkToolIndex         ChunkField = "choices[].delta.tool_calls[].index"
	ChunkToolID            ChunkField = "choices[].delta.tool_calls[].id"
	ChunkToolType          ChunkField = "choices[].delta.tool_calls[].type"
	ChunkFunction          ChunkField = "choices[].delta.tool_calls[].function"
	ChunkFunctionName      ChunkField = "choices[].delta.tool_calls[].function.name"
	ChunkFunctionArguments ChunkField = "choices[].delta.tool_calls[].function.arguments"
)

type ChunkFailure struct {
	Reason ChunkReason `json:"reason"`
	Field  ChunkField  `json:"field"`
}

func (t *Trace) MalformedChunk(reason ChunkReason, field ChunkField) {
	if t == nil {
		return
	}
	switch reason {
	case ChunkUnknown, ChunkJSONSyntax, ChunkJSONType, ChunkMissing, ChunkInvalidValue, ChunkNegative, ChunkInvalidShape:
	default:
		reason = ChunkUnknown
	}
	switch field {
	case ChunkFieldUnknown, ChunkRoot, ChunkID, ChunkObject, ChunkCreated, ChunkUsage, ChunkUsagePrompt, ChunkUsageCompletion, ChunkUsageTotal, ChunkChoices, ChunkChoiceIndex, ChunkFinishReason, ChunkDelta, ChunkDeltaRole, ChunkDeltaContent, ChunkDeltaRefusal, ChunkTools, ChunkToolIndex, ChunkToolID, ChunkToolType, ChunkFunction, ChunkFunctionName, ChunkFunctionArguments:
	default:
		field = ChunkFieldUnknown
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.record.MalformedChunkDetail = &ChunkFailure{Reason: reason, Field: field}
}
