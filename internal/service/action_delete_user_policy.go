package service

import (
	"math"
	"reflect"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
)

// validateLockedDeleteIntent classifies visible target drift as a fixed 409.
// Authorization and tombstone visibility are decided before this check by the
// locked identity evaluator; this function only accepts the reviewed active
// users.delete descriptor and the target snapshot locked by that transaction.
func validateLockedDeleteIntent(descriptor actionsecurity.Descriptor, value any, target *models.User) error {
	expected, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersDelete)
	if !ok || descriptor.Action != expected.Action || descriptor.Name != expected.Name ||
		descriptor.Capability != expected.Capability || descriptor.RootOnly != expected.RootOnly ||
		descriptor.RequiresTicket != expected.RequiresTicket || descriptor.Active != expected.Active ||
		descriptor.TargetKind != expected.TargetKind || descriptor.Encode == nil || expected.Encode == nil ||
		reflect.ValueOf(descriptor.Encode).Pointer() != reflect.ValueOf(expected.Encode).Pointer() {
		return ErrActionVerificationConflict
	}
	intent, ok := value.(actionsecurity.DeleteUserIntent)
	if !ok || target == nil || target.ID <= 0 || target.Guid <= 0 || target.IsDeleted != 0 ||
		target.Status != models.UserStatusActive || target.AuthVersion <= 0 || target.AuthVersion >= math.MaxInt32 ||
		intent.TargetGUID != target.Guid || intent.ExpectedAuthVersion <= 0 || intent.ExpectedAuthVersion != target.AuthVersion {
		return ErrActionVerificationConflict
	}
	return nil
}
