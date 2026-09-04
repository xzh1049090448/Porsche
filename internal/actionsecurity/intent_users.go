package actionsecurity

import (
	"errors"
	"math"
	"sort"
)

var errInvalidIntent = errors.New("invalid action intent")

type CreateAdminIntent struct {
	Username       string
	Nickname       *string
	Password       []byte
	GroupGUID      *int64
	PlanType       int
	AllowedModels  []string
	DailyCallLimit int
}
type ResetPasswordIntent struct {
	TargetGUID  int64
	NewPassword []byte
	Reason      string
}
type RoleIntent struct {
	TargetGUID          int64
	ExpectedAuthVersion int
	Reason              string
}
type PermissionOverrideIntent struct {
	Capability string
	Effect     int
}
type PermissionsWriteIntent struct {
	TargetGUID                 int64
	ExpectedPermissionsVersion int64
	CatalogVersion             int
	Overrides                  []PermissionOverrideIntent
}
type DeleteUserIntent struct {
	TargetGUID          int64
	ExpectedAuthVersion int
	Reason              string
}

func encodeCreateAdminIntent(intent CreateAdminIntent) ([]byte, error) {
	defer clear(intent.Password)
	if intent.Username == "" || len(intent.Password) == 0 || intent.PlanType < 1 || intent.PlanType > 3 || intent.DailyCallLimit < 0 || intent.DailyCallLimit > math.MaxInt32 ||
		(intent.Nickname != nil && *intent.Nickname == "") || (intent.GroupGUID != nil && *intent.GroupGUID <= 0) {
		return nil, errInvalidIntent
	}
	models := append([]string(nil), intent.AllowedModels...)
	for _, model := range models {
		if model == "" {
			return nil, errInvalidIntent
		}
	}
	sort.Strings(models)
	unique := models[:0]
	for _, model := range models {
		if len(unique) == 0 || unique[len(unique)-1] != model {
			unique = append(unique, model)
		}
	}
	items := make([][]byte, len(unique))
	for i := range unique {
		items[i] = []byte(unique[i])
	}
	var w intentWriter
	w.fieldString(1, intent.Username)
	w.fieldNullableString(2, intent.Nickname)
	w.fieldBytes(3, intent.Password)
	w.fieldString(4, "admin")
	if intent.GroupGUID == nil {
		w.field(5, typeNull, nil)
	} else {
		w.fieldInt64(5, *intent.GroupGUID)
	}
	w.fieldInt32(6, intent.PlanType)
	w.fieldArray(7, items)
	w.fieldInt32(8, intent.DailyCallLimit)
	return w.bytes(), nil
}

func encodeResetPasswordIntent(intent ResetPasswordIntent) ([]byte, error) {
	defer clear(intent.NewPassword)
	if intent.TargetGUID <= 0 || len(intent.NewPassword) == 0 || intent.Reason == "" {
		return nil, errInvalidIntent
	}
	var w intentWriter
	w.fieldInt64(1, intent.TargetGUID)
	w.fieldBytes(2, intent.NewPassword)
	w.fieldString(3, intent.Reason)
	return w.bytes(), nil
}

func encodeRoleIntent(intent RoleIntent, role string) ([]byte, error) {
	if intent.TargetGUID <= 0 || intent.ExpectedAuthVersion <= 0 || intent.ExpectedAuthVersion > math.MaxInt32 || intent.Reason == "" {
		return nil, errInvalidIntent
	}
	var w intentWriter
	w.fieldInt64(1, intent.TargetGUID)
	w.fieldInt32(2, intent.ExpectedAuthVersion)
	w.fieldString(3, role)
	w.fieldString(4, intent.Reason)
	return w.bytes(), nil
}

func encodePermissionsWriteIntent(intent PermissionsWriteIntent) ([]byte, error) {
	if intent.TargetGUID <= 0 || intent.ExpectedPermissionsVersion <= 0 || intent.CatalogVersion <= 0 || intent.CatalogVersion > math.MaxInt32 {
		return nil, errInvalidIntent
	}
	overrides := append([]PermissionOverrideIntent(nil), intent.Overrides...)
	for _, override := range overrides {
		if override.Capability == "" || override.Effect < 1 || override.Effect > 3 {
			return nil, errInvalidIntent
		}
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Capability < overrides[j].Capability })
	items := make([][]byte, len(overrides))
	for i, override := range overrides {
		if i > 0 && overrides[i-1].Capability == override.Capability {
			return nil, errInvalidIntent
		}
		var item intentWriter
		item.fieldString(1, override.Capability)
		item.fieldInt32(2, override.Effect)
		items[i] = item.bytes()
	}
	var w intentWriter
	w.fieldInt64(1, intent.TargetGUID)
	w.fieldInt64(2, intent.ExpectedPermissionsVersion)
	w.fieldInt32(3, intent.CatalogVersion)
	w.fieldArray(4, items)
	return w.bytes(), nil
}

func encodeDeleteUserIntent(intent DeleteUserIntent) ([]byte, error) {
	if intent.TargetGUID <= 0 || intent.ExpectedAuthVersion <= 0 || intent.ExpectedAuthVersion > math.MaxInt32 || intent.Reason == "" {
		return nil, errInvalidIntent
	}
	var w intentWriter
	w.fieldInt64(1, intent.TargetGUID)
	w.fieldInt32(2, intent.ExpectedAuthVersion)
	w.fieldString(3, intent.Reason)
	return w.bytes(), nil
}
