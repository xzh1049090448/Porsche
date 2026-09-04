package actionsecurity

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestPermissionOverrideIntentFrozenTypeName(t *testing.T) {
	if got := reflect.TypeOf(PermissionOverrideIntent{}).Name(); got != "PermissionOverrideIntent" {
		t.Fatalf("permission override DTO type name = %q, want PermissionOverrideIntent", got)
	}
}

func descriptorFor(t *testing.T, action Action) Descriptor {
	t.Helper()
	for _, descriptor := range InactiveActionDescriptors() {
		if descriptor.Action == action {
			return descriptor
		}
	}
	t.Fatalf("descriptor %d missing", action)
	return Descriptor{}
}

func TestCreateAdminIntentCanonicalOrderNullRoleAndSortedModels(t *testing.T) {
	password := []byte{0x53, 0x33, 0x63, 0x72, 0x65, 0x74}
	models := []string{"model-z", "model-a", "model-z"}
	encoded, err := descriptorFor(t, ActionUsersCreateAdmin).Encode(CreateAdminIntent{
		Username: " Alice ", Password: password, PlanType: 1, AllowedModels: models, DailyCallLimit: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeFields(t, encoded)
	if len(fields) != 8 {
		t.Fatalf("field count=%d, want 8", len(fields))
	}
	for i, field := range fields {
		if field.tag != byte(i+1) {
			t.Fatalf("field[%d] tag=%d, want %d", i, field.tag, i+1)
		}
	}
	if string(fields[0].value) != " Alice " || fields[1].typ != typeNull || string(fields[3].value) != "admin" || fields[4].typ != typeNull {
		t.Fatalf("non-canonical identity/null/fixed role fields: %#v", fields)
	}
	if !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("caller password was not zeroed")
	}
	if !reflect.DeepEqual(models, []string{"model-z", "model-a", "model-z"}) {
		t.Fatalf("caller model slice mutated: %#v", models)
	}
	array := fields[6].value
	wantArray := []byte{0, 0, 0, 2, 0, 0, 0, 7, 'm', 'o', 'd', 'e', 'l', '-', 'a', 0, 0, 0, 7, 'm', 'o', 'd', 'e', 'l', '-', 'z'}
	if binary.BigEndian.Uint32(array[:4]) != 2 || !bytes.Equal(array, wantArray) {
		t.Fatalf("models array not sorted/deduplicated: %x", array)
	}
}

func TestPasswordZeroOnSuccessAndValidationError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		intent any
	}{
		{"create success", ActionUsersCreateAdmin, CreateAdminIntent{Username: "alice", Password: []byte{1, 2, 3}, PlanType: 1}},
		{"create error", ActionUsersCreateAdmin, CreateAdminIntent{Password: []byte{1, 2, 3}, PlanType: 1}},
		{"reset success", ActionUsersResetPassword, ResetPasswordIntent{TargetGUID: 1, NewPassword: []byte{4, 5, 6}, Reason: "requested"}},
		{"reset error", ActionUsersResetPassword, ResetPasswordIntent{NewPassword: []byte{4, 5, 6}, Reason: "requested"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var secret []byte
			switch in := tc.intent.(type) {
			case CreateAdminIntent:
				secret = in.Password
			case ResetPasswordIntent:
				secret = in.NewPassword
			}
			_, _ = descriptorFor(t, tc.action).Encode(tc.intent)
			if !bytes.Equal(secret, make([]byte, len(secret))) {
				t.Fatal("caller password was not zeroed")
			}
		})
	}
}

