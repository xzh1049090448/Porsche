package actionsecurity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

var errInvalidIntent = errors.New("invalid action intent")

type CreateAccountIntent struct {
	Username       string
	Nickname       *string
	Password       []byte
	Role           string
	GroupGUID      *int64
	PlanType       int
	AllowedModels  []string
	DailyCallLimit int
	Overrides      []PermissionOverrideIntent
}
type ResetPasswordIntent struct {
	TargetGUID          int64
	ExpectedAuthVersion int
	NewPassword         []byte
	Reason              string
}

func (i ResetPasswordIntent) String() string {
	return fmt.Sprintf("ResetPasswordIntent{TargetGUID:%d ExpectedAuthVersion:%d NewPassword:<redacted> Reason:%q}", i.TargetGUID, i.ExpectedAuthVersion, i.Reason)
}
func (i ResetPasswordIntent) GoString() string { return i.String() }
func (i ResetPasswordIntent) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, i.String())
}
func (ResetPasswordIntent) MarshalJSON() ([]byte, error) { return json.Marshal(struct{}{}) }

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

func encodeCreateAccountIntent(intent CreateAccountIntent, role string) ([]byte, error) {
	defer clear(intent.Password)
	if intent.Username == "" || !utf8.ValidString(intent.Username) || len(intent.Password) == 0 || intent.Role != role || intent.PlanType < 1 || intent.PlanType > 3 || intent.DailyCallLimit < 0 || intent.DailyCallLimit > math.MaxInt32 ||
		(intent.Nickname != nil && (*intent.Nickname == "" || !utf8.ValidString(*intent.Nickname))) || (intent.GroupGUID != nil && *intent.GroupGUID <= 0) {
		return nil, errInvalidIntent
	}
	if _, err := checkedU32Length(uint64(len(intent.Username))); err != nil {
		return nil, err
	}
	if _, err := checkedU32Length(uint64(len(intent.Password))); err != nil {
		return nil, err
	}
	if intent.Nickname != nil {
		if _, err := checkedU32Length(uint64(len(*intent.Nickname))); err != nil {
			return nil, err
		}
	}
	if err := checkedStringArrayPayloadLength(intent.AllowedModels); err != nil {
		return nil, err
	}
	models := append([]string(nil), intent.AllowedModels...)
	for _, model := range models {
		if model == "" || !utf8.ValidString(model) {
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
	capabilityLengths := make([]uint64, len(intent.Overrides))
	for i, override := range intent.Overrides {
		capabilityLengths[i] = uint64(len(override.Capability))
	}
	if err := checkedCreateOverrideArrayPayloadLength(uint64(len(intent.Overrides)), capabilityLengths); err != nil {
		return nil, err
	}
	overrides := append([]PermissionOverrideIntent(nil), intent.Overrides...)
	for _, override := range overrides {
		if override.Capability == "" || !utf8.ValidString(override.Capability) || (override.Effect != 2 && override.Effect != 3) {
			return nil, errInvalidIntent
		}
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Capability < overrides[j].Capability })
	overrideItems := make([][]byte, len(overrides))
	for i, override := range overrides {
		if i > 0 && overrides[i-1].Capability == override.Capability {
			return nil, errInvalidIntent
		}
		item, err := encodeIntent(func(w *intentWriter) error {
			if err := w.fieldString(1, override.Capability); err != nil {
				return err
			}
			return w.fieldInt32(2, override.Effect)
		})
		if err != nil {
			return nil, err
		}
		overrideItems[i] = item
	}
	defer func() {
		for _, item := range overrideItems {
			clear(item)
		}
	}()
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldString(1, intent.Username); err != nil {
			return err
		}
		if err := w.fieldNullableString(2, intent.Nickname); err != nil {
			return err
		}
		if err := w.fieldBytes(3, intent.Password); err != nil {
			return err
		}
		if err := w.fieldString(4, intent.Role); err != nil {
			return err
		}
		if intent.GroupGUID == nil {
			if err := w.field(5, typeNull, nil); err != nil {
				return err
			}
		} else if err := w.fieldInt64(5, *intent.GroupGUID); err != nil {
			return err
		}
		if err := w.fieldInt32(6, intent.PlanType); err != nil {
			return err
		}
		if err := w.fieldArray(7, items); err != nil {
			return err
		}
		if err := w.fieldInt32(8, intent.DailyCallLimit); err != nil {
			return err
		}
		return w.fieldArray(9, overrideItems)
	})
}

