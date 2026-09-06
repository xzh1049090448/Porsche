package actionsecurity

import "testing"

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

func TestRegistryReturnsCopiesAndActivatesOnlyCreateActionsAndUsersDelete(t *testing.T) {
	first := InactiveActionDescriptors()
	first[0].Name = "mutated"
	if got := InactiveActionDescriptors()[0].Name; got != "users.create_admin" {
		t.Fatalf("inactive registry was mutated through returned slice: %q", got)
	}
	active := ActiveActionRegistry()
	if len(active) != 3 {
		t.Fatalf("active registry length = %d, want 3", len(active))
	}
	want := []Descriptor{
		{ActionUsersDelete, "users.delete", "users.delete", false, true, true, TargetUser, nil},
		{ActionUsersCreate, "users.create", "users.create", false, false, true, TargetNone, nil},
		{ActionUsersCreateAdmin, "users.create_admin", "users.create", true, true, true, TargetNone, nil},
	}
	for i, got := range active {
		if got.Action != want[i].Action || got.Name != want[i].Name || got.Capability != want[i].Capability || got.RootOnly != want[i].RootOnly ||
			got.RequiresTicket != want[i].RequiresTicket || got.Active != want[i].Active || got.TargetKind != want[i].TargetKind || got.Encode == nil {
			t.Fatalf("active descriptor[%d] = %+v, want metadata %+v and non-nil encoder", i, got, want[i])
		}
	}
	active[0].Name = "mutated"
	active = append(active, Descriptor{Name: "mutated"})
	if got := ActiveActionRegistry(); len(got) != 3 || got[0].Name != "users.delete" {
		t.Fatal("active registry was mutated through returned slice")
	}
	for _, action := range []Action{ActionUsersResetPassword, ActionUsersPromote, ActionUsersDemote, ActionUsersPermissionsWrite, ActionPublicContentPublish, ActionPublicContentRollback} {
		if _, ok := ResolveActiveAction(action); ok {
			t.Fatalf("inactive action %d resolved from production registry", action)
		}
	}
	for _, action := range []Action{ActionUsersCreate, ActionUsersCreateAdmin, ActionUsersDelete} {
		if got, ok := ResolveActiveAction(action); !ok || got.Action != action || !got.Active {
			t.Fatalf("action %d did not resolve as active: %+v, ok=%v", action, got, ok)
		}
	}
}

func TestDescriptorRejectsWrongDTOType(t *testing.T) {
	descriptors := append(InactiveActionDescriptors(), ActiveActionRegistry()...)
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
