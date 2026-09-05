package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const UserDeleteBodyLimit int64 = 4 * 1024

var (
	ErrUserDeleteInvalidBody  = errors.New("invalid user delete request")
	ErrUserDeleteBodyTooLarge = errors.New("user delete request body too large")
)

type IssueUserDeleteRequest struct {
	Action          string `json:"action"`
	TargetGUID      int64  `json:"target_guid"`
	ExpectedVersion int    `json:"expected_auth_version"`
	Reason          string `json:"reason"`
	Password        []byte `json:"-"`
}

func (request IssueUserDeleteRequest) String() string {
	return request.redactedString()
}

func (request IssueUserDeleteRequest) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.redactedString())
}

func (request IssueUserDeleteRequest) redactedString() string {
	return fmt.Sprintf("IssueUserDeleteRequest{Action:%q TargetGUID:%d ExpectedVersion:%d Reason:%q Password:<redacted>}", request.Action, request.TargetGUID, request.ExpectedVersion, request.Reason)
}

type ExecuteUserDeleteRequest struct {
	ExpectedVersion int    `json:"expected_auth_version"`
	Reason          string `json:"reason"`
}

type UserDeleteIssueResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresAt int64  `json:"expires_at"`
}

type UserDeleteResponseUser struct {
	GUID   string `json:"guid"`
	Status string `json:"status"`
}

type DeleteUserResponse struct {
	OperationRef string                 `json:"operation_ref"`
	User         UserDeleteResponseUser `json:"user"`
}

type UserDeleteQueryResponse struct {
	OperationRef string  `json:"operation_ref"`
	Scope        string  `json:"scope"`
	Status       string  `json:"status"`
	FinishedAt   *int64  `json:"finished_at"`
	FailureCode  *string `json:"failure_code"`
}

type userDeleteIssueWire struct {
	Action   json.RawMessage
	Intent   json.RawMessage
	Password json.RawMessage
}

type userDeleteIntentWire struct {
	TargetGUID      json.RawMessage
	ExpectedVersion json.RawMessage
	Reason          json.RawMessage
}

type userDeleteExecuteWire struct {
	Action          json.RawMessage
	ExpectedVersion json.RawMessage
	Reason          json.RawMessage
}