func TestPasswordEncodingIsIndependentAndCallerClearable(t *testing.T) {
	password := []byte{0x71, 0x52, 0x39, 0x21}
	wantPassword := append([]byte(nil), password...)
	encoded, err := descriptorFor(t, ActionUsersCreateAdmin).Encode(CreateAdminIntent{Username: "alice", Password: password, PlanType: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(password, make([]byte, len(password))) {
		t.Fatal("caller password was not cleared")
	}
	if !bytes.Contains(encoded, wantPassword) {
		t.Fatal("encoded HMAC input does not contain its independent password copy")
	}
	clear(encoded)
	if !bytes.Equal(encoded, make([]byte, len(encoded))) {
		t.Fatal("caller could not clear encoded HMAC input")
	}
}

func TestAllDescriptorsDeterministicAndDoNotMutateInputs(t *testing.T) {
	nickname := "Nick"
	group := int64(44)
	cases := []struct {
		name   string
		action Action
		make   func() any
	}{
		{"create", ActionUsersCreateAdmin, func() any {
			return CreateAdminIntent{Username: "alice", Nickname: &nickname, Password: []byte{1, 2, 3}, GroupGUID: &group, PlanType: 2, AllowedModels: []string{"z", "a", "z"}, DailyCallLimit: 4}
		}},
		{"reset", ActionUsersResetPassword, func() any { return ResetPasswordIntent{TargetGUID: 2, NewPassword: []byte{4, 5, 6}, Reason: "case"} }},
		{"promote", ActionUsersPromote, func() any { return RoleIntent{TargetGUID: 2, ExpectedAuthVersion: 3, Reason: "case"} }},
		{"demote", ActionUsersDemote, func() any { return RoleIntent{TargetGUID: 2, ExpectedAuthVersion: 3, Reason: "case"} }},
		{"permissions", ActionUsersPermissionsWrite, func() any {
			return PermissionsWriteIntent{TargetGUID: 2, ExpectedPermissionsVersion: 3, CatalogVersion: 1, Overrides: []PermissionOverrideIntent{{Capability: "z", Effect: 3}, {Capability: "a", Effect: 2}}}
		}},
		{"delete", ActionUsersDelete, func() any { return DeleteUserIntent{TargetGUID: 2, ExpectedAuthVersion: 3, Reason: "case"} }},
		{"publish", ActionPublicContentPublish, func() any {
			return PublishIntent{ContentType: "page", VersionGUID: 5, ExpectedBaseVersion: 4, Reason: "case"}
		}},
		{"rollback", ActionPublicContentRollback, func() any {
			return RollbackIntent{ContentType: "page", VersionGUID: 4, ExpectedCurrentVersion: 5, Reason: "case"}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			firstInput, secondInput := tc.make(), tc.make()
			firstBefore, secondBefore := cloneIntentForMutationCheck(firstInput), cloneIntentForMutationCheck(secondInput)
			first, err := descriptorFor(t, tc.action).Encode(firstInput)
			if err != nil {
				t.Fatal(err)
			}
			second, err := descriptorFor(t, tc.action).Encode(secondInput)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, second) {
				t.Fatal("equal intents encoded differently")
			}
			assertIntentMutationContract(t, firstInput, firstBefore)
			assertIntentMutationContract(t, secondInput, secondBefore)
			clear(first)
			clear(second)
		})
	}
}

func cloneIntentForMutationCheck(value any) any {
	switch in := value.(type) {
	case CreateAdminIntent:
		in.Password = append([]byte(nil), in.Password...)
		in.AllowedModels = append([]string(nil), in.AllowedModels...)
		return in
	case ResetPasswordIntent:
		in.NewPassword = append([]byte(nil), in.NewPassword...)
		return in
	case PermissionsWriteIntent:
		in.Overrides = append([]PermissionOverrideIntent(nil), in.Overrides...)
		return in
	default:
		return value
	}
}

func assertIntentMutationContract(t *testing.T, after, before any) {
	t.Helper()
	switch got := after.(type) {
	case CreateAdminIntent:
		want := before.(CreateAdminIntent)
		clear(want.Password)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("create input mutation = %#v, want only password clear", got)
		}
	case ResetPasswordIntent:
		want := before.(ResetPasswordIntent)
		clear(want.NewPassword)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("reset input mutation = %#v, want only password clear", got)
		}
	default:
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("input mutated: after=%#v before=%#v", after, before)
		}
	}
}

func TestUserIntentFixedRolesAndCanonicalValidation(t *testing.T) {
	tests := []struct {
		name   string
		action Action
		intent any
		role   string
	}{
		{"promote", ActionUsersPromote, RoleIntent{TargetGUID: 1, ExpectedAuthVersion: 2, Reason: "case"}, "admin"},
		{"demote", ActionUsersDemote, RoleIntent{TargetGUID: 1, ExpectedAuthVersion: 2, Reason: "case"}, "user"},
	}
	for _, tc := range tests {
		encoded, err := descriptorFor(t, tc.action).Encode(tc.intent)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		fields := decodeFields(t, encoded)
		if len(fields) != 4 || string(fields[2].value) != tc.role {
			t.Fatalf("%s fixed role fields=%#v", tc.name, fields)
		}
	}

	invalid := []struct {
		action Action
		intent any
	}{
		{ActionUsersCreateAdmin, CreateAdminIntent{Username: "", Password: []byte{1}, PlanType: 1}},
		{ActionUsersCreateAdmin, CreateAdminIntent{Username: "alice", Password: []byte{1}, PlanType: 0}},
		{ActionUsersCreateAdmin, CreateAdminIntent{Username: "alice", Password: []byte{1}, PlanType: 1, AllowedModels: []string{""}}},
		{ActionUsersCreateAdmin, CreateAdminIntent{Username: "alice", Password: []byte{1}, PlanType: 1, DailyCallLimit: -1}},
		{ActionUsersResetPassword, ResetPasswordIntent{TargetGUID: 0, NewPassword: []byte{1}, Reason: "case"}},
		{ActionUsersPromote, RoleIntent{TargetGUID: 1, ExpectedAuthVersion: 0, Reason: "case"}},
		{ActionUsersDelete, DeleteUserIntent{TargetGUID: 1, ExpectedAuthVersion: 1, Reason: ""}},
		{ActionUsersPermissionsWrite, PermissionsWriteIntent{TargetGUID: 1, ExpectedPermissionsVersion: 1, CatalogVersion: 1, Overrides: []PermissionOverrideIntent{{Capability: "users.read", Effect: 4}}}},
		{ActionUsersPermissionsWrite, PermissionsWriteIntent{TargetGUID: 1, ExpectedPermissionsVersion: 1, CatalogVersion: 1, Overrides: []PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.read", Effect: 3}}}},
	}
	for i, tc := range invalid {
		if _, err := descriptorFor(t, tc.action).Encode(tc.intent); err == nil {
			t.Errorf("invalid case %d accepted", i)
		}
	}
}

func TestPermissionsIntentSortsWithoutMutatingCaller(t *testing.T) {
	overrides := []PermissionOverrideIntent{{Capability: "users.reset_password", Effect: 3}, {Capability: "users.read", Effect: 2}}
	wantCaller := append([]PermissionOverrideIntent(nil), overrides...)
	encoded, err := descriptorFor(t, ActionUsersPermissionsWrite).Encode(PermissionsWriteIntent{TargetGUID: 1, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: overrides})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(overrides, wantCaller) {
		t.Fatalf("caller overrides mutated: %#v", overrides)
	}
	array := decodeFields(t, encoded)[3].value
	if bytes.Index(array, []byte("users.read")) > bytes.Index(array, []byte("users.reset_password")) {
		t.Fatalf("overrides not capability sorted: %x", array)
	}
}
