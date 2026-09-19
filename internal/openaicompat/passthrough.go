package openaicompat

import (
	"encoding/json"
	"sort"
	"strings"
)

// passthroughDeniedFields is the fixed, default-deny strip list applied to a
// raw passthrough body before it may reach the upstream. It mirrors the
// new-api RemoveDisabledFields pattern: fields that could leak operator or
// user identity, change billing, or open provider-specific behaviour are
// removed unless the operator explicitly opts the model into passthrough.
var passthroughDeniedFields = map[string]struct{}{
	"user": {}, "metadata": {}, "safety_identifier": {}, "service_tier": {},
	"inference_geo": {}, "speed": {}, "store": {}, "previous_response_id": {},
	"prompt_cache_key": {}, "logit_bias": {}, "logprobs": {}, "top_logprobs": {},
	"api_key": {}, "authorization": {}, "secret": {}, "password": {},
	"credentials": {}, "provider": {},
}

// PassthroughReport is the content-free metadata produced while sanitizing a
// raw passthrough body. It never contains request text or values.
type PassthroughReport struct {
	StrippedFields []string
	MessageCount   int
	ToolCount      int
}

// ExtractChatRouting reads only the routing fields (model, stream) from a Chat
// Completions body. Unlike DecodeChat it tolerates fields the structured
// contract does not model, so an explicitly enabled passthrough model can
// still be authorized before the body is sanitized.
func ExtractChatRouting(body []byte) (string, bool, *Error) {
	if len(body) > MaxRequestBodyBytes {
		return "", false, RequestTooLarge()
	}
	var routing struct {
		Model  string `json:"model"`
		Stream *bool  `json:"stream"`
	}
	if err := json.Unmarshal(body, &routing); err != nil {
		return "", false, InvalidRequest()
	}
	if strings.TrimSpace(routing.Model) == "" {
		return "", false, InvalidRequest()
	}
	return routing.Model, routing.Stream != nil && *routing.Stream, nil
}

// SanitizePassthrough validates the minimal structure of a raw Chat
// Completions body, removes the default-deny field list, and returns the
// re-encoded body with a content-free report. It only deletes fields; it never
// adds or rewrites a value.
func SanitizePassthrough(body []byte, model string) ([]byte, PassthroughReport, *Error) {
	report := PassthroughReport{}
	if len(body) > MaxRequestBodyBytes {
		return nil, report, RequestTooLarge()
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, report, InvalidRequest()
	}
	rawModel, ok := fields["model"]
	if !ok {
		return nil, report, InvalidRequest()
	}
	var requested string
	if json.Unmarshal(rawModel, &requested) != nil || requested != model {
		return nil, report, InvalidRequest()
	}
	rawMessages, ok := fields["messages"]
	if !ok {
		return nil, report, InvalidRequest()
	}
	var messages []json.RawMessage
	if json.Unmarshal(rawMessages, &messages) != nil || len(messages) == 0 || len(messages) > MaxMessages {
		return nil, report, InvalidRequest()
	}
	report.MessageCount = len(messages)
	if rawTools, ok := fields["tools"]; ok {
		var tools []json.RawMessage
		if json.Unmarshal(rawTools, &tools) != nil || len(tools) > MaxTools {
			return nil, report, InvalidRequest()
		}
		report.ToolCount = len(tools)
	}

	stripped := make([]string, 0, len(passthroughDeniedFields))
	for field := range fields {
		if _, denied := passthroughDeniedFields[field]; denied {
			delete(fields, field)
			stripped = append(stripped, field)
		}
	}
	if rawStreamOptions, ok := fields["stream_options"]; ok {
		var options map[string]json.RawMessage
		if json.Unmarshal(rawStreamOptions, &options) == nil && options != nil {
			if _, denied := options["include_obfuscation"]; denied {
				delete(options, "include_obfuscation")
				stripped = append(stripped, "stream_options.include_obfuscation")
			}
			if len(options) == 0 {
				delete(fields, "stream_options")
			} else {
				encoded, err := json.Marshal(options)
				if err != nil {
					return nil, report, InvalidRequest()
				}
				fields["stream_options"] = encoded
			}
		}
	}
	sort.Strings(stripped)
	report.StrippedFields = stripped
	sanitized, err := json.Marshal(fields)
	if err != nil {
		return nil, report, InvalidRequest()
	}
	return sanitized, report, nil
}
