package actionsecurity

import "errors"

var errWrongIntentType = errors.New("wrong intent type")

var canonicalActionDescriptors = [...]Descriptor{
	{ActionUsersCreateAdmin, "users.create_admin", "users.create", true, true, false, TargetNone, encodeCreateAdminAny},
	{ActionUsersResetPassword, "users.reset_password", "users.reset_password", false, true, false, TargetUser, encodeResetPasswordAny},
	{ActionUsersPromote, "users.promote", "users.promote", true, true, false, TargetUser, encodePromoteAny},
	{ActionUsersDemote, "users.demote", "users.demote", true, true, false, TargetUser, encodeDemoteAny},
	{ActionUsersPermissionsWrite, "users.permissions.write", "users.permissions.write", true, true, false, TargetUser, encodePermissionsWriteAny},
	{ActionUsersDelete, "users.delete", "users.delete", false, true, false, TargetUser, encodeDeleteUserAny},
	{ActionPublicContentPublish, "public_content.publish", "public_content.publish", false, true, false, TargetPublicContent, encodePublishAny},
	{ActionPublicContentRollback, "public_content.rollback", "public_content.rollback", false, true, false, TargetPublicContent, encodeRollbackAny},
	{ActionUsersCreate, "users.create", "users.create", false, false, false, TargetNone, encodeCreateAny},
}

var inactiveActionOrder = [...]Action{
	ActionUsersCreateAdmin,
	ActionUsersResetPassword,
	ActionUsersPromote,
	ActionUsersDemote,
	ActionUsersPermissionsWrite,
	ActionUsersDelete,
	ActionPublicContentPublish,
	ActionPublicContentRollback,
	ActionUsersCreate,
}

var futureActionOrder = [...]Action{ActionUsersCreate, ActionUsersCreateAdmin, ActionUsersDelete, ActionUsersResetPassword}
var activeActionOrder = [...]Action{ActionUsersCreate, ActionUsersCreateAdmin, ActionUsersDelete, ActionUsersResetPassword}

func InactiveActionDescriptors() []Descriptor {
	return projectActionDescriptors(inactiveActionOrder[:], nil)
}

func ActiveActionRegistry() []Descriptor {
	return projectActionDescriptors(activeActionOrder[:], activeActionOrder[:])
}

// FutureActionDescriptors returns the ordered activation candidate for the
// complete user-management bundle. It is not used for production resolution.
func FutureActionDescriptors() []Descriptor {
	return projectActionDescriptors(futureActionOrder[:], activeActionOrder[:])
}

func canonicalActionDescriptor(action Action) (Descriptor, bool) {
	for _, descriptor := range canonicalActionDescriptors {
		if descriptor.Action == action {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

func projectActionDescriptors(order []Action, activeActions []Action) []Descriptor {
	out := make([]Descriptor, 0, len(order))
	for _, action := range order {
		descriptor, ok := canonicalActionDescriptor(action)
		if !ok {
			continue
		}
		descriptor.Active = actionIn(activeActions, action)
		out = append(out, descriptor)
	}
	return out
}

func actionIn(actions []Action, action Action) bool {
	for _, candidate := range actions {
		if candidate == action {
			return true
		}
	}
	return false
}

func ResolveActiveAction(action Action) (Descriptor, bool) {
	for _, descriptor := range ActiveActionRegistry() {
		if descriptor.Action == action {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

func encodeCreateAny(value any) ([]byte, error) {
	intent, ok := value.(CreateAccountIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeCreateAccountIntent(intent, "user")
}

func encodeCreateAdminAny(value any) ([]byte, error) {
	intent, ok := value.(CreateAccountIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeCreateAccountIntent(intent, "admin")
}
func encodeResetPasswordAny(value any) ([]byte, error) {
	intent, ok := value.(ResetPasswordIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeResetPasswordIntent(intent)
}
func encodePromoteAny(value any) ([]byte, error) {
	intent, ok := value.(PromoteIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodePromoteIntent(intent)
}
func encodeDemoteAny(value any) ([]byte, error) {
	intent, ok := value.(DemoteIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeDemoteIntent(intent)
}
func encodePermissionsWriteAny(value any) ([]byte, error) {
	intent, ok := value.(PermissionsWriteIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodePermissionsWriteIntent(intent)
}
func encodeDeleteUserAny(value any) ([]byte, error) {
	intent, ok := value.(DeleteUserIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeDeleteUserIntent(intent)
}
func encodePublishAny(value any) ([]byte, error) {
	intent, ok := value.(PublishIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodePublishIntent(intent)
}
func encodeRollbackAny(value any) ([]byte, error) {
	intent, ok := value.(RollbackIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeRollbackIntent(intent)
}
