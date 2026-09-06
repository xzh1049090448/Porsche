package actionsecurity

import (
	"bytes"
	"encoding/binary"
	"math"
	"reflect"
	"strings"
	"testing"
)

func decodeArrayItems(t *testing.T, encoded []byte) [][]byte {
	t.Helper()
	if len(encoded) < 4 {
		t.Fatalf("truncated array count: %x", encoded)
	}
	count := int(binary.BigEndian.Uint32(encoded[:4]))
	encoded = encoded[4:]
	items := make([][]byte, count)
	for i := range items {
		if len(encoded) < 4 {
			t.Fatalf("truncated array item length at %d: %x", i, encoded)
		}
		length := int(binary.BigEndian.Uint32(encoded[:4]))
		encoded = encoded[4:]
		if len(encoded) < length {
			t.Fatalf("truncated array item %d: length=%d available=%d", i, length, len(encoded))
		}
		items[i] = append([]byte(nil), encoded[:length]...)
		encoded = encoded[length:]
	}
	if len(encoded) != 0 {
		t.Fatalf("trailing array bytes: %x", encoded)
	}
	return items
}

func TestPermissionOverrideIntentFrozenTypeName(t *testing.T) {
	if got := reflect.TypeOf(PermissionOverrideIntent{}).Name(); got != "PermissionOverrideIntent" {
		t.Fatalf("permission override DTO type name = %q, want PermissionOverrideIntent", got)
	}
}

func descriptorFor(t *testing.T, action Action) Descriptor {
	t.Helper()
	for _, descriptors := range [][]Descriptor{InactiveActionDescriptors(), FutureActionDescriptors(), ActiveActionRegistry()} {
		for _, descriptor := range descriptors {
			if descriptor.Action == action {
				return descriptor
			}
		}
	}
	t.Fatalf("descriptor %d missing", action)
	return Descriptor{}
}

