package dto

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
)

const RolePermissionBodyLimit int64 = 4 * 1024

var (
	ErrRolePermissionInvalidBody    = errors.New("invalid role permission request")
	ErrRolePermissionBodyTooLarge   = errors.New("role permission request body too large")
	ErrRolePermissionInactiveAction = errors.New("inactive role permission action")
)

type PromoteIssueRequest struct {
	Action          string                       `json:"action"`
	Intent          actionsecurity.PromoteIntent `json:"intent"`
	CurrentPassword []byte                       `json:"-"`
}

type DemoteIssueRequest struct {
	Action          string                      `json:"action"`
	Intent          actionsecurity.DemoteIntent `json:"intent"`
	CurrentPassword []byte                      `json:"-"`
}

type PermissionsWriteIssueRequest struct {
	Action          string                                `json:"action"`
	Intent          actionsecurity.PermissionsWriteIntent `json:"intent"`
	CurrentPassword []byte                                `json:"-"`
}

type PromoteExecuteRequest struct {
	Action                     string
	ExpectedAuthVersion        int
	ExpectedPermissionsVersion int64
	CatalogVersion             int
	Overrides                  []actionsecurity.PermissionOverrideIntent
	Reason                     string
}

func (request PromoteExecuteRequest) Intent(targetGUID int64) actionsecurity.PromoteIntent {
	return actionsecurity.PromoteIntent{TargetGUID: targetGUID, ExpectedAuthVersion: request.ExpectedAuthVersion, ExpectedPermissionsVersion: request.ExpectedPermissionsVersion, CatalogVersion: request.CatalogVersion, Overrides: append([]actionsecurity.PermissionOverrideIntent(nil), request.Overrides...), Reason: request.Reason}
}

type DemoteExecuteRequest struct {
	Action                     string
	ExpectedAuthVersion        int
	ExpectedPermissionsVersion int64
	CatalogVersion             int
	Reason                     string
}

func (request DemoteExecuteRequest) Intent(targetGUID int64) actionsecurity.DemoteIntent {
	return actionsecurity.DemoteIntent{TargetGUID: targetGUID, ExpectedAuthVersion: request.ExpectedAuthVersion, ExpectedPermissionsVersion: request.ExpectedPermissionsVersion, CatalogVersion: request.CatalogVersion, Reason: request.Reason}
}

type PermissionsWriteExecuteRequest struct {
	ExpectedAuthVersion        int
	ExpectedPermissionsVersion int64
	CatalogVersion             int
	Overrides                  []actionsecurity.PermissionOverrideIntent
	Reason                     string
}

func (request PermissionsWriteExecuteRequest) Intent(targetGUID int64) actionsecurity.PermissionsWriteIntent {
	return actionsecurity.PermissionsWriteIntent{TargetGUID: targetGUID, ExpectedAuthVersion: request.ExpectedAuthVersion, ExpectedPermissionsVersion: request.ExpectedPermissionsVersion, CatalogVersion: request.CatalogVersion, Overrides: append([]actionsecurity.PermissionOverrideIntent(nil), request.Overrides...), Reason: request.Reason}
}

type RolePermissionResult struct {
	OperationRef                string `json:"operation_ref"`
	TargetGUID                  string `json:"target_guid"`
	ResultingAuthVersion        int    `json:"resulting_auth_version"`
	ResultingPermissionsVersion int64  `json:"resulting_permissions_version"`
	ResultingRole               string `json:"resulting_role"`
}

type RolePermissionQuery struct {
	OperationRef                string  `json:"operation_ref"`
	Scope                       string  `json:"scope"`
	Status                      string  `json:"status"`
	FinishedAt                  *int64  `json:"finished_at"`
	FailureCode                 *string `json:"failure_code"`
	TargetGUID                  *string `json:"target_guid"`
	ResultingAuthVersion        *int    `json:"resulting_auth_version"`
	ResultingPermissionsVersion *int64  `json:"resulting_permissions_version"`
	ResultingRole               *string `json:"resulting_role"`
}

type RolePermissionQueryResponse = RolePermissionQuery
type PermissionWriteIssueRequest = PermissionsWriteIssueRequest
type PermissionWriteExecuteRequest = PermissionsWriteExecuteRequest

