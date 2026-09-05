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
	Action   string                `json:"action"`
	Intent   *userDeleteIntentWire `json:"intent"`
	Password *string               `json:"current_password"`
}

type userDeleteIntentWire struct {
	TargetGUID      string          `json:"target_guid"`
	ExpectedVersion json.RawMessage `json:"expected_auth_version"`
	Reason          string          `json:"reason"`
}

type userDeleteExecuteWire struct {
	Action          string          `json:"action"`
	ExpectedVersion json.RawMessage `json:"expected_auth_version"`
	Reason          string          `json:"reason"`
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
	var wire userDeleteIssueWire
	if err := decodeUserDeleteWire(raw, &wire); err != nil || wire.Action != "users.delete" || wire.Intent == nil || wire.Password == nil || *wire.Password == "" {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	targetGUID, ok := parseCanonicalPositiveInt64(wire.Intent.TargetGUID)
	if !ok {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	version, ok := parseUserDeleteVersion(wire.Intent.ExpectedVersion)
	if !ok {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	reason, ok := normalizeUserDeleteReason(wire.Intent.Reason)
	if !ok {
		return IssueUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	password := append([]byte(nil), []byte(*wire.Password)...)
	wire.Password = nil
	return IssueUserDeleteRequest{Action: wire.Action, TargetGUID: targetGUID, ExpectedVersion: version, Reason: reason, Password: password}, nil
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
	var wire userDeleteExecuteWire
	if err := decodeUserDeleteWire(raw, &wire); err != nil || wire.Action != "delete" {
		return ExecuteUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	version, ok := parseUserDeleteVersion(wire.ExpectedVersion)
	if !ok {
		return ExecuteUserDeleteRequest{}, ErrUserDeleteInvalidBody
	}
	reason, ok := normalizeUserDeleteReason(wire.Reason)
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

func decodeUserDeleteWire(raw []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return ErrUserDeleteInvalidBody
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ErrUserDeleteInvalidBody
	}
	return nil
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