func TestCreateAccountIntentCanonicalOrderNullRoleSortedModelsAndOverrides(t *testing.T) {
	password := []byte{0x53, 0x33, 0x63, 0x72, 0x65, 0x74}
	models := []string{"model-z", "model-a", "model-z"}
	overrides := []PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.write", Effect: 3}}
	encoded, err := descriptorFor(t, ActionUsersCreateAdmin).Encode(CreateAccountIntent{
		Username: " Alice ", Password: password, Role: "admin", PlanType: 1, AllowedModels: models, DailyCallLimit: 7, Overrides: overrides,
	})
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeFields(t, encoded)
	if len(fields) != 9 {
		t.Fatalf("field count=%d, want 9", len(fields))
	}
	for i, field := range fields {
		if field.tag != byte(i+1) {
			t.Fatalf("field[%d] tag=%d, want %d", i, field.tag, i+1)
		}
	}
	wantTypes := []byte{typeString, typeNull, typeBytes, typeString, typeNull, typeInt32, typeArray, typeInt32, typeArray}
	for i, wantType := range wantTypes {
		if fields[i].typ != wantType {
			t.Fatalf("field[%d] type=%d, want %d", i, fields[i].typ, wantType)
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
	overrideItems := decodeArrayItems(t, fields[8].value)
	if len(overrideItems) != 2 {
		t.Fatalf("override count=%d, want 2", len(overrideItems))
	}
	for i, want := range []struct {
		capability string
		effect     int
	}{{"users.read", 2}, {"users.write", 3}} {
		itemFields := decodeFields(t, overrideItems[i])
		if len(itemFields) != 2 || itemFields[0].tag != 1 || itemFields[0].typ != typeString || string(itemFields[0].value) != want.capability ||
			itemFields[1].tag != 2 || itemFields[1].typ != typeInt32 || int(binary.BigEndian.Uint32(itemFields[1].value)) != want.effect {
			t.Fatalf("override[%d] fields=%#v, want capability/effect %q/%d", i, itemFields, want.capability, want.effect)
		}
	}
	if !reflect.DeepEqual(overrides, []PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.write", Effect: 3}}) {
		t.Fatalf("caller overrides mutated: %#v", overrides)
	}
}

func TestCreateAccountIntentBindsAllFieldsAndCanonicalizesEquivalentPermutations(t *testing.T) {
	nickname := "Nick"
	group := int64(44)
	makeIntent := func(models []string, overrides []PermissionOverrideIntent) CreateAccountIntent {
		return CreateAccountIntent{
			Username: "alice", Nickname: &nickname, Password: []byte{1, 2, 3}, Role: "user", GroupGUID: &group,
			PlanType: 2, AllowedModels: models, DailyCallLimit: 4, Overrides: overrides,
		}
	}
	first := makeIntent([]string{"z", "a", "z"}, []PermissionOverrideIntent{{Capability: "users.write", Effect: 3}, {Capability: "users.read", Effect: 2}})
	second := makeIntent([]string{"a", "z"}, []PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.write", Effect: 3}})
	firstEncoded, err := descriptorFor(t, ActionUsersCreate).Encode(first)
	if err != nil {
		t.Fatal(err)
	}
	secondEncoded, err := descriptorFor(t, ActionUsersCreate).Encode(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstEncoded, secondEncoded) {
		t.Fatalf("equivalent create intents encoded differently: %x vs %x", firstEncoded, secondEncoded)
	}
	admin := makeIntent([]string{"a", "z"}, []PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.write", Effect: 3}})
	admin.Role = "admin"
	adminEncoded, err := descriptorFor(t, ActionUsersCreateAdmin).Encode(admin)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstEncoded, adminEncoded) {
		t.Fatal("role did not affect canonical encoding")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*CreateAccountIntent)
	}{
		{"nickname", func(in *CreateAccountIntent) { value := "Other"; in.Nickname = &value }},
		{"username", func(in *CreateAccountIntent) { in.Username = "other" }},
		{"password", func(in *CreateAccountIntent) { in.Password = []byte{4, 5, 6} }},
		{"group", func(in *CreateAccountIntent) { value := int64(45); in.GroupGUID = &value }},
		{"plan", func(in *CreateAccountIntent) { in.PlanType = 3 }},
		{"models", func(in *CreateAccountIntent) { in.AllowedModels = []string{"other"} }},
		{"daily limit", func(in *CreateAccountIntent) { in.DailyCallLimit = 5 }},
		{"overrides", func(in *CreateAccountIntent) {
			in.Overrides = []PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := makeIntent([]string{"a", "z"}, []PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.write", Effect: 3}})
			tc.mutate(&candidate)
			got, err := descriptorFor(t, ActionUsersCreate).Encode(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(firstEncoded, got) {
				t.Fatal("approved field did not affect canonical encoding")
			}
		})
	}
}

func TestCreateAccountOverridePayloadBounds(t *testing.T) {
	if err := checkedCreateOverrideArrayPayloadLength(1, []uint64{uint64(math.MaxUint32) - 24}); err != nil {
		t.Fatalf("exact maximum override payload rejected: %v", err)
	}
	for _, tc := range []struct {
		name              string
		count             uint64
		capabilityLengths []uint64
	}{
		{"payload max plus one", 1, []uint64{uint64(math.MaxUint32) - 23}},
		{"capability item overflow", 1, []uint64{math.MaxUint64}},
		{"count overflow", uint64(math.MaxUint32) + 1, nil},
		{"count mismatch", 2, []uint64{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkedCreateOverrideArrayPayloadLength(tc.count, tc.capabilityLengths); err == nil {
				t.Fatal("invalid create override payload accepted")
			}
		})
	}
}

func TestPasswordZeroOnSuccessAndValidationError(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action Action
		intent any
	}{
		{"create success", ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1, 2, 3}, Role: "admin", PlanType: 1}},
		{"create error", ActionUsersCreateAdmin, CreateAccountIntent{Password: []byte{1, 2, 3}, Role: "admin", PlanType: 1}},
		{"reset success", ActionUsersResetPassword, ResetPasswordIntent{TargetGUID: 1, NewPassword: []byte{4, 5, 6}, Reason: "requested"}},
		{"reset error", ActionUsersResetPassword, ResetPasswordIntent{NewPassword: []byte{4, 5, 6}, Reason: "requested"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var secret []byte
			switch in := tc.intent.(type) {
			case CreateAccountIntent:
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
	encoded, err := descriptorFor(t, ActionUsersCreateAdmin).Encode(CreateAccountIntent{Username: "alice", Password: password, Role: "admin", PlanType: 1})
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

func TestCreateAccountIntentOnlyClearsItsPasswordOnSuccessAndError(t *testing.T) {
	nickname := "Nick"
	group := int64(44)
	for _, tc := range []struct {
		name   string
		action Action
		role   string
	}{
		{"success", ActionUsersCreate, "user"},
		{"rejected role", ActionUsersCreate, "admin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			models := []string{"z", "a", "z"}
			overrides := []PermissionOverrideIntent{{Capability: "users.write", Effect: 3}, {Capability: "users.read", Effect: 2}}
			intent := CreateAccountIntent{
				Username: "alice", Nickname: &nickname, Password: []byte{1, 2, 3}, Role: tc.role, GroupGUID: &group,
				PlanType: 2, AllowedModels: models, DailyCallLimit: 4, Overrides: overrides,
			}
			want := cloneIntentForMutationCheck(intent).(CreateAccountIntent)
			_, _ = descriptorFor(t, tc.action).Encode(intent)
			clear(want.Password)
			if !reflect.DeepEqual(intent, want) {
				t.Fatalf("create input mutation = %#v, want only password clear", intent)
			}
		})
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
			return CreateAccountIntent{Username: "alice", Nickname: &nickname, Password: []byte{1, 2, 3}, Role: "admin", GroupGUID: &group, PlanType: 2, AllowedModels: []string{"z", "a", "z"}, DailyCallLimit: 4, Overrides: []PermissionOverrideIntent{{Capability: "users.write", Effect: 3}, {Capability: "users.read", Effect: 2}}}
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
	case CreateAccountIntent:
		in.Password = append([]byte(nil), in.Password...)
		in.AllowedModels = append([]string(nil), in.AllowedModels...)
		in.Overrides = append([]PermissionOverrideIntent(nil), in.Overrides...)
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
	case CreateAccountIntent:
		want := before.(CreateAccountIntent)
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
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "", Password: []byte{1}, Role: "admin", PlanType: 1}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 0}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1, AllowedModels: []string{""}}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1, DailyCallLimit: -1}},
		{ActionUsersCreate, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "user", PlanType: 1}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1, Overrides: []PermissionOverrideIntent{{Capability: "", Effect: 2}}}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1, Overrides: []PermissionOverrideIntent{{Capability: "\xff", Effect: 2}}}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1, Overrides: []PermissionOverrideIntent{{Capability: "users.read", Effect: 1}}}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1, Overrides: []PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.read", Effect: 3}}}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1, AllowedModels: []string{"\xff"}}},
		{ActionUsersCreateAdmin, CreateAccountIntent{Username: "\xff", Password: []byte{1}, Role: "admin", PlanType: 1}},
		{ActionUsersResetPassword, ResetPasswordIntent{TargetGUID: 0, NewPassword: []byte{1}, Reason: "case"}},
		{ActionUsersPromote, RoleIntent{TargetGUID: 1, ExpectedAuthVersion: 0, Reason: "case"}},
		{ActionUsersDelete, DeleteUserIntent{TargetGUID: 0, ExpectedAuthVersion: 1, Reason: "case"}},
		{ActionUsersDelete, DeleteUserIntent{TargetGUID: 1, ExpectedAuthVersion: 0, Reason: "case"}},
		{ActionUsersDelete, DeleteUserIntent{TargetGUID: 1, ExpectedAuthVersion: 2147483648, Reason: "case"}},
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

