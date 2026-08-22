package whitelabel

import (
	"bytes"
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

type ChatCompletionChoice struct {
	Index        int             `json:"index"`
	Message      json.RawMessage `json:"message,omitempty"`
	FinishReason *string         `json:"finish_reason"`
	Logprobs     json.RawMessage `json:"logprobs,omitempty"`
}

type ChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ProjectChatCompletion validates the minimal OpenAI completion shape and
// drops all upstream-owned fields that are not part of the public contract.
func (s *WhiteLabelService) ProjectChatCompletion(data []byte, logicalModelID string) (ChatCompletion, *Error) {
	var upstream struct {
		ID      string                 `json:"id"`
		Object  string                 `json:"object"`
		Created int64                  `json:"created"`
		Choices []ChatCompletionChoice `json:"choices"`
		Usage   *ChatCompletionUsage   `json:"usage"`
	}
	if !validModelID(logicalModelID) || json.Unmarshal(data, &upstream) != nil || upstream.ID == "" || upstream.Object != "chat.completion" || upstream.Created < 0 || len(upstream.Choices) == 0 || !validCompletionChoices(upstream.Choices) || !validCompletionUsage(upstream.Usage) {
		return ChatCompletion{}, ErrUpstreamUnavailable("malformed chat completion")
	}
	return ChatCompletion{ID: upstream.ID, Object: upstream.Object, Created: upstream.Created, Model: logicalModelID, Choices: upstream.Choices, Usage: upstream.Usage}, nil
}

func validCompletionChoices(choices []ChatCompletionChoice) bool {
	for _, choice := range choices {
		if choice.Index < 0 || !validJSONObject(choice.Message) || !validNullableJSONObject(choice.Logprobs) {
			return false
		}
	}
	return true
}

func validCompletionUsage(usage *ChatCompletionUsage) bool {
	return usage == nil || (usage.PromptTokens >= 0 && usage.CompletionTokens >= 0 && usage.TotalTokens >= 0)
}

func validJSONObject(value json.RawMessage) bool {
	return len(value) > 0 && bytes.HasPrefix(bytes.TrimSpace(value), []byte("{")) && json.Valid(value)
}

func validNullableJSONObject(value json.RawMessage) bool {
	return len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || validJSONObject(value)
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
