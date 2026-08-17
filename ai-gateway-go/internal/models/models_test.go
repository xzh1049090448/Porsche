package models

import "testing"

func TestUserStatusIsActiveSupportsPythonEnumName(t *testing.T) {
	for _, status := range []UserStatus{"active", "ACTIVE", " Active "} {
		if !status.IsActive() {
			t.Fatalf("status %q should be active", status)
		}
	}

	if UserStatusDisabled.IsActive() {
		t.Fatal("disabled status should not be active")
	}
}
