package service

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	ErrPlatformSSEV2MetaRequired  = errors.New("platform sse v2 meta required")
	ErrPlatformSSEV2Sequence      = errors.New("platform sse v2 invalid sequence")
	ErrPlatformSSEV2InvalidEvent  = errors.New("platform sse v2 invalid event")
	ErrPlatformSSEV2ModelsRunning = errors.New("platform sse v2 models still running")
	ErrPlatformSSEV2Terminal      = errors.New("platform sse v2 terminal")
)

const platformSSEV2Schema = "platform-chat-sse.v2"

const (
	platformSSEV2MaxModels                     = 3
	platformSSEV2MaxIdentifierBytes            = 255
	platformSSEV2MaxModelIdentifierBytes       = 128
	platformSSEV2MaxDeltaBytes                 = 1 << 20
	platformSSEV2MaxFrameBytes                 = 2 << 20
	platformSSEV2MaxSafeInteger          int64 = 9007199254740991
)

var platformSSEV2StableCodes = map[string]struct{}{
	"gateway_upstream_error": {},
	"invalid_request":        {},
	"rate_limited":           {},
	"cancelled":              {},
	"timeout":                {},
	"internal_error":         {},
	"upstream_error":         {},
}

type platformSSEV2ModelState struct {
	nextSeq  int64
	terminal bool
	failed   bool
	code     string
}

// PlatformSSEV2Encoder owns only protocol framing and ordering. It has no
// database, Redis, HTTP, or upstream dependencies, so a future lifecycle can
// write each returned frame then immediately Flush it.
type PlatformSSEV2Encoder struct {
	mu           sync.Mutex
	generationID string
	models       []string
	states       map[string]*platformSSEV2ModelState
	metaSent     bool
	terminal     bool
	err          error
}

func NewPlatformSSEV2Encoder(generationID string, models []string) (*PlatformSSEV2Encoder, error) {
	if !platformSSEV2CanonicalUUID(generationID) || len(models) == 0 || len(models) > platformSSEV2MaxModels {
		return nil, ErrPlatformSSEV2InvalidEvent
	}
	states := make(map[string]*platformSSEV2ModelState, len(models))
	copyModels := make([]string, len(models))
	for index, model := range models {
		if !platformSSEV2ModelIdentifier(model) {
			return nil, ErrPlatformSSEV2InvalidEvent
		}
		if _, duplicate := states[model]; duplicate {
			return nil, ErrPlatformSSEV2InvalidEvent
		}
		copyModels[index] = model
		states[model] = &platformSSEV2ModelState{nextSeq: 1}
	}
	return &PlatformSSEV2Encoder{generationID: generationID, models: copyModels, states: states}, nil
}

func (e *PlatformSSEV2Encoder) Err() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.err
}

func (e *PlatformSSEV2Encoder) Meta(conversationGUID string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !platformSSEV2Identifier(conversationGUID) || e.metaSent {
		return e.fail(ErrPlatformSSEV2InvalidEvent)
	}
	e.metaSent = true
	return e.frame("meta", struct {
		Schema           string   `json:"schema"`
		GenerationID     string   `json:"generation_id"`
		ConversationGUID string   `json:"conversation_guid"`
		Models           []string `json:"models"`
	}{platformSSEV2Schema, e.generationID, conversationGUID, e.models})
}

func (e *PlatformSSEV2Encoder) Delta(model string, seq int64, delta string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.readyModel(model)
	if state == nil {
		return nil
	}
	if delta == "" || len(delta) > platformSSEV2MaxDeltaBytes || !platformSSEV2SafeInteger(seq) {
		return e.fail(ErrPlatformSSEV2InvalidEvent)
	}
	if seq != state.nextSeq {
		return e.fail(ErrPlatformSSEV2Sequence)
	}
	state.nextSeq++
	return e.frame("delta", struct {
		GenerationID string `json:"generation_id"`
		Model        string `json:"model"`
		Seq          int64  `json:"seq"`
		Delta        string `json:"delta"`
	}{e.generationID, model, seq, delta})
}

func (e *PlatformSSEV2Encoder) ModelDone(model string, lastSeq int64) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.readyModel(model)
	if state == nil {
		return nil
	}
	if !platformSSEV2SafeInteger(lastSeq) || lastSeq != state.nextSeq-1 {
		return e.fail(ErrPlatformSSEV2Sequence)
	}
	state.terminal = true
	return e.frame("model_done", struct {
		GenerationID string `json:"generation_id"`
		Model        string `json:"model"`
		LastSeq      int64  `json:"last_seq"`
	}{e.generationID, model, lastSeq})
}

func (e *PlatformSSEV2Encoder) ModelError(model, code, requestID string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	state := e.readyModel(model)
	if state == nil {
		return nil
	}
	state.terminal = true
	state.failed = true
	state.code = platformSSEV2Code(code)
	payload := struct {
		GenerationID string `json:"generation_id"`
		Model        string `json:"model"`
		Code         string `json:"code"`
		RequestID    string `json:"request_id,omitempty"`
	}{e.generationID, model, state.code, platformSSEV2RequestID(requestID)}
	return e.frame("model_error", payload)
}