func checkedCreateOverrideArrayPayloadLength(count uint64, capabilityLengths []uint64) error {
	if count != uint64(len(capabilityLengths)) {
		return errIntentEncodingTooLarge
	}
	itemLengths := make([]uint64, len(capabilityLengths))
	for i, capabilityLength := range capabilityLengths {
		if capabilityLength > math.MaxUint64-16 {
			return errIntentEncodingTooLarge
		}
		itemLengths[i] = capabilityLength + 16 // capability field plus fixed effect field
	}
	_, err := checkedArrayPayloadLength(count, itemLengths)
	return err
}

func encodeResetPasswordIntent(intent ResetPasswordIntent) ([]byte, error) {
	defer clear(intent.NewPassword)
	if intent.TargetGUID <= 0 || intent.ExpectedAuthVersion <= 0 || intent.ExpectedAuthVersion > math.MaxInt32 || len(intent.NewPassword) == 0 || intent.Reason == "" {
		return nil, errInvalidIntent
	}
	if _, err := checkedU32Length(uint64(len(intent.NewPassword))); err != nil {
		return nil, err
	}
	if _, err := checkedU32Length(uint64(len(intent.Reason))); err != nil {
		return nil, err
	}
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldInt64(1, intent.TargetGUID); err != nil {
			return err
		}
		if err := w.fieldInt32(2, intent.ExpectedAuthVersion); err != nil {
			return err
		}
		if err := w.fieldBytes(3, intent.NewPassword); err != nil {
			return err
		}
		return w.fieldString(4, intent.Reason)
	})
}

func encodeRoleIntent(intent RoleIntent, role string) ([]byte, error) {
	if intent.TargetGUID <= 0 || intent.ExpectedAuthVersion <= 0 || intent.ExpectedAuthVersion > math.MaxInt32 || intent.Reason == "" {
		return nil, errInvalidIntent
	}
	if _, err := checkedU32Length(uint64(len(intent.Reason))); err != nil {
		return nil, err
	}
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldInt64(1, intent.TargetGUID); err != nil {
			return err
		}
		if err := w.fieldInt32(2, intent.ExpectedAuthVersion); err != nil {
			return err
		}
		if err := w.fieldString(3, role); err != nil {
			return err
		}
		return w.fieldString(4, intent.Reason)
	})
}

func encodePermissionsWriteIntent(intent PermissionsWriteIntent) ([]byte, error) {
	if intent.TargetGUID <= 0 || intent.ExpectedPermissionsVersion <= 0 || intent.CatalogVersion <= 0 || intent.CatalogVersion > math.MaxInt32 {
		return nil, errInvalidIntent
	}
	if _, err := checkedU32Length(uint64(len(intent.Overrides))); err != nil {
		return nil, err
	}
	arrayLength := uint64(4)
	for _, override := range intent.Overrides {
		if _, err := checkedU32Length(uint64(len(override.Capability))); err != nil {
			return nil, err
		}
		var err error
		arrayLength, err = addArrayItemLength(arrayLength, uint64(len(override.Capability))+16)
		if err != nil {
			return nil, err
		}
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
		item, err := encodeIntent(func(w *intentWriter) error {
			if err := w.fieldString(1, override.Capability); err != nil {
				return err
			}
			return w.fieldInt32(2, override.Effect)
		})
		if err != nil {
			return nil, err
		}
		items[i] = item
	}
	defer func() {
		for _, item := range items {
			clear(item)
		}
	}()
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldInt64(1, intent.TargetGUID); err != nil {
			return err
		}
		if err := w.fieldInt64(2, intent.ExpectedPermissionsVersion); err != nil {
			return err
		}
		if err := w.fieldInt32(3, intent.CatalogVersion); err != nil {
			return err
		}
		return w.fieldArray(4, items)
	})
}

func encodeDeleteUserIntent(intent DeleteUserIntent) ([]byte, error) {
	reason, ok := normalizeDeleteUserReason(intent.Reason)
	if intent.TargetGUID <= 0 || intent.ExpectedAuthVersion <= 0 || intent.ExpectedAuthVersion > math.MaxInt32 || !ok {
		return nil, errInvalidIntent
	}
	if _, err := checkedU32Length(uint64(len(reason))); err != nil {
		return nil, err
	}
	return encodeIntent(func(w *intentWriter) error {
		if err := w.fieldInt64(1, intent.TargetGUID); err != nil {
			return err
		}
		if err := w.fieldInt32(2, intent.ExpectedAuthVersion); err != nil {
			return err
		}
		return w.fieldString(3, reason)
	})
}

func normalizeDeleteUserReason(raw string) (string, bool) {
	normalized := strings.TrimSpace(raw)
	count := utf8.RuneCountInString(normalized)
	return normalized, utf8.ValidString(normalized) && count >= 1 && count <= 200
}
