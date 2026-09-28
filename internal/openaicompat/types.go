package openaicompat

import "encoding/json"

const (
	MaxRequestBodyBytes = 12 * 1024 * 1024
	// MaxMessages bounds one request's message array. It must comfortably fit a
	// long agent conversation: a coding agent commonly replays 100-500 message
	// and tool-result entries. The 12 MiB body cap remains the hard bound.
	MaxMessages         = 1024
	MaxTextContentBytes = 1 * 1024 * 1024
	MaxTools            = 32
	MaxParallelCalls    = 64
	MaxArgumentsBytes   = 256 * 1024
	MaxToolOutputBytes  = 1 * 1024 * 1024
	MaxCallIDBytes      = 128
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Conversation struct {
	Model                 string
	Instructions          []Message
	Messages              []Message
	Tools                 []ToolDefinition
	ToolChoice            ToolChoice
	ParallelToolCalls     *bool
	MaxOutputTokens       *int64
	Temperature           *float64
	TopP                  *float64
	FrequencyPenalty      *float64
	PresencePenalty       *float64
	Stop                  json.RawMessage
	Seed                  *int64
	N                     *int64
	ResponseFormat        json.RawMessage
	IncludeUsage          bool
	Stream                bool
	ReasoningEffort       string
	Thinking              *ThinkingMode
	ThinkingClearThinking *bool
}

type Message struct {
	Role             Role
	Content          any
	ToolCalls        []ToolCall
	ToolCallID       string
	ReasoningContent *string
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Strict      *bool
}

type ToolChoice struct {
	Mode string
	Name string
}

// Error is an internal classification. Detail is a content-free diagnostic
// reason (field name or size limit, never a request value); it is logged
// server-side and is never part of the public error response.
type Error struct {
	Code   string
	Status int
	Detail string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func InvalidRequest() *Error       { return &Error{Code: "invalid_request", Status: 400} }
func UnsupportedParameter() *Error { return &Error{Code: "unsupported_parameter", Status: 400} }
func RequestTooLarge() *Error      { return &Error{Code: "request_too_large", Status: 413} }

func InvalidRequestDetail(detail string) *Error {
	return &Error{Code: "invalid_request", Status: 400, Detail: detail}
}

func UnsupportedParameterDetail(detail string) *Error {
	return &Error{Code: "unsupported_parameter", Status: 400, Detail: detail}
}

func RequestTooLargeDetail(detail string) *Error {
	return &Error{Code: "request_too_large", Status: 413, Detail: detail}
}