func (request PromoteIssueRequest) String() string {
	return fmt.Sprintf("PromoteIssueRequest{Action:%q Intent:%v CurrentPassword:<redacted>}", request.Action, request.Intent)
}
func (request PromoteIssueRequest) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.String())
}
func (request *PromoteIssueRequest) ClearSecrets() {
	if request != nil {
		clear(request.CurrentPassword)
		request.CurrentPassword = nil
	}
}
func (request DemoteIssueRequest) String() string {
	return fmt.Sprintf("DemoteIssueRequest{Action:%q Intent:%v CurrentPassword:<redacted>}", request.Action, request.Intent)
}
func (request DemoteIssueRequest) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.String())
}
func (request *DemoteIssueRequest) ClearSecrets() {
	if request != nil {
		clear(request.CurrentPassword)
		request.CurrentPassword = nil
	}
}
func (request PermissionsWriteIssueRequest) String() string {
	return fmt.Sprintf("PermissionsWriteIssueRequest{Action:%q Intent:%v CurrentPassword:<redacted>}", request.Action, request.Intent)
}
func (request PermissionsWriteIssueRequest) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, request.String())
}
func (request *PermissionsWriteIssueRequest) ClearSecrets() {
	if request != nil {
		clear(request.CurrentPassword)
		request.CurrentPassword = nil
	}
}

func DecodePromoteIssue(body io.Reader) (PromoteIssueRequest, error) {
	action, intentRaw, password, err := decodeRolePermissionIssue(body, "users.promote")
	if err != nil {
		return PromoteIssueRequest{}, err
	}
	intent, err := decodePromoteIntent(intentRaw)
	if err != nil {
		clear(password)
		return PromoteIssueRequest{}, err
	}
	return PromoteIssueRequest{Action: action, Intent: intent, CurrentPassword: password}, nil
}

func DecodeDemoteIssue(body io.Reader) (DemoteIssueRequest, error) {
	action, intentRaw, password, err := decodeRolePermissionIssue(body, "users.demote")
	if err != nil {
		return DemoteIssueRequest{}, err
	}
	intent, err := decodeDemoteIntent(intentRaw)
	if err != nil {
		clear(password)
		return DemoteIssueRequest{}, err
	}
	return DemoteIssueRequest{Action: action, Intent: intent, CurrentPassword: password}, nil
}

func DecodePermissionsWriteIssue(body io.Reader) (PermissionsWriteIssueRequest, error) {
	action, intentRaw, password, err := decodeRolePermissionIssue(body, "users.permissions.write")
	if err != nil {
		return PermissionsWriteIssueRequest{}, err
	}
	intent, err := decodePermissionsWriteIntent(intentRaw, true)
	if err != nil {
		clear(password)
		return PermissionsWriteIssueRequest{}, err
	}
	return PermissionsWriteIssueRequest{Action: action, Intent: intent, CurrentPassword: password}, nil
}

func DecodePermissionWriteIssue(body io.Reader) (PermissionWriteIssueRequest, error) {
	return DecodePermissionsWriteIssue(body)
}

