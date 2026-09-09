package actionsecurity

import (
	"reflect"
	"testing"
)

func descriptorByAction(t *testing.T, descriptors []Descriptor, action Action) Descriptor {
	t.Helper()
	for _, descriptor := range descriptors {
		if descriptor.Action == action {
			return descriptor
		}
	}
	t.Fatalf("action %d missing", action)
	return Descriptor{}
}

func TestInactiveActionDescriptorsExactContract(t *testing.T) {
	want := []Descriptor{
		{ActionUsersCreateAdmin, "users.create_admin", "users.create", true, true, false, TargetNone, nil},
		{ActionUsersResetPassword, "users.reset_password", "users.reset_password", false, true, false, TargetUser, nil},
		{ActionUsersPromote, "users.promote", "users.promote", true, true, false, TargetUser, nil},
		{ActionUsersDemote, "users.demote", "users.demote", true, true, false, TargetUser, nil},
		{ActionUsersPermissionsWrite, "users.permissions.write", "users.permissions.write", true, true, false, TargetUser, nil},
		{ActionUsersDelete, "users.delete", "users.delete", false, true, false, TargetUser, nil},
		{ActionPublicContentPublish, "public_content.publish", "public_content.publish", false, true, false, TargetPublicContent, nil},
		{ActionPublicContentRollback, "public_content.rollback", "public_content.rollback", false, true, false, TargetPublicContent, nil},
		{ActionUsersCreate, "users.create", "users.create", false, false, false, TargetNone, nil},
	}
	got := InactiveActionDescriptors()
	if len(got) != len(want) {
		t.Fatalf("descriptor count = %d, want %d", len(got), len(want))
	}
	seenActions := map[Action]bool{}
	seenNames := map[string]bool{}
	for i := range want {
		if int(got[i].Action) != i+1 {
			t.Errorf("descriptor[%d] action integer = %d, want %d", i, got[i].Action, i+1)
		}
		if got[i].Action != want[i].Action || got[i].Name != want[i].Name || got[i].Capability != want[i].Capability ||
			got[i].RootOnly != want[i].RootOnly || got[i].RequiresTicket != want[i].RequiresTicket ||
			got[i].Active != want[i].Active || got[i].TargetKind != want[i].TargetKind || got[i].Encode == nil {
			t.Errorf("descriptor[%d] = %+v, want metadata %+v and non-nil encoder", i, got[i], want[i])
		}
		if seenActions[got[i].Action] || seenNames[got[i].Name] {
			t.Errorf("duplicate descriptor action/name: %d %q", got[i].Action, got[i].Name)
		}
		seenActions[got[i].Action], seenNames[got[i].Name] = true, true
	}
}

func TestRegistryReturnsCopiesAndActivatesExactUserManagementBundle(t *testing.T) {
	first := InactiveActionDescriptors()
	first[0].Name = "mutated"
	if got := InactiveActionDescriptors()[0].Name; got != "users.create_admin" {
		t.Fatalf("inactive registry was mutated through returned slice: %q", got)
	}
	active := ActiveActionRegistry()
	if len(active) != 7 {
		t.Fatalf("active registry length = %d, want 7", len(active))
	}
	want := []Descriptor{
		{ActionUsersCreate, "users.create", "users.create", false, false, true, TargetNone, nil},
		{ActionUsersCreateAdmin, "users.create_admin", "users.create", true, true, true, TargetNone, nil},
		{ActionUsersDelete, "users.delete", "users.delete", false, true, true, TargetUser, nil},
		{ActionUsersResetPassword, "users.reset_password", "users.reset_password", false, true, true, TargetUser, nil},
		{ActionUsersPromote, "users.promote", "users.promote", true, true, true, TargetUser, nil},
		{ActionUsersDemote, "users.demote", "users.demote", true, true, true, TargetUser, nil},
		{ActionUsersPermissionsWrite, "users.permissions.write", "users.permissions.write", true, true, true, TargetUser, nil},
	}
	for i, got := range active {
		if got.Action != want[i].Action || got.Name != want[i].Name || got.Capability != want[i].Capability || got.RootOnly != want[i].RootOnly ||
			got.RequiresTicket != want[i].RequiresTicket || got.Active != want[i].Active || got.TargetKind != want[i].TargetKind || got.Encode == nil {
			t.Fatalf("active descriptor[%d] = %+v, want metadata %+v and non-nil encoder", i, got, want[i])
		}
	}
	active[0].Name = "mutated"
	active = append(active, Descriptor{Name: "mutated"})
	if got := ActiveActionRegistry(); len(got) != 7 || got[6].Name != "users.permissions.write" {
		t.Fatal("active registry was mutated through returned slice")
	}
	for _, action := range []Action{ActionPublicContentPublish, ActionPublicContentRollback} {
		if _, ok := ResolveActiveAction(action); ok {
			t.Fatalf("inactive action %d resolved from production registry", action)
		}
	}
	for _, action := range []Action{ActionUsersCreate, ActionUsersCreateAdmin, ActionUsersDelete, ActionUsersResetPassword, ActionUsersPromote, ActionUsersDemote, ActionUsersPermissionsWrite} {
		if got, ok := ResolveActiveAction(action); !ok || got.Action != action || !got.Active {
			t.Fatalf("user-management action %d did not resolve as active: %+v, ok=%v", action, got, ok)
		}
	}
}

