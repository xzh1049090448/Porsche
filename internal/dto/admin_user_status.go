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

const AdminUserStatusBodyLimit int64 = 4 * 1024

var (
	ErrAdminUserStatusInvalidBody  = errors.New("invalid admin user status request")
	ErrAdminUserStatusBodyTooLarge = errors.New("admin user status request body too large")
)

// AdminUserStatusRequest is an owned, normalized A06 transition intent.
type AdminUserStatusRequest struct {
	Status              string
	Reason              *string
	ExpectedAuthVersion int
}

// DecodeAdminUserStatus accepts exactly the three frozen A06 fields. The
// token pass rejects aliases and duplicates before any value is interpreted.
func DecodeAdminUserStatus(body io.Reader) (AdminUserStatusRequest, error) {
	if body == nil {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, AdminUserStatusBodyLimit+1))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		clear(raw)
		if errors.As(err, &maxBytesError) {
			return AdminUserStatusRequest{}, ErrAdminUserStatusBodyTooLarge
		}
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}
	defer clear(raw)
	if int64(len(raw)) > AdminUserStatusBodyLimit {
		return AdminUserStatusRequest{}, ErrAdminUserStatusBodyTooLarge
	}
	if len(raw) == 0 || !utf8.Valid(raw) || !validAdminUserEditStringTokens(raw) {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}
	values := make(map[string]json.RawMessage, 3)
	seenFolded := make(map[string]struct{}, 3)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
		}
		folded := strings.ToLower(key)
		if _, exists := seenFolded[folded]; exists {
			return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
		}
		seenFolded[folded] = struct{}{}
		if key != "status" && key != "reason" && key != "expected_auth_version" {
			return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
		}
		values[key] = value
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}

	statusRaw, hasStatus := values["status"]
	reasonRaw, hasReason := values["reason"]
	versionRaw, hasVersion := values["expected_auth_version"]
	if !hasStatus || !hasReason || !hasVersion {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}
	var status string
	if json.Unmarshal(statusRaw, &status) != nil || (status != "active" && status != "disabled") {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}
	version, ok := parseAdminUserStatusVersion(versionRaw)
	if !ok {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}

	request := AdminUserStatusRequest{Status: status, ExpectedAuthVersion: version}
	if status == "active" {
		if !bytes.Equal(bytes.TrimSpace(reasonRaw), []byte("null")) {
			return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
		}
		return request, nil
	}
	var reason string
	if json.Unmarshal(reasonRaw, &reason) != nil {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}
	reason = strings.TrimSpace(reason)
	if count := utf8.RuneCountInString(reason); count < 1 || count > 200 {
		return AdminUserStatusRequest{}, ErrAdminUserStatusInvalidBody
	}
	request.Reason = &reason
	return request, nil
}

func parseAdminUserStatusVersion(raw json.RawMessage) (int, bool) {
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
