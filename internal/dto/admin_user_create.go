package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

const AdminUserCreateBodyLimit int64 = 4 * 1024

var (
	ErrAdminUserCreateInvalidBody    = errors.New("invalid admin user create request")
	ErrAdminUserCreateBodyTooLarge   = errors.New("admin user create request body too large")
	ErrAdminUserCreateInactiveAction = errors.New("inactive admin user create action")
)

// AdminUserCreateRequest is the normalized public creation intent. Password is
// intentionally owned by the request and callers must invoke ClearSecrets.
type AdminUserCreateRequest struct {
	Username            string
	Nickname            *string
	Password            []byte
	Role                models.UserRole
	GroupGUID           *int64
	PlanType            models.PlanType
	PermissionOverrides []actionsecurity.PermissionOverrideIntent
}

func (request AdminUserCreateRequest) String() string { return request.redactedString() }

func (request AdminUserCreateRequest) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.redactedString())
}

func (request AdminUserCreateRequest) redactedString() string {
	return fmt.Sprintf("AdminUserCreateRequest{Username:%q Nickname:%v Password:<redacted> Role:%q GroupGUID:%v PlanType:%q PermissionOverrides:%v}", request.Username, request.Nickname, request.Role.String(), request.GroupGUID, request.PlanType.String(), request.PermissionOverrides)
}

// ClearSecrets zeros the owned password before releasing its reference.
func (request *AdminUserCreateRequest) ClearSecrets() {
	if request == nil {
		return
	}
	clear(request.Password)
	request.Password = nil
}

// AdminUserCreateVerificationRequest is the exact ticket-issue wrapper for a
// Root's administrator creation request.
type AdminUserCreateVerificationRequest struct {
	Action          string
	Intent          AdminUserCreateRequest
	CurrentPassword []byte
}

func (request AdminUserCreateVerificationRequest) String() string { return request.redactedString() }

func (request AdminUserCreateVerificationRequest) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.redactedString())
}

func (request AdminUserCreateVerificationRequest) redactedString() string {
	return fmt.Sprintf("AdminUserCreateVerificationRequest{Action:%q Intent:%s CurrentPassword:<redacted>}", request.Action, request.Intent.redactedString())
}

// ClearSecrets independently clears both credential values on every lifecycle.
func (request *AdminUserCreateVerificationRequest) ClearSecrets() {
	if request == nil {
		return
	}
	request.Intent.ClearSecrets()
	clear(request.CurrentPassword)
	request.CurrentPassword = nil
}

// DecodeAdminUserCreate decodes exactly one bounded, strict creation object.
func DecodeAdminUserCreate(body io.Reader) (AdminUserCreateRequest, error) {
	raw, err := readAdminUserCreateBody(body)
	if err != nil {
		return AdminUserCreateRequest{}, err
	}
	defer clear(raw)
	return decodeAdminUserCreateRaw(raw)
}

func decodeAdminUserCreateRaw(raw []byte) (AdminUserCreateRequest, error) {
	keys := []string{"username", "nickname", "password", "role", "group_guid", "plan_type", "permission_overrides"}
	values, present, err := scanAdminUserCreateObject(raw, []string{"username", "password", "role"}, keys)
	if err != nil {
		return AdminUserCreateRequest{}, ErrAdminUserCreateInvalidBody
	}
	usernameRaw, ok := decodeUserDeleteString(values[0])
	if !ok {
		return AdminUserCreateRequest{}, ErrAdminUserCreateInvalidBody
	}
	username, err := service.NormalizeUsername(usernameRaw)
	if err != nil {
		return AdminUserCreateRequest{}, ErrAdminUserCreateInvalidBody
	}
	password, ok := decodeOwnedUserDeleteJSONString(values[2])
	if !ok || service.ValidatePassword(string(password)) != nil {
		clear(password)
		return AdminUserCreateRequest{}, ErrAdminUserCreateInvalidBody
	}
	request := AdminUserCreateRequest{Username: username, Password: password, PlanType: models.PlanFree, PermissionOverrides: make([]actionsecurity.PermissionOverrideIntent, 0)}
	fail := func() (AdminUserCreateRequest, error) {
		request.ClearSecrets()
		return AdminUserCreateRequest{}, ErrAdminUserCreateInvalidBody
	}
	roleRaw, ok := decodeUserDeleteString(values[3])
	if !ok {
		return fail()
	}
	role, ok := models.ParseUserRole(roleRaw)
	if !ok || (role != models.UserRoleUser && role != models.UserRoleAdmin) {
		return fail()
	}
	request.Role = role
	if present[1] {
		nickname, null, ok := decodeAdminUserCreateNullableString(values[1])
		if !ok {
			return fail()
		}
		if !null {
			nickname, err = service.NormalizeManagedUserNickname(nickname)
			if err != nil {
				return fail()
			}
			request.Nickname = &nickname
		}
	}
	if present[4] {
		groupRaw, null, ok := decodeAdminUserCreateNullableString(values[4])
		if !ok {
			return fail()
		}
		if !null {
			groupGUID, ok := parseCanonicalPositiveInt64(groupRaw)
			if !ok {
				return fail()
			}
			request.GroupGUID = &groupGUID
		}
	}
	if present[5] {
		planRaw, ok := decodeUserDeleteString(values[5])
		if !ok {
			return fail()
		}
		plan, ok := models.ParsePlanType(planRaw)
		if !ok {
			return fail()
		}
		request.PlanType = plan
	}
	if present[6] {
		overrides, ok := decodeAdminUserCreateOverrides(values[6])
		if !ok || (request.Role != models.UserRoleAdmin && len(overrides) != 0) {
			return fail()
		}
		request.PermissionOverrides = overrides
	}
	return request, nil
}

