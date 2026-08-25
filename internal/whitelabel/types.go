package whitelabel

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// ChatCompletion is the client-safe subset of a non-streaming OpenAI chat
// completion. Model is always the logical model ID selected by this gateway,
// rather than an upstream provider identifier.
type ChatCompletion struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   *ChatCompletionUsage   `json:"usage,omitempty"`
}

// ChatCompletionChoice omits logprobs intentionally: no stable, minimal
// public logprobs schema is required by this gateway, so upstream nested data
// is never forwarded as an opaque value.
type ChatCompletionChoice struct {
	Index        int                   `json:"index"`
	Message      ChatCompletionMessage `json:"message"`
	FinishReason *string               `json:"finish_reason"`
}

// ChatCompletionMessage is the allowed public completion-message surface.
// It deliberately excludes provider-specific nested fields.
type ChatCompletionMessage struct {
	Role      string                   `json:"role"`
	Content   any                      `json:"content"`
	Refusal   *string                  `json:"refusal,omitempty"`
	ToolCalls []ChatCompletionToolCall `json:"tool_calls,omitempty"`
}

type ChatCompletionContentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ChatCompletionToolCall struct {
	ID       string                     `json:"id"`
	Type     string                     `json:"type"`
	Function ChatCompletionFunctionCall `json:"function"`
}

type ChatCompletionFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatMessage is the internal, OpenAI-compatible request message passed to
// the sole white-label upstream. It is intentionally provider-neutral.
type ChatMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

// ChatCompletionRequest holds the platform fields needed to build a
// white-label request after platform validation.
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream,omitempty"`
}

// ProjectChatCompletion validates the minimal OpenAI completion shape and
// drops all upstream-owned fields that are not part of the public contract.
func (s *WhiteLabelService) ProjectChatCompletion(data []byte, logicalModelID string) (ChatCompletion, *Error) {
	var upstream struct {
		ID      string                     `json:"id"`
		Object  string                     `json:"object"`
		Created int64                      `json:"created"`
		Choices []upstreamCompletionChoice `json:"choices"`
		Usage   *ChatCompletionUsage       `json:"usage"`
	}
	if !validModelID(logicalModelID) || json.Unmarshal(data, &upstream) != nil || upstream.ID == "" || upstream.Object != "chat.completion" || upstream.Created < 0 || len(upstream.Choices) == 0 || !validCompletionUsage(upstream.Usage) {
		return ChatCompletion{}, ErrUpstreamUnavailable("malformed chat completion")
	}
	choices, choicesErr := projectCompletionChoices(upstream.Choices)
	if choicesErr != nil {
		return ChatCompletion{}, ErrUpstreamUnavailable("malformed chat completion")
	}
	return ChatCompletion{ID: upstream.ID, Object: upstream.Object, Created: upstream.Created, Model: logicalModelID, Choices: choices, Usage: upstream.Usage}, nil
}

type upstreamCompletionChoice struct {
	Index        int             `json:"index"`
	Message      json.RawMessage `json:"message"`
	FinishReason *string         `json:"finish_reason"`
}

func projectCompletionChoices(upstream []upstreamCompletionChoice) ([]ChatCompletionChoice, error) {
	choices := make([]ChatCompletionChoice, 0, len(upstream))
	for _, choice := range upstream {
		message, err := projectCompletionMessage(choice.Message)
		if choice.Index < 0 || err != nil {
			return nil, errMalformedCompletion
		}
		choices = append(choices, ChatCompletionChoice{Index: choice.Index, Message: message, FinishReason: choice.FinishReason})
	}
	return choices, nil
}

var errMalformedCompletion = &completionProjectionError{}

type completionProjectionError struct{}

func (*completionProjectionError) Error() string { return "malformed completion" }

func projectCompletionMessage(raw json.RawMessage) (ChatCompletionMessage, error) {
	var upstream struct {
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		Refusal   *string         `json:"refusal"`
		ToolCalls json.RawMessage `json:"tool_calls"`
	}
	if json.Unmarshal(raw, &upstream) != nil || strings.TrimSpace(upstream.Role) == "" {
		return ChatCompletionMessage{}, errMalformedCompletion
	}
	content, err := projectCompletionContent(upstream.Content)
	if err != nil {
		return ChatCompletionMessage{}, errMalformedCompletion
	}
	toolCalls, err := projectToolCalls(upstream.ToolCalls)
	if err != nil {
		return ChatCompletionMessage{}, errMalformedCompletion
	}
	return ChatCompletionMessage{Role: upstream.Role, Content: content, Refusal: upstream.Refusal, ToolCalls: toolCalls}, nil
}

