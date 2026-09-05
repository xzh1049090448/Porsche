package actionsecurity

import "errors"

var errWrongIntentType = errors.New("wrong intent type")

var inactiveActionDescriptors = [...]Descriptor{
	{ActionUsersCreateAdmin, "users.create_admin", "users.create", true, true, false, TargetNone, encodeCreateAdminAny},
	{ActionUsersResetPassword, "users.reset_password", "users.reset_password", false, true, false, TargetUser, encodeResetPasswordAny},
	{ActionUsersPromote, "users.promote", "users.promote", true, true, false, TargetUser, encodePromoteAny},
	{ActionUsersDemote, "users.demote", "users.demote", true, true, false, TargetUser, encodeDemoteAny},
	{ActionUsersPermissionsWrite, "users.permissions.write", "users.permissions.write", true, true, false, TargetUser, encodePermissionsWriteAny},
	{ActionUsersDelete, "users.delete", "users.delete", false, true, false, TargetUser, encodeDeleteUserAny},
	{ActionPublicContentPublish, "public_content.publish", "public_content.publish", false, true, false, TargetPublicContent, encodePublishAny},
	{ActionPublicContentRollback, "public_content.rollback", "public_content.rollback", false, true, false, TargetPublicContent, encodeRollbackAny},
}

func InactiveActionDescriptors() []Descriptor {
	out := make([]Descriptor, len(inactiveActionDescriptors))
	copy(out, inactiveActionDescriptors[:])
	return out
}

func ActiveActionRegistry() []Descriptor {
	out := make([]Descriptor, 0, 1)
	for _, descriptor := range inactiveActionDescriptors {
		if descriptor.Action == ActionUsersDelete {
			descriptor.Active = true
			out = append(out, descriptor)
			break
		}
	}
	return out
}

func ResolveActiveAction(action Action) (Descriptor, bool) {
	for _, descriptor := range ActiveActionRegistry() {
		if descriptor.Action == action {
			return descriptor, true
		}
	}
	return Descriptor{}, false
}

func encodeCreateAdminAny(value any) ([]byte, error) {
	intent, ok := value.(CreateAdminIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeCreateAdminIntent(intent)
}
func encodeResetPasswordAny(value any) ([]byte, error) {
	intent, ok := value.(ResetPasswordIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeResetPasswordIntent(intent)
}
func encodePromoteAny(value any) ([]byte, error) {
	intent, ok := value.(RoleIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeRoleIntent(intent, "admin")
}
func encodeDemoteAny(value any) ([]byte, error) {
	intent, ok := value.(RoleIntent)
	if !ok {
		return nil, errWrongIntentType
	}
	return encodeRoleIntent(intent, "user")
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
