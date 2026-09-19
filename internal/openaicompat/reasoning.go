package openaicompat

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// ThinkingMode is the canonical value of the Chat Completions "thinking"
// switch. Only the two frozen DeepSeek Harness wire values are accepted.
type ThinkingMode string

const (
	ThinkingEnabled  ThinkingMode = "enabled"
	ThinkingDisabled ThinkingMode = "disabled"
)

// ReasoningEfforts is the complete accepted reasoning-effort enum shared by the
// Chat "reasoning_effort" field and the Responses "reasoning.effort" field.
var ReasoningEfforts = map[string]struct{}{
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"max":     {},
}

// ReasoningPolicy declares which logical models may carry reasoning fields. The
// zero value, or any policy with no selector, rejects every reasoning field so
// a default deployment keeps the pre-existing contract unchanged.
type ReasoningPolicy struct {
	Models   map[string]struct{}
	Patterns []*regexp.Regexp
}

// NoReasoning is the fail-closed policy: no model accepts reasoning fields.
var NoReasoning = ReasoningPolicy{}

// Allows reports whether model may carry reasoning fields. Exact selectors are
// matched first, then the compiled RE2 patterns; a nil pattern is skipped.
func (p ReasoningPolicy) Allows(model string) bool {
	model = strings.TrimSpace(model)
	if _, ok := p.Models[model]; ok {
		return true
	}
	for _, pattern := range p.Patterns {
		if pattern == nil {
			continue
		}
		if pattern.MatchString(model) {
			return true
		}
	}
	return false
}

// Empty reports whether the policy opts in no model.
func (p ReasoningPolicy) Empty() bool {
	return len(p.Models) == 0 && len(p.Patterns) == 0
}

var thinkingRequestFields = map[string]struct{}{"type": {}}

// decodeThinking validates the Chat "thinking" object. Every shape problem
// (unknown nested field, missing/extra field, wrong JSON type, unknown enum
// value) is classified as unsupported_parameter by the frozen design.
func decodeThinking(raw json.RawMessage) (*ThinkingMode, *Error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	if hasUnknownFields(raw, thinkingRequestFields) {
		return nil, UnsupportedParameter()
	}
	var dto struct {
		Type string `json:"type"`
	}
	if decodeStrict(raw, &dto) != nil {
		return nil, UnsupportedParameter()
	}
	mode := ThinkingMode(dto.Type)
	if mode != ThinkingEnabled && mode != ThinkingDisabled {
		return nil, UnsupportedParameter()
	}
	return &mode, nil
}

// decodeReasoningEffort validates the Chat "reasoning_effort" value. A
// non-string or out-of-enum value is a known-field shape error: invalid_request.
func decodeReasoningEffort(value *string) (string, *Error) {
	if value == nil {
		return "", nil
	}
	if _, ok := ReasoningEfforts[*value]; !ok {
		return "", InvalidRequest()
	}
	return *value, nil
}

var responseReasoningFields = map[string]struct{}{"effort": {}}

// decodeResponseReasoning validates the Responses "reasoning" object and maps
// its optional "effort" value onto the canonical Chat effort. Unknown nested
// fields keep the existing nested-unknown classification.
func decodeResponseReasoning(raw json.RawMessage) (string, *Error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	if hasUnknownFields(raw, responseReasoningFields) {
		return "", UnsupportedParameter()
	}
	var dto struct {
		Effort *string `json:"effort"`
	}
	if decodeStrict(raw, &dto) != nil || dto.Effort != nil && !validReasoningEffort(*dto.Effort) {
		return "", InvalidRequest()
	}
	if dto.Effort == nil {
		return "", nil
	}
	return *dto.Effort, nil
}

func validReasoningEffort(effort string) bool {
	_, ok := ReasoningEfforts[effort]
	return ok
}

// reasoningAllowed reports whether a request that carries any reasoning control
// (top-level effort, thinking, or assistant reasoning_content) may proceed for
// model. A request that carries none is always allowed.
func reasoningAllowed(policy ReasoningPolicy, model, effort string, thinking *ThinkingMode, messages []Message) bool {
	carried := effort != "" || thinking != nil
	if !carried {
		for _, message := range messages {
			if message.ReasoningContent != nil {
				carried = true
				break
			}
		}
	}
	return !carried || policy.Allows(model)
}
