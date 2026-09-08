package models

import "testing"

func TestPermissionPolicyStableBidirectionalEncoding(t *testing.T) {
	capabilities := []string{
		"users.read", "users.create", "users.edit", "users.enable", "users.disable", "users.reset_password",
		"users.sessions.read", "users.sessions.revoke", "users.plan.change", "users.group.change", "users.quota.adjust", "users.delete",
		"users.deleted.read", "users.promote", "users.demote", "users.permissions.write", "users.audit.read", "groups.read",
		"groups.write", "public_content.read", "public_content.edit", "public_content.preview", "public_content.publish", "public_content.rollback",
	}
	for code, want := range capabilities {
		code++
		got, ok := PermissionCapabilityName(code)
		if !ok || got != want {
			t.Fatalf("capability name for %d = %q, %v; want %q, true", code, got, ok, want)
		}
		back, ok := PermissionCapabilityCode(want)
		if !ok || back != code {
			t.Fatalf("capability code for %q = %d, %v; want %d, true", want, back, ok, code)
		}
	}
	for code, want := range map[int]string{1: "inherit", 2: "allow", 3: "deny"} {
		got, ok := PermissionEffectName(code)
		if !ok || got != want {
			t.Fatalf("effect name for %d = %q, %v; want %q, true", code, got, ok, want)
		}
		back, ok := PermissionEffectCode(want)
		if !ok || back != code {
			t.Fatalf("effect code for %q = %d, %v; want %d, true", want, back, ok, code)
		}
	}
}

func TestPermissionPolicyUnknownEncodingIsRejected(t *testing.T) {
	for _, code := range []int{-1, 0, 25, 999} {
		if name, ok := PermissionCapabilityName(code); ok || name != "" {
			t.Fatalf("unknown capability %d accepted as %q", code, name)
		}
	}
	for _, name := range []string{"", "Users.Read", "users.READ", "unknown"} {
		if code, ok := PermissionCapabilityCode(name); ok || code != 0 {
			t.Fatalf("unknown capability %q accepted as %d", name, code)
		}
	}
	for _, code := range []int{-1, 0, 4, 999} {
		if name, ok := PermissionEffectName(code); ok || name != "" {
			t.Fatalf("unknown effect %d accepted as %q", code, name)
		}
	}
	for _, name := range []string{"", "ALLOW", "users.read", "unknown"} {
		if code, ok := PermissionEffectCode(name); ok || code != 0 {
			t.Fatalf("unknown effect %q accepted as %d", name, code)
		}
	}
}
