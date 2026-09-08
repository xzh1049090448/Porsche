package service

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func activeDeleteDescriptor(t *testing.T) actionsecurity.Descriptor {
	t.Helper()
	descriptor, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersDelete)
	if !ok {
		t.Fatal("users.delete descriptor is not active")
	}
	return descriptor
}

func TestValidateLockedDeleteIntentAcceptsExactFreshActiveTarget(t *testing.T) {
	target := &models.User{
		ID:          30,
		AuditFields: models.AuditFields{Guid: 3001},
		Role:        models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 4,
	}
	intent := actionsecurity.DeleteUserIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, Reason: "approved"}
	if err := validateLockedDeleteIntent(activeDeleteDescriptor(t), intent, target); err != nil {
		t.Fatalf("valid locked delete intent rejected: %v", err)
	}
}

func TestValidateLockedDeleteIntentRejectsDescriptorTypeBindingStateAndVersionAsFixedConflict(t *testing.T) {
	descriptor := activeDeleteDescriptor(t)
	target := models.User{
		ID:          30,
		AuditFields: models.AuditFields{Guid: 3001},
		Role:        models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 4,
	}
	validIntent := actionsecurity.DeleteUserIntent{TargetGUID: target.Guid, ExpectedAuthVersion: target.AuthVersion, Reason: "approved"}

	tests := []struct {
		name       string
		descriptor actionsecurity.Descriptor
		intent     any
		target     *models.User
	}{
		{name: "nil target", descriptor: descriptor, intent: validIntent},
		{name: "wrong intent type", descriptor: descriptor, intent: struct{}{}, target: &target},
		{name: "wrong action", descriptor: func() actionsecurity.Descriptor {
			value := descriptor
			value.Action = actionsecurity.ActionUsersDemote
			return value
		}(), intent: validIntent, target: &target},
		{name: "wrong name", descriptor: func() actionsecurity.Descriptor {
			value := descriptor
			value.Name = "users.delete.changed"
			return value
		}(), intent: validIntent, target: &target},
		{name: "wrong capability", descriptor: func() actionsecurity.Descriptor { value := descriptor; value.Capability = "users.edit"; return value }(), intent: validIntent, target: &target},
		{name: "wrong root only", descriptor: func() actionsecurity.Descriptor { value := descriptor; value.RootOnly = true; return value }(), intent: validIntent, target: &target},
		{name: "missing ticket", descriptor: func() actionsecurity.Descriptor { value := descriptor; value.RequiresTicket = false; return value }(), intent: validIntent, target: &target},
		{name: "inactive", descriptor: func() actionsecurity.Descriptor { value := descriptor; value.Active = false; return value }(), intent: validIntent, target: &target},
		{name: "wrong target kind", descriptor: func() actionsecurity.Descriptor {
			value := descriptor
			value.TargetKind = actionsecurity.TargetNone
			return value
		}(), intent: validIntent, target: &target},
		{name: "wrong encoder", descriptor: func() actionsecurity.Descriptor {
			value := descriptor
			value.Encode = func(any) ([]byte, error) { return []byte("changed"), nil }
			return value
		}(), intent: validIntent, target: &target},
		{name: "zero target id", descriptor: descriptor, intent: validIntent, target: func() *models.User { value := target; value.ID = 0; return &value }()},
		{name: "guid mismatch", descriptor: descriptor, intent: func() actionsecurity.DeleteUserIntent { value := validIntent; value.TargetGUID++; return value }(), target: &target},
		{name: "disabled target", descriptor: descriptor, intent: validIntent, target: func() *models.User { value := target; value.Status = models.UserStatusDisabled; return &value }()},
		{name: "unknown target state", descriptor: descriptor, intent: validIntent, target: func() *models.User { value := target; value.Status = models.UserStatus(99); return &value }()},
		{name: "deleted target", descriptor: descriptor, intent: validIntent, target: func() *models.User { value := target; value.IsDeleted = 1; return &value }()},
		{name: "zero expected version", descriptor: descriptor, intent: func() actionsecurity.DeleteUserIntent {
			value := validIntent
			value.ExpectedAuthVersion = 0
			return value
		}(), target: &target},
		{name: "stale expected version", descriptor: descriptor, intent: func() actionsecurity.DeleteUserIntent {
			value := validIntent
			value.ExpectedAuthVersion--
			return value
		}(), target: &target},
		{name: "zero stored version", descriptor: descriptor, intent: validIntent, target: func() *models.User { value := target; value.AuthVersion = 0; return &value }()},
		{name: "max stored version", descriptor: descriptor, intent: actionsecurity.DeleteUserIntent{TargetGUID: target.Guid, ExpectedAuthVersion: math.MaxInt32, Reason: "approved"}, target: func() *models.User { value := target; value.AuthVersion = math.MaxInt32; return &value }()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLockedDeleteIntent(tc.descriptor, tc.intent, tc.target)
			if !errors.Is(err, ErrActionVerificationConflict) {
				t.Fatalf("error = %v, want fixed conflict", err)
			}
			if err.Error() != ErrActionVerificationConflict.Error() {
				t.Fatalf("error text = %q, want %q", err.Error(), ErrActionVerificationConflict.Error())
			}
			status, message := StatusFromError(err)
			if status != 409 || message != "admin action verification conflict" {
				t.Fatalf("HTTP classification = %d/%q, want fixed 409 conflict", status, message)
			}
			for _, private := range []string{strconv.FormatInt(target.Guid, 10), strconv.Itoa(target.AuthVersion), target.Role.String(), "password"} {
				if private != "" && strings.Contains(err.Error(), private) {
					t.Fatalf("fixed conflict leaked target detail %q", private)
				}
			}
		})
	}
}
