package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const AdminUserEditBodyLimit int64 = 4 * 1024

var (
	ErrAdminUserEditInvalidBody  = errors.New("invalid admin user edit request")
	ErrAdminUserEditBodyTooLarge = errors.New("admin user edit request body too large")
)

// AdminUserEditRequest is an owned, normalized A05 nickname edit intent.
type AdminUserEditRequest struct {
	Nickname            *string
	ClearNickname       bool
	ExpectedAuthVersion int
}

// DecodeAdminUserEdit decodes exactly one bounded object containing the two
// A05 request fields. It scans tokens before decoding values so duplicate,
// case-folded, and unknown keys never enter a struct binder.
func DecodeAdminUserEdit(body io.Reader) (AdminUserEditRequest, error) {
	if body == nil {
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, AdminUserEditBodyLimit+1))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		clear(raw)
		if errors.As(err, &maxBytesError) {
			return AdminUserEditRequest{}, ErrAdminUserEditBodyTooLarge
		}
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}
	defer clear(raw)
	if int64(len(raw)) > AdminUserEditBodyLimit {
		return AdminUserEditRequest{}, ErrAdminUserEditBodyTooLarge
	}
	if len(raw) == 0 || !utf8.Valid(raw) || !validAdminUserEditStringTokens(raw) {
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}
	values := make(map[string]json.RawMessage, 2)
	seenFolded := make(map[string]struct{}, 2)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
		}
		folded := strings.ToLower(key)
		if _, exists := seenFolded[folded]; exists {
			return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
		}
		seenFolded[folded] = struct{}{}
		if key != "nickname" && key != "expected_auth_version" {
			return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
		}
		values[key] = value
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}

	nicknameRaw, hasNickname := values["nickname"]
	versionRaw, hasVersion := values["expected_auth_version"]
	if !hasNickname || !hasVersion {
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}

	version, ok := parseAdminUserEditVersion(versionRaw)
	if !ok {
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}
	request := AdminUserEditRequest{ExpectedAuthVersion: version}
	if bytes.Equal(bytes.TrimSpace(nicknameRaw), []byte("null")) {
		request.ClearNickname = true
		return request, nil
	}
	var nickname string
	if err := json.Unmarshal(nicknameRaw, &nickname); err != nil {
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}
	nickname = strings.TrimSpace(nickname)
	if count := utf8.RuneCountInString(nickname); count < 1 || count > 64 {
		return AdminUserEditRequest{}, ErrAdminUserEditInvalidBody
	}
	request.Nickname = &nickname
	return request, nil
}

func parseAdminUserEditVersion(raw json.RawMessage) (int, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] < '1' || raw[0] > '9' {
		return 0, false
	}
	for _, value := range raw[1:] {
		if value < '0' || value > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(string(raw), 10, 32)
	return int(parsed), err == nil && parsed > 0
}

// validAdminUserEditStringTokens rejects control bytes, malformed escapes, and
// unpaired UTF-16 surrogate escapes before encoding/json can replace them.
func validAdminUserEditStringTokens(raw []byte) bool {
	for offset := 0; offset < len(raw); offset++ {
		if raw[offset] != '"' {
			continue
		}
		offset++
		closed := false
		for offset < len(raw) {
			value := raw[offset]
			switch {
			case value == '"':
				closed = true
			case value < 0x20:
				return false
			case value == '\\':
				offset++
				if offset >= len(raw) {
					return false
				}
				switch raw[offset] {
				case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
				case 'u':
					codePoint, ok := parseAdminUserEditHex4(raw, offset+1)
					if !ok {
						return false
					}
					offset += 4
					if codePoint >= 0xd800 && codePoint <= 0xdbff {
						if offset+6 >= len(raw) || raw[offset+1] != '\\' || raw[offset+2] != 'u' {
							return false
						}
						low, ok := parseAdminUserEditHex4(raw, offset+3)
						if !ok || low < 0xdc00 || low > 0xdfff {
							return false
						}
						offset += 6
					} else if codePoint >= 0xdc00 && codePoint <= 0xdfff {
						return false
					}
				default:
					return false
				}
			case value >= utf8.RuneSelf:
				_, size := utf8.DecodeRune(raw[offset:])
				if size == 1 {
					return false
				}
				offset += size - 1
			}
			if closed {
				break
			}
			offset++
		}
		if !closed {
			return false
		}
	}
	return true
}

func parseAdminUserEditHex4(raw []byte, offset int) (uint16, bool) {
	if offset+4 > len(raw) {
		return 0, false
	}
	var parsed uint16
	for _, value := range raw[offset : offset+4] {
		parsed <<= 4
		switch {
		case value >= '0' && value <= '9':
			parsed += uint16(value - '0')
		case value >= 'a' && value <= 'f':
			parsed += uint16(value-'a') + 10
		case value >= 'A' && value <= 'F':
			parsed += uint16(value-'A') + 10
		default:
			return 0, false
		}
	}
	return parsed, true
}
