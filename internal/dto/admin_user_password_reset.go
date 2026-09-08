package dto

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/porsche/ai-gateway-go/internal/service"
)

type AdminUserPasswordResetRequest struct {
	Action              string
	TargetGUID          int64
	ExpectedAuthVersion int
	NewPassword         []byte
	Reason              string
	CurrentPassword     []byte
}

type AdminUserPasswordResetResponse struct {
	OperationRef         string `json:"operation_ref"`
	TargetGUID           string `json:"target_guid"`
	ResultingAuthVersion int    `json:"resulting_auth_version"`
}

type AdminUserPasswordResetQueryResponse struct {
	OperationRef         string  `json:"operation_ref"`
	Scope                string  `json:"scope"`
	Status               string  `json:"status"`
	FinishedAt           *int64  `json:"finished_at"`
	FailureCode          *string `json:"failure_code"`
	TargetGUID           *string `json:"target_guid"`
	ResultingAuthVersion *int    `json:"resulting_auth_version"`
}

func (r AdminUserPasswordResetRequest) String() string   { return r.redacted() }
func (r AdminUserPasswordResetRequest) GoString() string { return r.redacted() }
func (r AdminUserPasswordResetRequest) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, r.redacted())
}
func (r AdminUserPasswordResetRequest) redacted() string {
	return fmt.Sprintf("AdminUserPasswordResetRequest{Action:%q TargetGUID:%d ExpectedAuthVersion:%d Reason:%q NewPassword:<redacted> CurrentPassword:<redacted>}", r.Action, r.TargetGUID, r.ExpectedAuthVersion, r.Reason)
}
func (r *AdminUserPasswordResetRequest) ClearSecrets() {
	if r == nil {
		return
	}
	clear(r.NewPassword)
	clear(r.CurrentPassword)
	r.NewPassword, r.CurrentPassword = nil, nil
}

func DecodeAdminUserPasswordResetIssue(body io.Reader) (AdminUserPasswordResetRequest, error) {
	raw, err := readUserDeleteBody(body)
	if err != nil {
		return AdminUserPasswordResetRequest{}, err
	}
	defer clear(raw)
	top, err := scanExactUserDeleteObject(raw, "action", "intent", "current_password")
	if err != nil {
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
	}
	action, ok := decodeUserDeleteString(top[0])
	if !ok || action != "users.reset_password" {
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInactiveAction
	}
	fields, err := scanExactUserDeleteObject(top[1], "target_guid", "expected_auth_version", "new_password", "reason")
	if err != nil {
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
	}
	request, err := decodePasswordResetFields(action, fields)
	if err != nil {
		return AdminUserPasswordResetRequest{}, err
	}
	current, ok := decodeOwnedUserDeleteJSONString(top[2])
	if !ok || len(current) == 0 {
		request.ClearSecrets()
		clear(current)
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
	}
	request.CurrentPassword = current
	return request, nil
}

func DecodeAdminUserPasswordResetExecute(body io.Reader) (AdminUserPasswordResetRequest, error) {
	raw, err := readUserDeleteBody(body)
	if err != nil {
		return AdminUserPasswordResetRequest{}, err
	}
	defer clear(raw)
	fields, err := scanExactUserDeleteObject(raw, "action", "expected_auth_version", "new_password", "reason")
	if err != nil {
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
	}
	action, ok := decodeUserDeleteString(fields[0])
	if !ok || action != "reset_password" {
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInactiveAction
	}
	return decodePasswordResetFields(action, fields[1:])
}

func decodePasswordResetFields(action string, fields []json.RawMessage) (AdminUserPasswordResetRequest, error) {
	if len(fields) != 4 && len(fields) != 3 {
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
	}
	offset := 0
	var target int64
	if len(fields) == 4 {
		raw, ok := decodeUserDeleteString(fields[0])
		if !ok {
			return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
		}
		var valid bool
		target, valid = parseCanonicalPositiveInt64(raw)
		if !valid {
			return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
		}
		offset = 1
	}
	version, ok := parseUserDeleteVersion(fields[offset])
	if !ok {
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
	}
	password, ok := decodeOwnedUserDeleteJSONString(fields[offset+1])
	if !ok || service.ValidatePasswordBytes(password) != nil {
		clear(password)
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
	}
	reasonRaw, ok := decodeUserDeleteString(fields[offset+2])
	if !ok {
		clear(password)
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
	}
	reason, ok := normalizeUserDeleteReason(reasonRaw)
	if !ok {
		clear(password)
		return AdminUserPasswordResetRequest{}, ErrUserDeleteInvalidBody
	}
	return AdminUserPasswordResetRequest{Action: action, TargetGUID: target, ExpectedAuthVersion: version, NewPassword: password, Reason: reason}, nil
}
