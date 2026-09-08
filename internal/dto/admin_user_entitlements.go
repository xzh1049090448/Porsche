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

const AdminUserEntitlementBodyLimit int64 = 4 * 1024

var (
	ErrAdminUserEntitlementInvalidBody  = errors.New("invalid admin user entitlement request")
	ErrAdminUserEntitlementBodyTooLarge = errors.New("admin user entitlement request body too large")
)

type AdminUserGroupChangeRequest struct {
	GroupGUID           int64
	Reason              string
	ExpectedAuthVersion int
}

type AdminUserPlanChangeRequest struct {
	PlanType            string
	Reason              string
	ExpectedAuthVersion int
}

func DecodeAdminUserGroupChange(body io.Reader) (AdminUserGroupChangeRequest, error) {
	values, err := decodeAdminUserEntitlementObject(body, []string{"group_guid", "reason", "expected_auth_version"})
	if err != nil {
		return AdminUserGroupChangeRequest{}, err
	}
	var guidRaw string
	if json.Unmarshal(values["group_guid"], &guidRaw) != nil {
		return AdminUserGroupChangeRequest{}, ErrAdminUserEntitlementInvalidBody
	}
	guid, ok := parseCanonicalPositiveInt64Entitlement(guidRaw)
	if !ok {
		return AdminUserGroupChangeRequest{}, ErrAdminUserEntitlementInvalidBody
	}
	reason, version, ok := parseAdminUserEntitlementCommon(values)
	if !ok {
		return AdminUserGroupChangeRequest{}, ErrAdminUserEntitlementInvalidBody
	}
	return AdminUserGroupChangeRequest{GroupGUID: guid, Reason: reason, ExpectedAuthVersion: version}, nil
}

func DecodeAdminUserPlanChange(body io.Reader) (AdminUserPlanChangeRequest, error) {
	values, err := decodeAdminUserEntitlementObject(body, []string{"plan_type", "reason", "expected_auth_version"})
	if err != nil {
		return AdminUserPlanChangeRequest{}, err
	}
	var plan string
	if json.Unmarshal(values["plan_type"], &plan) != nil || (plan != "free" && plan != "professional" && plan != "enterprise") {
		return AdminUserPlanChangeRequest{}, ErrAdminUserEntitlementInvalidBody
	}
	reason, version, ok := parseAdminUserEntitlementCommon(values)
	if !ok {
		return AdminUserPlanChangeRequest{}, ErrAdminUserEntitlementInvalidBody
	}
	return AdminUserPlanChangeRequest{PlanType: plan, Reason: reason, ExpectedAuthVersion: version}, nil
}

func decodeAdminUserEntitlementObject(body io.Reader, keys []string) (map[string]json.RawMessage, error) {
	if body == nil {
		return nil, ErrAdminUserEntitlementInvalidBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, AdminUserEntitlementBodyLimit+1))
	if err != nil {
		var size *http.MaxBytesError
		if errors.As(err, &size) {
			return nil, ErrAdminUserEntitlementBodyTooLarge
		}
		return nil, ErrAdminUserEntitlementInvalidBody
	}
	defer clear(raw)
	if int64(len(raw)) > AdminUserEntitlementBodyLimit {
		return nil, ErrAdminUserEntitlementBodyTooLarge
	}
	if len(raw) == 0 || !utf8.Valid(raw) || !validAdminUserEditStringTokens(raw) {
		return nil, ErrAdminUserEntitlementInvalidBody
	}
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return nil, ErrAdminUserEntitlementInvalidBody
	}
	values := make(map[string]json.RawMessage, len(keys))
	seenFolded := make(map[string]struct{}, len(keys))
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrAdminUserEntitlementInvalidBody
		}
		folded := strings.ToLower(key)
		if _, duplicate := seenFolded[folded]; duplicate {
			return nil, ErrAdminUserEntitlementInvalidBody
		}
		seenFolded[folded] = struct{}{}
		if _, ok := allowed[key]; !ok {
			return nil, ErrAdminUserEntitlementInvalidBody
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrAdminUserEntitlementInvalidBody
		}
		values[key] = value
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') || len(values) != len(keys) {
		return nil, ErrAdminUserEntitlementInvalidBody
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrAdminUserEntitlementInvalidBody
	}
	return values, nil
}

func parseAdminUserEntitlementCommon(values map[string]json.RawMessage) (string, int, bool) {
	var reason string
	if json.Unmarshal(values["reason"], &reason) != nil {
		return "", 0, false
	}
	reason = strings.TrimSpace(reason)
	if count := utf8.RuneCountInString(reason); count < 1 || count > 200 {
		return "", 0, false
	}
	version, ok := parseAdminUserStatusVersion(values["expected_auth_version"])
	return reason, version, ok
}

func parseCanonicalPositiveInt64Entitlement(raw string) (int64, bool) {
	if raw == "" || raw[0] < '1' || raw[0] > '9' {
		return 0, false
	}
	for _, digit := range raw[1:] {
		if digit < '0' || digit > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value > 0
}
