package models

import "testing"

func TestUserStatusIsActiveUsesStableIntegerMapping(t *testing.T) {
	if !UserStatusActive.IsActive() {
		t.Fatal("active status should be active")
	}
	if UserStatusDisabled.IsActive() {
		t.Fatal("disabled status should not be active")
	}
}