func projectCompletionContent(raw json.RawMessage) (any, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []ChatCompletionContentPart
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return nil, errMalformedCompletion
	}
	for _, part := range parts {
		if part.Type != "text" {
			return nil, errMalformedCompletion
		}
	}
	return parts, nil
}

func projectToolCalls(raw json.RawMessage) ([]ChatCompletionToolCall, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var calls []ChatCompletionToolCall
	if json.Unmarshal(raw, &calls) != nil {
		return nil, errMalformedCompletion
	}
	for _, call := range calls {
		if call.ID == "" || call.Type != "function" || call.Function.Name == "" {
			return nil, errMalformedCompletion
		}
	}
	return calls, nil
}

func validCompletionUsage(usage *ChatCompletionUsage) bool {
	return usage == nil || (usage.PromptTokens >= 0 && usage.CompletionTokens >= 0 && usage.TotalTokens >= 0)
}

const CodeModelUnavailable Code = "model_unavailable"

const (
	catalogTTL = 5 * 60 * 1_000_000_000
	detailTTL  = 60 * 60 * 1_000_000_000
	staleTTL   = 24 * 60 * 60 * 1_000_000_000
	// maxModelMetadataTextBytes bounds retained upstream title and description
	// text, limiting memory consumed by an individual model response.
	maxModelMetadataTextBytes = 4 * 1024
)

// Model is a safe, normalized view of an upstream model. IDs remain opaque:
// consumers must only compare them for equality and pass them back unchanged.
type Model struct {
	ID                   string  `json:"id"`
	Object               string  `json:"object,omitempty"`
	Created              int64   `json:"created,omitempty"`
	OwnedBy              string  `json:"owned_by,omitempty"`
	Title                string  `json:"title,omitempty"`
	Description          string  `json:"description,omitempty"`
	ContextWindow        int64   `json:"context_window,omitempty"`
	MaxTokens            int64   `json:"max_tokens,omitempty"`
	InputTokenPricePerM  float64 `json:"input_token_price_per_m,omitempty"`
	OutputTokenPricePerM float64 `json:"output_token_price_per_m,omitempty"`
}

// Catalog contains the models a caller may use. CatalogStale indicates the
// data was retained after a failed refresh and is at most 24 hours old.
type Catalog struct {
	Data         []Model `json:"data"`
	CatalogStale bool    `json:"catalog_stale"`
}

type upstreamModel struct {
	ID                   string  `json:"id"`
	Object               string  `json:"object"`
	Created              float64 `json:"created"`
	OwnedBy              string  `json:"owned_by"`
	Title                string  `json:"title"`
	Description          string  `json:"description"`
	ContextWindow        float64 `json:"context_window"`
	MaxTokens            float64 `json:"max_tokens"`
	InputTokenPricePerM  float64 `json:"input_token_price_per_m"`
	OutputTokenPricePerM float64 `json:"output_token_price_per_m"`
}

func normalizeModels(models []upstreamModel) []Model {
	seen := make(map[string]struct{}, len(models))
	out := make([]Model, 0, len(models))
	for _, upstream := range models {
		if !validModelID(upstream.ID) {
			continue
		}
		if _, duplicate := seen[upstream.ID]; duplicate {
			continue
		}
		seen[upstream.ID] = struct{}{}
		out = append(out, Model{
			ID: upstream.ID, Object: safeText(upstream.Object), OwnedBy: safeText(upstream.OwnedBy),
			Title: safeModelMetadataText(upstream.Title), Description: safeModelMetadataText(upstream.Description),
			Created: safeWhole(upstream.Created), ContextWindow: safeWhole(upstream.ContextWindow),
			MaxTokens: safeWhole(upstream.MaxTokens), InputTokenPricePerM: safeNonNegative(upstream.InputTokenPricePerM),
			OutputTokenPricePerM: safeNonNegative(upstream.OutputTokenPricePerM),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func safeText(value string) string {
	if !utf8.ValidString(value) || strings.IndexFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return ""
	}
	return value
}

func safeModelMetadataText(value string) string {
	if len(value) > maxModelMetadataTextBytes {
		return ""
	}
	return safeText(value)
}

func safeWhole(value float64) int64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > math.MaxInt64 || math.Trunc(value) != value {
		return 0
	}
	return int64(value)
}

func safeNonNegative(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return value
}

// validModelID accepts opaque identifiers while excluding syntax that can
// change an HTTP path or be confused with whitespace/control data.
func validModelID(id string) bool {
	if id == "" || id != strings.TrimSpace(id) || !utf8.ValidString(id) {
		return false
	}
	return strings.IndexFunc(id, func(r rune) bool {
		return r < 0x20 || r == 0x7f || strings.ContainsRune("/\\?#%", r)
	}) < 0
}

func cloneModels(in []Model) []Model { return append([]Model(nil), in...) }