// DecodeAdminUserCreateVerification accepts only the active administrator
// ticket action and clears both independently owned passwords on any failure.
func DecodeAdminUserCreateVerification(body io.Reader) (AdminUserCreateVerificationRequest, error) {
	raw, err := readAdminUserCreateBody(body)
	if err != nil {
		return AdminUserCreateVerificationRequest{}, err
	}
	defer clear(raw)
	values, _, err := scanAdminUserCreateObject(raw, []string{"action", "intent", "current_password"}, []string{"action", "intent", "current_password"})
	if err != nil {
		return AdminUserCreateVerificationRequest{}, ErrAdminUserCreateInvalidBody
	}
	action, ok := decodeUserDeleteString(values[0])
	if !ok || bytes.Equal(bytes.TrimSpace(values[0]), []byte("null")) {
		return AdminUserCreateVerificationRequest{}, ErrAdminUserCreateInvalidBody
	}
	if action != "users.create_admin" {
		return AdminUserCreateVerificationRequest{}, ErrAdminUserCreateInactiveAction
	}
	intent, err := decodeAdminUserCreateRaw(values[1])
	if err != nil {
		return AdminUserCreateVerificationRequest{}, err
	}
	request := AdminUserCreateVerificationRequest{Action: action, Intent: intent}
	fail := func(err error) (AdminUserCreateVerificationRequest, error) {
		request.ClearSecrets()
		return AdminUserCreateVerificationRequest{}, err
	}
	if request.Intent.Role != models.UserRoleAdmin {
		return fail(ErrAdminUserCreateInvalidBody)
	}
	current, ok := decodeOwnedUserDeleteJSONString(values[2])
	if !ok || len(current) == 0 {
		clear(current)
		return fail(ErrAdminUserCreateInvalidBody)
	}
	request.CurrentPassword = current
	return request, nil
}

func readAdminUserCreateBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, ErrAdminUserCreateInvalidBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, AdminUserCreateBodyLimit+1))
	if err != nil {
		var maxBytesError *http.MaxBytesError
		clear(raw)
		if errors.As(err, &maxBytesError) {
			return nil, ErrAdminUserCreateBodyTooLarge
		}
		return nil, ErrAdminUserCreateInvalidBody
	}
	if int64(len(raw)) > AdminUserCreateBodyLimit {
		clear(raw)
		return nil, ErrAdminUserCreateBodyTooLarge
	}
	return raw, nil
}

func scanAdminUserCreateObject(raw []byte, required, allowed []string) ([]json.RawMessage, []bool, error) {
	if !utf8.Valid(raw) {
		return nil, nil, ErrAdminUserCreateInvalidBody
	}
	values, present, end, err := scanAdminUserCreateObjectAt(raw, 0, required, allowed)
	if err != nil || skipUserDeleteSpace(raw, end) != len(raw) {
		return nil, nil, ErrAdminUserCreateInvalidBody
	}
	return values, present, nil
}

