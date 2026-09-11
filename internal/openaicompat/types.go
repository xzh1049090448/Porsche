package openaicompat

import "encoding/json"

const (
	MaxRequestBodyBytes = 12 * 1024 * 1024
	MaxMessages         = 128
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
	Model             string
	Instructions      []Message
	Messages          []Message
	Tools             []ToolDefinition
	ToolChoice        ToolChoice
	ParallelToolCalls *bool
	MaxOutputTokens   *int64
	Temperature       *float64
	TopP              *float64
	FrequencyPenalty  *float64
	PresencePenalty   *float64
	Stop              json.RawMessage
	Seed              *int64
	N                 *int64
	ResponseFormat    json.RawMessage
	IncludeUsage      bool
	Stream            bool
}

type Message struct {
	Role       Role
	Content    any
	ToolCalls  []ToolCall
	ToolCallID string
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

type Error struct {
	Code   string
	Status int
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