func (e *PlatformSSEV2Encoder) DoneSingle(conversationGUID string, tokens, totalTokensUsed int64) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil || e.terminal {
		return e.fail(ErrPlatformSSEV2Terminal)
	}
	if !platformSSEV2SafeInteger(tokens) || !platformSSEV2SafeInteger(totalTokensUsed) {
		return e.fail(ErrPlatformSSEV2InvalidEvent)
	}
	if len(e.models) != 1 || !e.canComplete(conversationGUID) || e.states[e.models[0]].failed {
		return e.fail(ErrPlatformSSEV2ModelsRunning)
	}
	e.terminal = true
	return e.frame("done", struct {
		GenerationID     string `json:"generation_id"`
		Status           string `json:"status"`
		ConversationGUID string `json:"conversation_guid"`
		Tokens           int64  `json:"tokens"`
		TotalTokensUsed  int64  `json:"total_tokens_used"`
	}{e.generationID, "completed", conversationGUID, tokens, totalTokensUsed})
}

func (e *PlatformSSEV2Encoder) DoneCompare(conversationGUID string, totalTokensUsed int64, modelTokens map[string]int64) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil || e.terminal {
		return e.fail(ErrPlatformSSEV2Terminal)
	}
	if !platformSSEV2SafeInteger(totalTokensUsed) {
		return e.fail(ErrPlatformSSEV2InvalidEvent)
	}
	if len(e.models) < 2 || !e.canComplete(conversationGUID) {
		return e.fail(ErrPlatformSSEV2ModelsRunning)
	}
	for model, tokens := range modelTokens {
		state, found := e.states[model]
		if !found || state.failed || !platformSSEV2SafeInteger(tokens) {
			return e.fail(ErrPlatformSSEV2InvalidEvent)
		}
	}
	models := make(map[string]any, len(e.models))
	for _, model := range e.models {
		state := e.states[model]
		if state.failed {
			models[model] = struct {
				Status string `json:"status"`
				Code   string `json:"code"`
			}{"failed", state.code}
			continue
		}
		tokens, found := modelTokens[model]
		if !found || !platformSSEV2SafeInteger(tokens) {
			return e.fail(ErrPlatformSSEV2InvalidEvent)
		}
		models[model] = struct {
			Status string `json:"status"`
			Tokens int64  `json:"tokens"`
		}{"completed", tokens}
	}
	e.terminal = true
	return e.frame("done", struct {
		GenerationID     string         `json:"generation_id"`
		Status           string         `json:"status"`
		ConversationGUID string         `json:"conversation_guid"`
		TotalTokensUsed  int64          `json:"total_tokens_used"`
		Models           map[string]any `json:"models"`
	}{e.generationID, "completed", conversationGUID, totalTokensUsed, models})
}

func (e *PlatformSSEV2Encoder) Error(code, requestID string) []byte {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.err != nil || e.terminal || !e.metaSent {
		return e.fail(ErrPlatformSSEV2Terminal)
	}
	e.terminal = true
	return e.frame("error", struct {
		GenerationID string `json:"generation_id"`
		Code         string `json:"code"`
		RequestID    string `json:"request_id,omitempty"`
	}{e.generationID, platformSSEV2Code(code), platformSSEV2RequestID(requestID)})
}

func (e *PlatformSSEV2Encoder) readyModel(model string) *platformSSEV2ModelState {
	if e.err != nil || e.terminal {
		e.fail(ErrPlatformSSEV2Terminal)
		return nil
	}
	if !e.metaSent {
		e.fail(ErrPlatformSSEV2MetaRequired)
		return nil
	}
	state, found := e.states[model]
	if !found || state.terminal {
		e.fail(ErrPlatformSSEV2InvalidEvent)
		return nil
	}
	return state
}

func (e *PlatformSSEV2Encoder) canComplete(conversationGUID string) bool {
	if e.err != nil || e.terminal || !e.metaSent || !platformSSEV2Identifier(conversationGUID) {
		return false
	}
	for _, model := range e.models {
		if !e.states[model].terminal {
			return false
		}
	}
	return true
}

func (e *PlatformSSEV2Encoder) fail(err error) []byte {
	if e.err == nil {
		e.err = err
	}
	e.terminal = true
	return nil
}

func (e *PlatformSSEV2Encoder) frame(event string, payload any) []byte {
	if e.err != nil {
		return nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return e.fail(ErrPlatformSSEV2InvalidEvent)
	}
	frame := []byte("event: " + event + "\ndata: " + string(data) + "\n\n")
	if len(frame) > platformSSEV2MaxFrameBytes {
		return e.fail(ErrPlatformSSEV2InvalidEvent)
	}
	return frame
}

func platformSSEV2CanonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i := 0; i < len(value); i++ {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
			continue
		}
		if !((value[i] >= '0' && value[i] <= '9') || (value[i] >= 'a' && value[i] <= 'f')) {
			return false
		}
	}
	return true
}

func platformSSEV2Identifier(value string) bool {
	return value != "" && utf8.ValidString(value) && value == strings.TrimSpace(value) && len(value) <= platformSSEV2MaxIdentifierBytes
}

func platformSSEV2ModelIdentifier(value string) bool {
	return platformSSEV2Identifier(value) && len([]byte(value)) <= platformSSEV2MaxModelIdentifierBytes
}

func platformSSEV2SafeInteger(value int64) bool {
	return value >= 0 && value <= platformSSEV2MaxSafeInteger
}

func platformSSEV2RequestID(value string) string {
	if len(value) == 0 || len(value) > 128 {
		return ""
	}
	for i := 0; i < len(value); i++ {
		char := value[i]
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-') {
			return ""
		}
	}
	return value
}

func platformSSEV2Code(code string) string {
	if _, allowed := platformSSEV2StableCodes[code]; allowed {
		return code
	}
	return "upstream_error"
}

func SetPlatformSSEV2Headers(headers http.Header) {
	headers.Set("Content-Type", "text/event-stream; charset=utf-8")
	headers.Set("Cache-Control", "no-cache, no-transform")
	headers.Set("X-Accel-Buffering", "no")
}
