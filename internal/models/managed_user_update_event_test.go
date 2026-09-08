package models

import "testing"

// TestAuthAuditEventManagedUserUpdatedKeepsThePublishedEventNumbersStable
// makes the new administrator-managed update event append-only. Auth audit
// values are persisted integers, so changing any historical value would make
// existing records ambiguous.
func TestAuthAuditEventManagedUserUpdatedKeepsThePublishedEventNumbersStable(t *testing.T) {
	for _, testCase := range []struct {
		event AuthAuditEventType
		value int
		name  string
	}{
		{AuthAuditEventRegistered, 1, "registered"},
		{AuthAuditEventLoginSucceeded, 2, "login_succeeded"},
		{AuthAuditEventRefreshSucceeded, 3, "refresh_succeeded"},
		{AuthAuditEventReplayRevoked, 4, "refresh_replay_revoked"},
		{AuthAuditEventLoggedOut, 5, "logged_out"},
		{AuthAuditEventSessionRevoked, 6, "session_revoked"},
		{AuthAuditEventUserDisabled, 7, "user_disabled"},
		{AuthAuditEventUserDeleted, 8, "user_deleted"},
		{AuthAuditEventPasswordChanged, 9, "password_changed"},
		{AuthAuditEventManagedUserUpdated, 10, "managed_user_updated"},
	} {
		if got := int(testCase.event); got != testCase.value {
			t.Fatalf("event %q value=%d, want stable %d", testCase.name, got, testCase.value)
		}
		if got := testCase.event.String(); got != testCase.name {
			t.Fatalf("event %d String()=%q, want %q", testCase.value, got, testCase.name)
		}
		if got, ok := ParseAuthAuditEventType(testCase.name); !ok || got != testCase.event {
			t.Fatalf("ParseAuthAuditEventType(%q)=(%d, %t), want (%d, true)", testCase.name, got, ok, testCase.event)
		}
	}
	if got, ok := ParseAuthAuditEventType("managed-user-updated"); ok || got != 0 {
		t.Fatalf("ParseAuthAuditEventType accepted non-contract spelling: (%d, %t)", got, ok)
	}
}