func DecodeUserDeleteIssue(body io.Reader) (IssueUserDeleteRequest, error) {
	raw, err := readUserDeleteBody(body)
	if err != nil {
		return IssueUserDeleteRequest{}, err
	}
	defer clear(raw)
	if err := validateUserDeleteJSON(raw); err != nil {
		return IssueUserDeleteRequest{}, err
	}
	top, err := decodeExactUserDeleteObject(raw, "action", "intent", "current_password")
	if err != nil {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	defer clearUserDeleteRawMap(top)
	intentFields, err := decodeExactUserDeleteObject(top["intent"], "target_guid", "expected_auth_version", "reason")
	if err != nil {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	defer clearUserDeleteRawMap(intentFields)
	wire := userDeleteIssueWire{Action: top["action"], Intent: top["intent"], Password: top["current_password"]}
	intent := userDeleteIntentWire{TargetGUID: intentFields["target_guid"], ExpectedVersion: intentFields["expected_auth_version"], Reason: intentFields["reason"]}
	action, ok := decodeUserDeleteString(wire.Action)
	if !ok || action != "users.delete" {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	targetRaw, ok := decodeUserDeleteString(intent.TargetGUID)
	if !ok {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	targetGUID, ok := parseCanonicalPositiveInt64(targetRaw)
	if !ok {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	version, ok := parseUserDeleteVersion(intent.ExpectedVersion)
	if !ok {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	reasonRaw, ok := decodeUserDeleteString(intent.Reason)
	if !ok {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	reason, ok := normalizeUserDeleteReason(reasonRaw)
	if !ok {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	password, ok := decodeOwnedUserDeleteJSONString(wire.Password)
	if !ok || len(password) == 0 {
		clear(password)
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	return IssueUserDeleteRequest{Action: action, TargetGUID: targetGUID, ExpectedVersion: version, Reason: reason, Password: password}, nil
}

func DecodeUserDeleteExecute(body io.Reader) (ExecuteUserDeleteRequest, error) {
	raw, err := readUserDeleteBody(body)
	if err != nil {
		return ExecuteUserDeleteRequest{}, err
	}
	defer clear(raw)
	if err := validateUserDeleteJSON(raw); err != nil {
		return ExecuteUserDeleteRequest{}, err
	}
	top, err := decodeExactUserDeleteObject(raw, "action", "expected_auth_version", "reason")
	if err != nil {
		return ExecuteUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	defer clearUserDeleteRawMap(top)
	wire := userDeleteExecuteWire{Action: top["action"], ExpectedVersion: top["expected_auth_version"], Reason: top["reason"]}
	action, ok := decodeUserDeleteString(wire.Action)
	if !ok || action != "delete" {
		return ExecuteUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	version, ok := parseUserDeleteVersion(wire.ExpectedVersion)
	if !ok {
		return ExecuteUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	reasonRaw, ok := decodeUserDeleteString(wire.Reason)
	if !ok {
		return ExecuteUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	reason, ok := normalizeUserDeleteReason(reasonRaw)
	if !ok {
		return ExecuteUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	return ExecuteUserDeleteRequest{ExpectedVersion: version, Reason: reason}, nil
}

func readUserDeleteBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, ErrUserDeleteInvalidBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, UserDeleteBodyLimit+1))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			clear(raw)
			return nil, ErrUserDeleteBodyTooLarge
		}
		clear(raw)
		return nil, ErrUserDeleteInvalidBody
	}
	if int64(len(raw)) > UserDeleteBodyLimit {
		clear(raw)
		return nil, ErrUserDeleteBodyTooLarge
	}
	return raw, nil
}

func decodeExactUserDeleteObject(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || len(fields) != len(keys) {
		clearUserDeleteRawMap(fields)
		return nil, ErrUserDeleteInvalidBody
	}
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
		if _, ok := fields[key]; !ok {
			clearUserDeleteRawMap(fields)
			return nil, ErrUserDeleteInvalidBody
		}
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			clearUserDeleteRawMap(fields)
			return nil, ErrUserDeleteInvalidBody
		}
	}
	return fields, nil
}

func clearUserDeleteRawMap(fields map[string]json.RawMessage) {
	for key, value := range fields {
		clear(value)
		delete(fields, key)
	}
}

func decodeUserDeleteString(raw json.RawMessage) (string, bool) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func decodeOwnedUserDeleteJSONString(raw json.RawMessage) ([]byte, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' || !utf8.Valid(raw) {
		return nil, false
	}
	decoded := make([]byte, 0, len(raw)-2)
	for i := 1; i < len(raw)-1; i++ {
		value := raw[i]
		switch {
		case value == '"' || value < 0x20:
			clear(decoded)
			return nil, false
		case value == '\\':
			i++
			if i >= len(raw)-1 {
				clear(decoded)
				return nil, false
			}
			switch raw[i] {
			case '"', '\\', '/':
				decoded = append(decoded, raw[i])
			case 'b':
				decoded = append(decoded, '\b')
			case 'f':
				decoded = append(decoded, '\f')
			case 'n':
				decoded = append(decoded, '\n')
			case 'r':
				decoded = append(decoded, '\r')
			case 't':
				decoded = append(decoded, '\t')
			case 'u':
				codePoint, ok := parseUserDeleteHex4(raw, i+1)
				if !ok {
					clear(decoded)
					return nil, false
				}
				i += 4
				r := rune(codePoint)
				if codePoint >= 0xd800 && codePoint <= 0xdbff {
					if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
						clear(decoded)
						return nil, false
					}
					low, ok := parseUserDeleteHex4(raw, i+3)
					if !ok || low < 0xdc00 || low > 0xdfff {
						clear(decoded)
						return nil, false
					}
					r = utf16.DecodeRune(r, rune(low))
					i += 6
				} else if codePoint >= 0xdc00 && codePoint <= 0xdfff {
					clear(decoded)
					return nil, false
				}
				decoded = utf8.AppendRune(decoded, r)
			default:
				clear(decoded)
				return nil, false
			}
		case value < utf8.RuneSelf:
			decoded = append(decoded, value)
		default:
			_, size := utf8.DecodeRune(raw[i : len(raw)-1])
			if size == 1 {
				clear(decoded)
				return nil, false
			}
			decoded = append(decoded, raw[i:i+size]...)
			i += size - 1
		}
	}
	return decoded, true
}

func validateUserDeleteJSON(raw []byte) error {
	if !utf8.Valid(raw) || !validUserDeleteEscapedUnicode(raw) {
		return ErrUserDeleteInvalidBody
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return ErrUserDeleteInvalidBody
	}
	root, ok := token.(json.Delim)
	if !ok || root != '{' {
		return ErrUserDeleteInvalidBody
	}
	if err := scanUserDeleteObject(decoder); err != nil {
		return ErrUserDeleteInvalidBody
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ErrUserDeleteInvalidBody
	}
	return nil
}

func scanUserDeleteObject(decoder *json.Decoder) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return ErrUserDeleteInvalidBody
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrUserDeleteInvalidBody
		}
		seen[key] = struct{}{}
		if err := scanUserDeleteValue(decoder); err != nil {
			return err
		}
	}
	token, err := decoder.Token()
	if err != nil || token != json.Delim('}') {
		return ErrUserDeleteInvalidBody
	}
	return nil
}

func scanUserDeleteValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		return scanUserDeleteObject(decoder)
	case '[':
		for decoder.More() {
			if err := scanUserDeleteValue(decoder); err != nil {
				return err
			}
		}
		token, err = decoder.Token()
		if err != nil || token != json.Delim(']') {
			return ErrUserDeleteInvalidBody
		}
		return nil
	default:
		return ErrUserDeleteInvalidBody
	}
}