func scanAdminUserCreateObjectAt(raw []byte, offset int, required, allowed []string) ([]json.RawMessage, []bool, int, error) {
	offset = skipUserDeleteSpace(raw, offset)
	if offset >= len(raw) || raw[offset] != '{' {
		return nil, nil, offset, ErrAdminUserCreateInvalidBody
	}
	offset++
	values := make([]json.RawMessage, len(allowed))
	present := make([]bool, len(allowed))
	offset = skipUserDeleteSpace(raw, offset)
	if offset < len(raw) && raw[offset] == '}' {
		return nil, nil, offset, ErrAdminUserCreateInvalidBody
	}
	for {
		keyStart := offset
		keyEnd, err := skipUserDeleteJSONString(raw, keyStart)
		if err != nil {
			return nil, nil, offset, err
		}
		keyBytes := raw[keyStart+1 : keyEnd-1]
		for _, value := range keyBytes {
			if value == '\\' {
				return nil, nil, offset, ErrAdminUserCreateInvalidBody
			}
		}
		index := exactUserDeleteKeyIndex(keyBytes, allowed)
		if index < 0 || present[index] {
			return nil, nil, offset, ErrAdminUserCreateInvalidBody
		}
		present[index] = true
		offset = skipUserDeleteSpace(raw, keyEnd)
		if offset >= len(raw) || raw[offset] != ':' {
			return nil, nil, offset, ErrAdminUserCreateInvalidBody
		}
		valueStart := skipUserDeleteSpace(raw, offset+1)
		valueEnd, err := skipUserDeleteValue(raw, valueStart)
		if err != nil {
			return nil, nil, offset, err
		}
		values[index] = raw[valueStart:valueEnd]
		offset = skipUserDeleteSpace(raw, valueEnd)
		if offset >= len(raw) {
			return nil, nil, offset, ErrAdminUserCreateInvalidBody
		}
		if raw[offset] == '}' {
			for _, key := range required {
				index := exactUserDeleteKeyIndex([]byte(key), allowed)
				if index < 0 || !present[index] {
					return nil, nil, offset, ErrAdminUserCreateInvalidBody
				}
			}
			return values, present, offset + 1, nil
		}
		if raw[offset] != ',' {
			return nil, nil, offset, ErrAdminUserCreateInvalidBody
		}
		offset = skipUserDeleteSpace(raw, offset+1)
		if offset >= len(raw) || raw[offset] == '}' {
			return nil, nil, offset, ErrAdminUserCreateInvalidBody
		}
	}
}

func decodeAdminUserCreateNullableString(raw json.RawMessage) (string, bool, bool) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", true, true
	}
	value, ok := decodeUserDeleteString(raw)
	return value, false, ok
}

func decodeAdminUserCreateOverrides(raw json.RawMessage) ([]actionsecurity.PermissionOverrideIntent, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return nil, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(trimmed, &items); err != nil {
		return nil, false
	}
	overrides := make([]actionsecurity.PermissionOverrideIntent, 0, len(items))
	for _, item := range items {
		values, _, err := scanAdminUserCreateObject(item, []string{"capability", "effect"}, []string{"capability", "effect"})
		if err != nil {
			return nil, false
		}
		capability, ok := decodeUserDeleteString(values[0])
		if !ok || capability == "" || !utf8.ValidString(capability) || !adminUserCreateGrantableCapability(capability) {
			return nil, false
		}
		effect, ok := decodeUserDeleteString(values[1])
		if !ok {
			return nil, false
		}
		value := 0
		switch effect {
		case "allow":
			value = 2
		case "deny":
			value = 3
		default:
			return nil, false
		}
		overrides = append(overrides, actionsecurity.PermissionOverrideIntent{Capability: capability, Effect: value})
	}
	for i := 1; i < len(overrides); i++ {
		for j := 0; j < i; j++ {
			if overrides[i].Capability == overrides[j].Capability {
				return nil, false
			}
		}
	}
	for i := 1; i < len(overrides); i++ {
		for j := i; j > 0 && overrides[j].Capability < overrides[j-1].Capability; j-- {
			overrides[j], overrides[j-1] = overrides[j-1], overrides[j]
		}
	}
	return overrides, true
}

func adminUserCreateGrantableCapability(name string) bool {
	for _, definition := range authz.Catalog() {
		if definition.Name == name {
			return definition.Grantable && !definition.RootOnly && !definition.Unavailable
		}
	}
	return false
}