func DecodePromoteExecute(body io.Reader) (PromoteExecuteRequest, error) {
	raw, err := readRolePermissionBody(body)
	if err != nil {
		return PromoteExecuteRequest{}, err
	}
	defer clear(raw)
	values, _, err := scanAdminUserCreateObject(raw, []string{"action", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []string{"action", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"})
	if err != nil {
		return PromoteExecuteRequest{}, ErrRolePermissionInvalidBody
	}
	action, ok := decodeUserDeleteString(values[0])
	if !ok {
		return PromoteExecuteRequest{}, ErrRolePermissionInvalidBody
	}
	if action != "promote" {
		return PromoteExecuteRequest{}, ErrRolePermissionInactiveAction
	}
	intent, err := decodePromoteFields(values[1:])
	if err != nil {
		return PromoteExecuteRequest{}, err
	}
	return PromoteExecuteRequest{Action: action, ExpectedAuthVersion: intent.ExpectedAuthVersion, ExpectedPermissionsVersion: intent.ExpectedPermissionsVersion, CatalogVersion: intent.CatalogVersion, Overrides: intent.Overrides, Reason: intent.Reason}, nil
}

func DecodeDemoteExecute(body io.Reader) (DemoteExecuteRequest, error) {
	raw, err := readRolePermissionBody(body)
	if err != nil {
		return DemoteExecuteRequest{}, err
	}
	defer clear(raw)
	values, _, err := scanAdminUserCreateObject(raw, []string{"action", "expected_auth_version", "expected_permissions_version", "catalog_version", "reason"}, []string{"action", "expected_auth_version", "expected_permissions_version", "catalog_version", "reason"})
	if err != nil {
		return DemoteExecuteRequest{}, ErrRolePermissionInvalidBody
	}
	action, ok := decodeUserDeleteString(values[0])
	if !ok {
		return DemoteExecuteRequest{}, ErrRolePermissionInvalidBody
	}
	if action != "demote" {
		return DemoteExecuteRequest{}, ErrRolePermissionInactiveAction
	}
	intent, err := decodeDemoteFields(values[1:])
	if err != nil {
		return DemoteExecuteRequest{}, err
	}
	return DemoteExecuteRequest{Action: action, ExpectedAuthVersion: intent.ExpectedAuthVersion, ExpectedPermissionsVersion: intent.ExpectedPermissionsVersion, CatalogVersion: intent.CatalogVersion, Reason: intent.Reason}, nil
}

func DecodePermissionsWriteExecute(body io.Reader) (PermissionsWriteExecuteRequest, error) {
	raw, err := readRolePermissionBody(body)
	if err != nil {
		return PermissionsWriteExecuteRequest{}, err
	}
	defer clear(raw)
	values, _, err := scanAdminUserCreateObject(raw, []string{"expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []string{"expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"})
	if err != nil {
		return PermissionsWriteExecuteRequest{}, ErrRolePermissionInvalidBody
	}
	intent, err := decodePermissionsWriteFields(values)
	if err != nil {
		return PermissionsWriteExecuteRequest{}, err
	}
	return PermissionsWriteExecuteRequest{ExpectedAuthVersion: intent.ExpectedAuthVersion, ExpectedPermissionsVersion: intent.ExpectedPermissionsVersion, CatalogVersion: intent.CatalogVersion, Overrides: intent.Overrides, Reason: intent.Reason}, nil
}

func DecodePermissionWriteExecute(body io.Reader) (PermissionWriteExecuteRequest, error) {
	return DecodePermissionsWriteExecute(body)
}
func DecodePermissionsWritePatch(body io.Reader) (PermissionsWriteExecuteRequest, error) {
	return DecodePermissionsWriteExecute(body)
}
func DecodePermissionWritePatch(body io.Reader) (PermissionWriteExecuteRequest, error) {
	return DecodePermissionsWriteExecute(body)
}

func decodeRolePermissionIssue(body io.Reader, expectedAction string) (string, json.RawMessage, []byte, error) {
	raw, err := readRolePermissionBody(body)
	if err != nil {
		return "", nil, nil, err
	}
	defer clear(raw)
	values, _, err := scanAdminUserCreateObject(raw, []string{"action", "intent", "current_password"}, []string{"action", "intent", "current_password"})
	if err != nil {
		return "", nil, nil, ErrRolePermissionInvalidBody
	}
	action, ok := decodeUserDeleteString(values[0])
	if !ok {
		return "", nil, nil, ErrRolePermissionInvalidBody
	}
	if action != expectedAction {
		return "", nil, nil, ErrRolePermissionInactiveAction
	}
	password, ok := decodeOwnedUserDeleteJSONString(values[2])
	if !ok || len(password) == 0 {
		clear(password)
		return "", nil, nil, ErrRolePermissionInvalidBody
	}
	intent := append(json.RawMessage(nil), values[1]...)
	return action, intent, password, nil
}

func decodePromoteIntent(raw json.RawMessage) (actionsecurity.PromoteIntent, error) {
	values, _, err := scanAdminUserCreateObject(raw, []string{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []string{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"})
	if err != nil {
		return actionsecurity.PromoteIntent{}, ErrRolePermissionInvalidBody
	}
	target, ok := parseRolePermissionGUID(values[0])
	if !ok {
		return actionsecurity.PromoteIntent{}, ErrRolePermissionInvalidBody
	}
	intent, err := decodePromoteFields(values[1:])
	if err != nil {
		return actionsecurity.PromoteIntent{}, err
	}
	intent.TargetGUID = target
	return intent, nil
}

func decodePromoteFields(values []json.RawMessage) (actionsecurity.PromoteIntent, error) {
	auth, ok := parseRolePermissionPositiveInt32(values[0])
	if !ok {
		return actionsecurity.PromoteIntent{}, ErrRolePermissionInvalidBody
	}
	permissions, ok := parseRolePermissionInt64(values[1], true)
	if !ok {
		return actionsecurity.PromoteIntent{}, ErrRolePermissionInvalidBody
	}
	catalog, ok := parseRolePermissionPositiveInt32(values[2])
	if !ok {
		return actionsecurity.PromoteIntent{}, ErrRolePermissionInvalidBody
	}
	overrides, ok := decodeRolePermissionOverrides(values[3])
	if !ok {
		return actionsecurity.PromoteIntent{}, ErrRolePermissionInvalidBody
	}
	reason, ok := decodeRolePermissionReason(values[4])
	if !ok {
		return actionsecurity.PromoteIntent{}, ErrRolePermissionInvalidBody
	}
	return actionsecurity.PromoteIntent{ExpectedAuthVersion: auth, ExpectedPermissionsVersion: permissions, CatalogVersion: catalog, Overrides: overrides, Reason: reason}, nil
}

func decodeDemoteIntent(raw json.RawMessage) (actionsecurity.DemoteIntent, error) {
	values, _, err := scanAdminUserCreateObject(raw, []string{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "reason"}, []string{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "reason"})
	if err != nil {
		return actionsecurity.DemoteIntent{}, ErrRolePermissionInvalidBody
	}
	target, ok := parseRolePermissionGUID(values[0])
	if !ok {
		return actionsecurity.DemoteIntent{}, ErrRolePermissionInvalidBody
	}
	intent, err := decodeDemoteFields(values[1:])
	if err != nil {
		return actionsecurity.DemoteIntent{}, err
	}
	intent.TargetGUID = target
	return intent, nil
}

func decodeDemoteFields(values []json.RawMessage) (actionsecurity.DemoteIntent, error) {
	auth, ok := parseRolePermissionPositiveInt32(values[0])
	if !ok {
		return actionsecurity.DemoteIntent{}, ErrRolePermissionInvalidBody
	}
	permissions, ok := parseRolePermissionInt64(values[1], false)
	if !ok {
		return actionsecurity.DemoteIntent{}, ErrRolePermissionInvalidBody
	}
	catalog, ok := parseRolePermissionPositiveInt32(values[2])
	if !ok {
		return actionsecurity.DemoteIntent{}, ErrRolePermissionInvalidBody
	}
	reason, ok := decodeRolePermissionReason(values[3])
	if !ok {
		return actionsecurity.DemoteIntent{}, ErrRolePermissionInvalidBody
	}
	return actionsecurity.DemoteIntent{ExpectedAuthVersion: auth, ExpectedPermissionsVersion: permissions, CatalogVersion: catalog, Reason: reason}, nil
}

func decodePermissionsWriteIntent(raw json.RawMessage, withTarget bool) (actionsecurity.PermissionsWriteIntent, error) {
	if !withTarget {
		return actionsecurity.PermissionsWriteIntent{}, ErrRolePermissionInvalidBody
	}
	values, _, err := scanAdminUserCreateObject(raw, []string{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"}, []string{"target_guid", "expected_auth_version", "expected_permissions_version", "catalog_version", "overrides", "reason"})
	if err != nil {
		return actionsecurity.PermissionsWriteIntent{}, ErrRolePermissionInvalidBody
	}
	target, ok := parseRolePermissionGUID(values[0])
	if !ok {
		return actionsecurity.PermissionsWriteIntent{}, ErrRolePermissionInvalidBody
	}
	intent, err := decodePermissionsWriteFields(values[1:])
	if err != nil {
		return actionsecurity.PermissionsWriteIntent{}, err
	}
	intent.TargetGUID = target
	return intent, nil
}

func decodePermissionsWriteFields(values []json.RawMessage) (actionsecurity.PermissionsWriteIntent, error) {
	auth, ok := parseRolePermissionPositiveInt32(values[0])
	if !ok {
		return actionsecurity.PermissionsWriteIntent{}, ErrRolePermissionInvalidBody
	}
	permissions, ok := parseRolePermissionInt64(values[1], false)
	if !ok {
		return actionsecurity.PermissionsWriteIntent{}, ErrRolePermissionInvalidBody
	}
	catalog, ok := parseRolePermissionPositiveInt32(values[2])
	if !ok {
		return actionsecurity.PermissionsWriteIntent{}, ErrRolePermissionInvalidBody
	}
	overrides, ok := decodeRolePermissionOverrides(values[3])
	if !ok {
		return actionsecurity.PermissionsWriteIntent{}, ErrRolePermissionInvalidBody
	}
	reason, ok := decodeRolePermissionReason(values[4])
	if !ok {
		return actionsecurity.PermissionsWriteIntent{}, ErrRolePermissionInvalidBody
	}
	return actionsecurity.PermissionsWriteIntent{ExpectedAuthVersion: auth, ExpectedPermissionsVersion: permissions, CatalogVersion: catalog, Overrides: overrides, Reason: reason}, nil
}

func readRolePermissionBody(body io.Reader) ([]byte, error) {
	if body == nil {
		return nil, ErrRolePermissionInvalidBody
	}
	raw, err := io.ReadAll(io.LimitReader(body, RolePermissionBodyLimit+1))
	if err != nil {
		var max *http.MaxBytesError
		clear(raw)
		if errors.As(err, &max) {
			return nil, ErrRolePermissionBodyTooLarge
		}
		return nil, ErrRolePermissionInvalidBody
	}
	if int64(len(raw)) > RolePermissionBodyLimit {
		clear(raw)
		return nil, ErrRolePermissionBodyTooLarge
	}
	return raw, nil
}

func parseRolePermissionGUID(raw json.RawMessage) (int64, bool) {
	value, ok := decodeUserDeleteString(raw)
	if !ok {
		return 0, false
	}
	return parseCanonicalPositiveInt64(value)
}
func parseRolePermissionPositiveInt32(raw json.RawMessage) (int, bool) {
	value, ok := parseRolePermissionInt64(raw, false)
	return int(value), ok && value <= 2147483647
}
func parseRolePermissionInt64(raw json.RawMessage, allowZero bool) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	for i, b := range raw {
		if b < '0' || b > '9' || (i == 0 && len(raw) > 1 && b == '0') {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(string(raw), 10, 64)
	return value, err == nil && (value > 0 || allowZero && value == 0)
}
func decodeRolePermissionReason(raw json.RawMessage) (string, bool) {
	value, ok := decodeUserDeleteString(raw)
	if !ok {
		return "", false
	}
	return normalizeUserDeleteReason(value)
}

func decodeRolePermissionOverrides(raw json.RawMessage) ([]actionsecurity.PermissionOverrideIntent, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal(trimmed, &items) != nil {
		return nil, false
	}
	definitions := make(map[string]authz.Definition)
	for _, definition := range authz.Catalog() {
		definitions[definition.Name] = definition
	}
	seen := make(map[string]struct{}, len(items))
	result := make([]actionsecurity.PermissionOverrideIntent, 0, len(items))
	for _, item := range items {
		values, _, err := scanAdminUserCreateObject(item, []string{"capability", "effect"}, []string{"capability", "effect"})
		if err != nil {
			return nil, false
		}
		capability, ok := decodeUserDeleteString(values[0])
		if !ok {
			return nil, false
		}
		if _, duplicate := seen[capability]; duplicate {
			return nil, false
		}
		seen[capability] = struct{}{}
		definition, known := definitions[capability]
		if !known || definition.Unavailable {
			return nil, false
		}
		effect, ok := decodeUserDeleteString(values[1])
		if !ok {
			return nil, false
		}
		switch effect {
		case "allow":
			if definition.RootOnly || !definition.Grantable {
				return nil, false
			}
			result = append(result, actionsecurity.PermissionOverrideIntent{Capability: capability, Effect: 2})
		case "deny":
			result = append(result, actionsecurity.PermissionOverrideIntent{Capability: capability, Effect: 3})
		default:
			return nil, false
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Capability < result[j].Capability })
	return result, true
}