func parseCanonicalPositiveInt64(raw string) (int64, bool) {
	if raw == "" || raw[0] < '1' || raw[0] > '9' {
		return 0, false
	}
	for i := 1; i < len(raw); i++ {
		if raw[i] < '0' || raw[i] > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value > 0
}

func parseUserDeleteVersion(raw json.RawMessage) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	for _, value := range raw {
		if value < '0' || value > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(string(raw), 10, 32)
	return int(parsed), err == nil && parsed > 0
}

func normalizeUserDeleteReason(raw string) (string, bool) {
	normalized := strings.TrimSpace(raw)
	count := utf8.RuneCountInString(normalized)
	return normalized, utf8.ValidString(normalized) && count >= 1 && count <= 200
}

func validUserDeleteEscapedUnicode(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '"' {
			continue
		}
		for i++; i < len(raw) && raw[i] != '"'; i++ {
			if raw[i] != '\\' {
				continue
			}
			i++
			if i >= len(raw) {
				return false
			}
			if raw[i] != 'u' {
				continue
			}
			value, ok := parseUserDeleteHex4(raw, i+1)
			if !ok {
				return false
			}
			i += 4
			if utf16.IsSurrogate(rune(value)) {
				if value < 0xd800 || value > 0xdbff || i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return false
				}
				low, ok := parseUserDeleteHex4(raw, i+3)
				if !ok || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			}
		}
		if i >= len(raw) {
			return false
		}
	}
	return true
}

func parseUserDeleteHex4(raw []byte, start int) (uint16, bool) {
	if start+4 > len(raw) {
		return 0, false
	}
	var value uint16
	for _, digit := range raw[start : start+4] {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += uint16(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += uint16(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += uint16(digit-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}