func TestDeleteUserIntentNormalizesReasonAndUsesUnicodeCodePointLimit(t *testing.T) {
	const middle = "keep  internal\ttext"
	encoded, err := descriptorFor(t, ActionUsersDelete).Encode(DeleteUserIntent{
		TargetGUID: 2, ExpectedAuthVersion: 2147483647, Reason: "\u2003" + middle + "\u00a0",
	})
	if err != nil {
		t.Fatal(err)
	}
	fields := decodeFields(t, encoded)
	if len(fields) != 3 || string(fields[2].value) != middle {
		t.Fatalf("normalized delete reason = %#v, want %q", fields, middle)
	}

	for _, reason := range []string{"", "\u2003\u00a0", strings.Repeat("界", 201)} {
		if _, err := descriptorFor(t, ActionUsersDelete).Encode(DeleteUserIntent{TargetGUID: 1, ExpectedAuthVersion: 1, Reason: reason}); err == nil {
			t.Fatalf("invalid delete reason %q accepted", reason)
		}
	}
}

func TestDeleteUserIntentCanonicalEncodingIgnoresOnlyEdgeWhitespace(t *testing.T) {
	base := DeleteUserIntent{TargetGUID: 9, ExpectedAuthVersion: 3, Reason: "reason"}
	trimmed, err := descriptorFor(t, ActionUsersDelete).Encode(base)
	if err != nil {
		t.Fatal(err)
	}
	spaced, err := descriptorFor(t, ActionUsersDelete).Encode(DeleteUserIntent{TargetGUID: 9, ExpectedAuthVersion: 3, Reason: "\t reason \n"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(trimmed, spaced) {
		t.Fatalf("equivalent delete intents encoded differently: %x vs %x", trimmed, spaced)
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
