package whitelabel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
)

const (
	MaxRequestBodyBytes = 12 * 1024 * 1024
	MaxMessages         = 128
	MaxTextContentBytes = 1 * 1024 * 1024
	MaxDataImageBytes   = 4 * 1024 * 1024
	MaxTools            = 32
)

type ValidationMode int

const (
	GatewayValidation ValidationMode = iota
	PlatformValidation
)

type chatRequest struct {
	Model            string          `json:"model"`
	Messages         []chatMessage   `json:"messages"`
	MaxTokens        *json.Number    `json:"max_tokens"`
	N                *json.Number    `json:"n"`
	Temperature      *json.Number    `json:"temperature"`
	TopP             *json.Number    `json:"top_p"`
	FrequencyPenalty *json.Number    `json:"frequency_penalty"`
	PresencePenalty  *json.Number    `json:"presence_penalty"`
	Stop             json.RawMessage `json:"stop"`
	Tools            []tool          `json:"tools"`
	ResponseFormat   *responseFormat `json:"response_format"`
	StreamOptions    *streamOptions  `json:"stream_options"`
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     *string   `json:"text"`
	ImageURL *imageURL `json:"image_url"`
}

type imageURL struct {
	URL string `json:"url"`
}

type tool struct {
	Type     string              `json:"type"`
	Function *functionDefinition `json:"function"`
}

