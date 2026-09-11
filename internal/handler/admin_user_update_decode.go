package handler

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"unicode/utf8"
)

const adminUserUpdateBodyLimit = 64 << 10

var (
	errInvalidAdminUserUpdateJSON   = errors.New("invalid admin user update JSON")
	errAdminUserUpdateTooLarge      = errors.New("admin user update body too large")
	errAdminUserUpdateStatusRetired = errors.New("admin user update status field is retired")
)

type adminUserUpdateRequest struct {
	PlanType       *string
	AllowedModels  *[]string
	DailyCallLimit *int
}

var adminUserUpdateFields = map[string]struct{}{
	"status": {}, "plan_type": {}, "allowed_models": {}, "daily_call_limit": {},
	"role": {}, "auth_version": {}, "expected_auth_version": {},
	"expected_permissions_version": {}, "permissions_version": {}, "catalog_version": {},
	"overrides": {}, "action": {}, "reason": {},
}

// decodeAdminUserUpdate accepts exactly one small JSON object. It scans keys
// itself because encoding/json otherwise accepts duplicate and case-folded
// fields, which could change an administrator's target unexpectedly.
func decodeAdminUserUpdate(body io.Reader) (adminUserUpdateRequest, error) {
	limited := io.LimitReader(body, adminUserUpdateBodyLimit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
	}
	if len(data) > adminUserUpdateBodyLimit {
		return adminUserUpdateRequest{}, errAdminUserUpdateTooLarge
	}
	if len(data) == 0 || !utf8.Valid(data) {
		return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
	}
	seen := make(map[string]struct{}, 4)
	raw := make(map[string]json.RawMessage, 4)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
		}
		if _, ok := adminUserUpdateFields[key]; !ok {
			return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
		}
		if _, duplicate := seen[key]; duplicate {
			return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
		}
		raw[key] = value
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') {
		return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
	}

	if _, hasStatus := raw["status"]; hasStatus {
		return adminUserUpdateRequest{}, errAdminUserUpdateStatusRetired
	}
	// A07 owns these mutations through dedicated v2 endpoints. Reject even null
	// or malformed values here, before legacy interpretation can reach storage.
	for _, retired := range []string{"plan_type", "allowed_models", "daily_call_limit", "role", "auth_version", "expected_auth_version", "expected_permissions_version", "permissions_version", "catalog_version", "overrides", "action", "reason"} {
		if _, present := raw[retired]; present {
			return adminUserUpdateRequest{}, errAdminUserUpdateStatusRetired
		}
	}
	request := adminUserUpdateRequest{}
	if value, ok := raw["plan_type"]; ok && !bytes.Equal(value, []byte("null")) {
		var parsed string
		if err := json.Unmarshal(value, &parsed); err != nil {
			return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
		}
		request.PlanType = &parsed
	}
	if value, ok := raw["allowed_models"]; ok && !bytes.Equal(value, []byte("null")) {
		var entries []json.RawMessage
		if err := json.Unmarshal(value, &entries); err != nil {
			return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
		}
		models := make([]string, len(entries))
		for index, entry := range entries {
			if bytes.Equal(entry, []byte("null")) || json.Unmarshal(entry, &models[index]) != nil || models[index] == "" {
				return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
			}
		}
		request.AllowedModels = &models
	}
	if value, ok := raw["daily_call_limit"]; ok && !bytes.Equal(value, []byte("null")) {
		if len(value) == 0 {
			return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
			}
		}
		var number json.Number
		if err := json.Unmarshal(value, &number); err != nil {
			return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
		}
		parsed, err := strconv.ParseInt(number.String(), 10, 64)
		if err != nil || parsed < 0 || parsed > 2147483647 {
			return adminUserUpdateRequest{}, errInvalidAdminUserUpdateJSON
		}
		limit := int(parsed)
		request.DailyCallLimit = &limit
	}
	return request, nil
}
