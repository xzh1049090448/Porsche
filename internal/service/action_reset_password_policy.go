package service

import (
	"math"
	"reflect"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func validateLockedResetPasswordIntent(descriptor actionsecurity.Descriptor, value any, target *models.User) error {
	expected, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersResetPassword)
	if !ok || descriptor.Action != expected.Action || descriptor.Name != expected.Name ||
		descriptor.Capability != expected.Capability || descriptor.RootOnly != expected.RootOnly ||
		descriptor.RequiresTicket != expected.RequiresTicket || descriptor.Active != expected.Active ||
		descriptor.TargetKind != expected.TargetKind || descriptor.Encode == nil || expected.Encode == nil ||
		reflect.ValueOf(descriptor.Encode).Pointer() != reflect.ValueOf(expected.Encode).Pointer() {
		return ErrActionVerificationConflict
	}
	intent, ok := value.(actionsecurity.ResetPasswordIntent)
	if !ok || target == nil || target.ID <= 0 || target.Guid <= 0 || target.IsDeleted != 0 ||
		(target.Status != models.UserStatusActive && target.Status != models.UserStatusDisabled) ||
		target.AuthVersion <= 0 || target.AuthVersion >= math.MaxInt32 || intent.TargetGUID != target.Guid ||
		intent.ExpectedAuthVersion != target.AuthVersion || ValidatePasswordBytes(intent.NewPassword) != nil {
		return ErrActionVerificationConflict
	}
	return nil
}