type functionDefinition struct {
	Name        string          `json:"name"`
	Description *string         `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responseFormat struct {
	Type string `json:"type"`
}

type streamOptions struct {
	IncludeUsage *bool `json:"include_usage"`
}

// ValidateRequest checks the OpenAI-compatible subset before any upstream
// request is made. The parser is deliberately closed to unknown fields.
func ValidateRequest(body []byte, mode ValidationMode) *Error {
	if len(body) > MaxRequestBodyBytes {
		return &Error{Code: CodeRequestTooLarge, Status: 413, Type: TypeInvalidRequest}
	}
	var request chatRequest
	if err := decodeStrict(body, &request); err != nil {
		return invalidRequest(CodeInvalidRequest)
	}
	if strings.TrimSpace(request.Model) == "" || request.Messages == nil || len(request.Messages) > MaxMessages {
		return invalidRequest(CodeInvalidRequest)
	}
	if request.MaxTokens == nil {
		return invalidRequest(CodeMissingMaxTokens)
	}
	if !validInteger(*request.MaxTokens, 1, 16384) || !validOptionalInteger(request.N, 1, 128) {
		return invalidRequest(CodeInvalidRequest)
	}
	if mode == PlatformValidation && request.N != nil && *request.N != "1" {
		return invalidRequest(CodeInvalidRequest)
	}
	if !validOptionalFloat(request.Temperature, 0, 1) || !validOptionalFloat(request.TopP, 0, 1) ||
		!validOptionalFloat(request.FrequencyPenalty, -2, 2) || !validOptionalFloat(request.PresencePenalty, -2, 2) {
		return invalidRequest(CodeInvalidRequest)
	}
	if err := validateStop(request.Stop); err != nil || len(request.Tools) > MaxTools ||
		!validateTools(request.Tools) || !validateResponseFormat(request.ResponseFormat) || !validateStreamOptions(request.StreamOptions) {
		return invalidRequest(CodeInvalidRequest)
	}
	for _, message := range request.Messages {
		if !validRole(message.Role) || !validateContent(message.Content) {
			return invalidRequest(CodeInvalidRequest)
		}
	}
	return nil
}

func decodeStrict(raw []byte, dest any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(dest); err != nil {
		return err
	}
	return ensureEOF(decoder)
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errMultipleJSONValues
		}
		return err
	}
	return nil
}

var errMultipleJSONValues = &multipleJSONValuesError{}

type multipleJSONValuesError struct{}

func (*multipleJSONValuesError) Error() string { return "multiple JSON values" }

func validInteger(value json.Number, min, max int64) bool {
	n, err := value.Int64()
	return err == nil && n >= min && n <= max
}

func validOptionalInteger(value *json.Number, min, max int64) bool {
	return value == nil || validInteger(*value, min, max)
}

func validOptionalFloat(value *json.Number, min, max float64) bool {
	if value == nil {
		return true
	}
	n, err := value.Float64()
	return err == nil && !math.IsNaN(n) && !math.IsInf(n, 0) && n >= min && n <= max
}

func validRole(role string) bool {
	switch role {
	case "system", "user", "assistant", "tool":
		return true
	default:
		return false
	}
}

func validateContent(raw json.RawMessage) bool {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return len(text) <= MaxTextContentBytes
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		return false
	}
	for _, rawPart := range parts {
		var part contentPart
		if err := decodeStrict(rawPart, &part); err != nil {
			return false
		}
		switch part.Type {
		case "text":
			if part.Text == nil || part.ImageURL != nil || len(*part.Text) > MaxTextContentBytes {
				return false
			}
		case "image_url":
			if part.Text != nil || part.ImageURL == nil || validateImageSource(part.ImageURL.URL) != nil {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validateStop(raw json.RawMessage) error {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil || len(many) > 4 {
		return errMultipleJSONValues
	}
	return nil
}

func validateTools(tools []tool) bool {
	for _, tool := range tools {
		if tool.Type != "function" || tool.Function == nil || !validFunctionName(tool.Function.Name) {
			return false
		}
		if tool.Function.Description != nil && len(*tool.Function.Description) > MaxTextContentBytes {
			return false
		}
		if len(tool.Function.Parameters) != 0 {
			var parameters map[string]json.RawMessage
			if err := decodeStrict(tool.Function.Parameters, &parameters); err != nil || parameters == nil {
				return false
			}
		}
	}
	return true
}

var functionName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func validFunctionName(name string) bool { return functionName.MatchString(name) }

func validateResponseFormat(format *responseFormat) bool {
	return format == nil || format.Type == "text" || format.Type == "json_object"
}

func validateStreamOptions(options *streamOptions) bool {
	return options == nil || options.IncludeUsage != nil
}

func validateImageSource(raw string) *Error {
	if strings.HasPrefix(strings.ToLower(raw), "data:") {
		return ValidateDataImage(raw)
	}
	return ValidateMediaURL(raw)
}

// ValidateDataImage permits only small raster data images. SVG is excluded
// because it is active content in common renderers.
func ValidateDataImage(raw string) *Error {
	const marker = ";base64,"
	if !strings.HasPrefix(strings.ToLower(raw), "data:") {
		return invalidRequest(CodeInvalidRequest)
	}
	separator := strings.Index(raw, marker)
	if separator < 0 {
		return invalidRequest(CodeInvalidRequest)
	}
	mime := strings.ToLower(raw[len("data:"):separator])
	switch mime {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
	default:
		return invalidRequest(CodeInvalidRequest)
	}
	encoded := raw[separator+len(marker):]
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(MaxDataImageBytes)+4 {
		return invalidRequest(CodeInvalidRequest)
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(decoded) > MaxDataImageBytes {
		return invalidRequest(CodeInvalidRequest)
	}
	return nil
}

// ValidateMediaURL validates syntactically only. It never resolves DNS, so no
// validation request can cause a network lookup.
func ValidateMediaURL(raw string) *Error {
	if len(raw) == 0 || len(raw) > 8192 || strings.ContainsAny(raw, "\r\n") {
		return invalidRequest(CodeInvalidRequest)
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Hostname() == "" {
		return invalidRequest(CodeInvalidRequest)
	}
	port := u.Port()
	if port != "" && port != "443" {
		return invalidRequest(CodeInvalidRequest)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return invalidRequest(CodeInvalidRequest)
	}
	if address, err := netip.ParseAddr(host); err == nil {
		if !publicAddress(address) {
			return invalidRequest(CodeInvalidRequest)
		}
	} else if numericAddressLike(host) || !validHostname(host) {
		return invalidRequest(CodeInvalidRequest)
	}
	return nil
}

func publicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"), netip.MustParsePrefix("ff00::/8"), netip.MustParsePrefix("2001:db8::/32"),
}

func numericAddressLike(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return false
		}
		for _, r := range label {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func validHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
}
