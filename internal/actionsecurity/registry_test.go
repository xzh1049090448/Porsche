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

func TestExactPublicAdministrationActionsAreActiveAndTicketed(t *testing.T) {
	want := map[Action]string{
		ActionPublicModelDelete:    "public_models.delete",
		ActionPublicPricingPublish: "public_pricing.publish",
		ActionPublicPricingRestore: "public_pricing.restore",
		ActionPublicContentPublish: "public_content.publish",
		ActionPublicContentRestore: "public_content.restore",
	}
	for action, name := range want {
		descriptor, ok := ResolveActiveAction(action)
		wantTarget := TargetPublicContent
		if action == ActionPublicPricingPublish {
			wantTarget = TargetNone
		}
		if !ok || !descriptor.Active || !descriptor.RequiresTicket || !descriptor.RootOnly || descriptor.Name != name || descriptor.TargetKind != wantTarget {
			t.Fatalf("action %d descriptor = %#v, found=%v", action, descriptor, ok)
		}
	}
}

func TestInactiveActionDescriptorsExactContract(t *testing.T) {
	want := []Descriptor{
		{ActionUsersCreateAdmin, "users.create_admin", "users.create", true, true, false, TargetNone, nil},
		{ActionUsersResetPassword, "users.reset_password", "users.reset_password", false, true, false, TargetUser, nil},
		{ActionUsersPromote, "users.promote", "users.promote", true, true, false, TargetUser, nil},
		{ActionUsersDemote, "users.demote", "users.demote", true, true, false, TargetUser, nil},
		{ActionUsersPermissionsWrite, "users.permissions.write", "users.permissions.write", true, true, false, TargetUser, nil},
		{ActionUsersDelete, "users.delete", "users.delete", false, true, false, TargetUser, nil},
		{ActionPublicContentPublish, "public_content.publish", "public_content.publish", true, true, false, TargetPublicContent, nil},
		{ActionPublicContentRollback, "public_content.rollback", "public_content.rollback", false, true, false, TargetPublicContent, nil},
		{ActionUsersCreate, "users.create", "users.create", false, false, false, TargetNone, nil},
		{ActionPublicModelDelete, "public_models.delete", "public_content.edit", true, true, false, TargetPublicContent, nil},
		{ActionPublicPricingPublish, "public_pricing.publish", "public_content.publish", true, true, false, TargetNone, nil},
		{ActionPublicPricingRestore, "public_pricing.restore", "public_content.rollback", true, true, false, TargetPublicContent, nil},
		{ActionPublicContentRestore, "public_content.restore", "public_content.rollback", true, true, false, TargetPublicContent, nil},
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
	if len(active) != 8 {
		t.Fatalf("active registry length = %d, want 8", len(active))
	}
	want := []Descriptor{
		{ActionUsersCreate, "users.create", "users.create", false, false, true, TargetNone, nil},
		{ActionUsersCreateAdmin, "users.create_admin", "users.create", true, true, true, TargetNone, nil},
		{ActionUsersDelete, "users.delete", "users.delete", false, true, true, TargetUser, nil},
		{ActionPublicModelDelete, "public_models.delete", "public_content.edit", true, true, true, TargetPublicContent, nil},
		{ActionPublicPricingPublish, "public_pricing.publish", "public_content.publish", true, true, true, TargetNone, nil},
		{ActionPublicPricingRestore, "public_pricing.restore", "public_content.rollback", true, true, true, TargetPublicContent, nil},
		{ActionPublicContentPublish, "public_content.publish", "public_content.publish", true, true, true, TargetPublicContent, nil},
		{ActionPublicContentRestore, "public_content.restore", "public_content.rollback", true, true, true, TargetPublicContent, nil},
	}
	for i, got := range active {
		if got.Action != want[i].Action || got.Name != want[i].Name || got.Capability != want[i].Capability || got.RootOnly != want[i].RootOnly ||
			got.RequiresTicket != want[i].RequiresTicket || got.Active != want[i].Active || got.TargetKind != want[i].TargetKind || got.Encode == nil {
			t.Fatalf("active descriptor[%d] = %+v, want metadata %+v and non-nil encoder", i, got, want[i])
		}
	}
	active[0].Name = "mutated"
	active = append(active, Descriptor{Name: "mutated"})
	if got := ActiveActionRegistry(); len(got) != 8 || got[0].Name != "users.create" || got[1].Name != "users.create_admin" || got[2].Name != "users.delete" {
		t.Fatal("active registry was mutated through returned slice")
	}
	for _, action := range []Action{ActionUsersResetPassword, ActionUsersPromote, ActionUsersDemote, ActionUsersPermissionsWrite, ActionPublicContentRollback} {
		if _, ok := ResolveActiveAction(action); ok {
			t.Fatalf("inactive action %d resolved from production registry", action)
		}
	}
	for _, action := range []Action{ActionUsersCreate, ActionUsersCreateAdmin, ActionUsersDelete} {
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
	if len(canonicalActionDescriptors) != 13 {
		t.Fatalf("canonical descriptor count = %d, want 13", len(canonicalActionDescriptors))
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
