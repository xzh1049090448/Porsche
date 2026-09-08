package models

const PermissionCatalogVersion = 1

type PermissionPolicyHead struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	UserID         int64 `gorm:"type:bigint;not null;uniqueIndex" json:"-"`
	PolicyVersion  int64 `gorm:"type:bigint;not null" json:"-"`
	CatalogVersion int   `gorm:"type:int;not null" json:"-"`
	RuleCount      int   `gorm:"type:int;not null" json:"-"`
}

func (PermissionPolicyHead) TableName() string { return "user_permission_heads" }

type PermissionOverride struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	UserID        int64 `gorm:"type:bigint;not null;index" json:"-"`
	PolicyVersion int64 `gorm:"type:bigint;not null" json:"-"`
	Capability    int   `gorm:"type:int;not null" json:"-"`
	Effect        int   `gorm:"type:int;not null" json:"-"`
}

func (PermissionOverride) TableName() string { return "user_permission_overrides" }

var permissionCapabilityNames = map[int]string{1: "users.read", 2: "users.create", 3: "users.edit", 4: "users.enable", 5: "users.disable", 6: "users.reset_password", 7: "users.sessions.read", 8: "users.sessions.revoke", 9: "users.plan.change", 10: "users.group.change", 11: "users.quota.adjust", 12: "users.delete", 13: "users.deleted.read", 14: "users.promote", 15: "users.demote", 16: "users.permissions.write", 17: "users.audit.read", 18: "groups.read", 19: "groups.write", 20: "public_content.read", 21: "public_content.edit", 22: "public_content.preview", 23: "public_content.publish", 24: "public_content.rollback"}

func PermissionCapabilityName(code int) (string, bool) {
	name, ok := permissionCapabilityNames[code]
	return name, ok
}

func PermissionCapabilityCode(name string) (int, bool) {
	for code, value := range permissionCapabilityNames {
		if value == name {
			return code, true
		}
	}
	return 0, false
}
func PermissionEffectName(code int) (string, bool) {
	switch code {
	case 1:
		return "inherit", true
	case 2:
		return "allow", true
	case 3:
		return "deny", true
	}
	return "", false
}

func PermissionEffectCode(name string) (int, bool) {
	switch name {
	case "inherit":
		return 1, true
	case "allow":
		return 2, true
	case "deny":
		return 3, true
	}
	return 0, false
}