func TestFutureCreateActionDescriptorsExactCanonicalOrder(t *testing.T) {
	want := []Descriptor{
		{ActionUsersCreate, "users.create", "users.create", false, false, true, TargetNone, nil},
		{ActionUsersCreateAdmin, "users.create_admin", "users.create", true, true, true, TargetNone, nil},
		{ActionUsersDelete, "users.delete", "users.delete", false, true, true, TargetUser, nil},
		{ActionUsersResetPassword, "users.reset_password", "users.reset_password", false, true, true, TargetUser, nil},
		{ActionUsersPromote, "users.promote", "users.promote", true, true, true, TargetUser, nil},
		{ActionUsersDemote, "users.demote", "users.demote", true, true, true, TargetUser, nil},
		{ActionUsersPermissionsWrite, "users.permissions.write", "users.permissions.write", true, true, true, TargetUser, nil},
	}
	got := FutureActionDescriptors()
	if len(got) != len(want) {
		t.Fatalf("future descriptor count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Action != want[i].Action || got[i].Name != want[i].Name || got[i].Capability != want[i].Capability ||
			got[i].RootOnly != want[i].RootOnly || got[i].RequiresTicket != want[i].RequiresTicket || got[i].Active != want[i].Active ||
			got[i].TargetKind != want[i].TargetKind || got[i].Encode == nil {
			t.Fatalf("future descriptor[%d] = %+v, want metadata %+v and non-nil encoder", i, got[i], want[i])
		}
	}
	got[0].Name = "mutated"
	if got := FutureActionDescriptors()[0].Name; got != "users.create" {
		t.Fatalf("future registry was mutated through returned slice: %q", got)
	}
}

func TestRegistryViewsProjectOneCanonicalDescriptorPerAction(t *testing.T) {
	if len(canonicalActionDescriptors) != 9 {
		t.Fatalf("canonical descriptor count = %d, want 9", len(canonicalActionDescriptors))
	}
	seen := map[Action]bool{}
	for _, descriptor := range canonicalActionDescriptors {
		if seen[descriptor.Action] {
			t.Fatalf("duplicate canonical descriptor for action %d", descriptor.Action)
		}
		seen[descriptor.Action] = true
	}
	views := [][]Descriptor{InactiveActionDescriptors(), FutureActionDescriptors(), ActiveActionRegistry()}
	for _, view := range views {
		for _, descriptor := range view {
			canonical, ok := canonicalActionDescriptor(descriptor.Action)
			if !ok {
				t.Fatalf("view action %d missing canonical descriptor", descriptor.Action)
			}
			if descriptor.Action != canonical.Action || descriptor.Name != canonical.Name || descriptor.Capability != canonical.Capability ||
				descriptor.RootOnly != canonical.RootOnly || descriptor.RequiresTicket != canonical.RequiresTicket || descriptor.TargetKind != canonical.TargetKind ||
				reflect.ValueOf(descriptor.Encode).Pointer() != reflect.ValueOf(canonical.Encode).Pointer() {
				t.Fatalf("view descriptor drifted from canonical action %d: %#v vs %#v", descriptor.Action, descriptor, canonical)
			}
		}
	}
}

func TestFutureActionDescriptorsUseTheirOwnTypedEncoders(t *testing.T) {
	future := FutureActionDescriptors()
	tests := []struct {
		name      string
		action    Action
		valid     any
		wrongRole any
		wrongType any
	}{
		{
			name:      "ordinary create",
			action:    ActionUsersCreate,
			valid:     CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "user", PlanType: 1},
			wrongRole: CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1},
			wrongType: DeleteUserIntent{},
		},
		{
			name:      "admin create",
			action:    ActionUsersCreateAdmin,
			valid:     CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "admin", PlanType: 1},
			wrongRole: CreateAccountIntent{Username: "alice", Password: []byte{1}, Role: "user", PlanType: 1},
			wrongType: DeleteUserIntent{},
		},
		{
			name:      "delete",
			action:    ActionUsersDelete,
			valid:     DeleteUserIntent{TargetGUID: 1, ExpectedAuthVersion: 1, Reason: "case"},
			wrongType: CreateAccountIntent{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			descriptor := descriptorByAction(t, future, tc.action)
			if _, err := descriptor.Encode(tc.valid); err != nil {
				t.Fatalf("valid intent rejected: %v", err)
			}
			if tc.wrongRole != nil {
				if _, err := descriptor.Encode(tc.wrongRole); err == nil {
					t.Fatal("wrong role accepted")
				}
			}
			if _, err := descriptor.Encode(tc.wrongType); err != errWrongIntentType {
				t.Fatalf("wrong type error = %v, want %v", err, errWrongIntentType)
			}
		})
	}
}

