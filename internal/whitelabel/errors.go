package whitelabel

import "fmt"

type Code string

const (
	CodeInvalidRequest             Code = "invalid_request_error"
	CodeMissingMaxTokens           Code = "missing_max_tokens"
	CodeRequestTooLarge            Code = "request_too_large"
	CodeGatewayUpstreamUnavailable Code = "gateway_upstream_unavailable"
)

type ErrorType string

const (
	TypeInvalidRequest ErrorType = "invalid_request_error"
	TypeAPI            ErrorType = "api_error"
)

// Error is an internal classification. Detail is intentionally never included
// in a PublicErrorResponse.
type Error struct {
	Code   Code
	Status int
	Type   ErrorType
	Detail string
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s", e.Code)
}

func invalidRequest(code Code) *Error {
	return &Error{Code: code, Status: 400, Type: TypeInvalidRequest}
}

func ErrUpstreamUnavailable(detail string) *Error {
	return &Error{Code: CodeGatewayUpstreamUnavailable, Status: 503, Type: TypeAPI, Detail: detail}
}

type PublicAPIError struct {
	Message string    `json:"message"`
	Type    ErrorType `json:"type"`
	Code    Code      `json:"code"`
}

type PublicErrorResponse struct {
	Status    int            `json:"-"`
	Error     PublicAPIError `json:"error"`
	RequestID string         `json:"request_id"`
}

// PublicError emits only fixed, client-safe values. It deliberately discards
// internal Error.Detail, including any upstream body, hostname, or request ID.
func PublicError(err *Error, requestID string) PublicErrorResponse {
	if err == nil {
		err = &Error{Code: CodeInvalidRequest, Status: 400, Type: TypeInvalidRequest}
	}

	message := "Invalid request."
	switch err.Code {
	case CodeMissingMaxTokens:
		message = "max_tokens is required."
	case CodeRequestTooLarge:
		message = "Request body is too large."
	case CodeGatewayUpstreamUnavailable:
		message = "The upstream service is unavailable."
	}
	status := err.Status
	if status == 0 {
		status = 400
	}
	errorType := err.Type
	if errorType == "" {
		errorType = TypeInvalidRequest
	}
	return PublicErrorResponse{
		Status:    status,
		Error:     PublicAPIError{Message: message, Type: errorType, Code: err.Code},
		RequestID: requestID,
	}
}