func TestDescriptorRejectsWrongDTOType(t *testing.T) {
	descriptors := append(InactiveActionDescriptors(), FutureActionDescriptors()...)
	for _, descriptor := range descriptors {
		if _, err := descriptor.Encode(struct{}{}); err == nil {
			t.Fatalf("descriptor %q accepted wrong DTO", descriptor.Name)
		}
	}
}

func TestCreateEncodersRejectWrongDTOType(t *testing.T) {
	for _, action := range []Action{ActionUsersCreate, ActionUsersCreateAdmin} {
		if _, err := descriptorFor(t, action).Encode(DeleteUserIntent{}); err != errWrongIntentType {
			t.Fatalf("create action %d wrong DTO error = %v, want %v", action, err, errWrongIntentType)
		}
	}
}

func TestA08InactiveDescriptorsUseDedicatedTypedEncoders(t *testing.T) {
	tests := []struct {
		action    Action
		wantValue int
		valid     any
		wrong     any
	}{
		{ActionUsersPromote, 3, PromoteIntent{TargetGUID: 1, ExpectedAuthVersion: 1, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "case"}, DemoteIntent{}},
		{ActionUsersDemote, 4, DemoteIntent{TargetGUID: 1, ExpectedAuthVersion: 1, ExpectedPermissionsVersion: 1, CatalogVersion: 1, Reason: "case"}, PromoteIntent{}},
		{ActionUsersPermissionsWrite, 5, PermissionsWriteIntent{TargetGUID: 1, ExpectedAuthVersion: 1, ExpectedPermissionsVersion: 1, CatalogVersion: 1, Reason: "case"}, PromoteIntent{}},
	}
	for _, tc := range tests {
		descriptor := descriptorByAction(t, InactiveActionDescriptors(), tc.action)
		if int(descriptor.Action) != tc.wantValue || descriptor.Active {
			t.Fatalf("action %d descriptor = %+v, want integer %d and inactive", tc.action, descriptor, tc.wantValue)
		}
		if _, err := descriptor.Encode(tc.valid); err != nil {
			t.Fatalf("action %d rejected dedicated intent: %v", tc.action, err)
		}
		if _, err := descriptor.Encode(tc.wrong); err != errWrongIntentType {
			t.Fatalf("action %d wrong type error = %v, want %v", tc.action, err, errWrongIntentType)
		}
	}
	if got := ActiveActionRegistry(); len(got) != 7 {
		t.Fatalf("active registry length = %d, want 7", len(got))
	}
}
