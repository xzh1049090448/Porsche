package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func TestRolePermissionOutboxWriterConstructorFailsClosed(t *testing.T) {
	if writer, err := NewRolePermissionOutboxWriter(nil, deleteWriterClock(8001)); writer != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil GUID source accepted: %#v/%v", writer, err)
	}
	if writer, err := NewRolePermissionOutboxWriter(func() int64 { return 1 }, nil); writer != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil clock accepted: %#v/%v", writer, err)
	}
}

func TestRolePermissionOutboxWriterPersistsExactPendingEnvelope(t *testing.T) {
	db, script := newA08WriterDB(t, actionsecurity.ActionUsersPromote)
	writer, err := NewRolePermissionOutboxWriter(func() int64 { return 7101 }, deleteWriterClock(8001))
	if err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if err := writer.Write(context.Background(), tx, a08OutboxEvent(actionsecurity.ActionUsersPromote)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	values := script.singleCommittedInsert(t, "admin_action_outbox").columnValues(t)
	want := map[string]any{
		"guid": int64(7101), "created_at": int64(8001), "created_by": int64(41), "updated_at": int64(8001), "updated_by": int64(41), "is_deleted": int64(0),
		"operation_id": int64(31), "public_ref": deleteWriterPublicRef, "action": int64(actionsecurity.ActionUsersPromote),
		"target_kind": int64(actionsecurity.TargetUser), "target_guid": int64(6001), "state": int64(models.OperationSucceeded),
		"failure_code": nil, "result_kind": models.ResultUser, "result_guid": int64(6001), "delivery_state": int64(models.DeliveryPending),
		"available_at": int64(8001), "delivered_at": nil, "attempt_count": int64(0),
	}
	if len(values) != len(want) {
		t.Fatalf("outbox fields = %v", values)
	}
	for key, expected := range want {
		if fmt.Sprint(values[key]) != fmt.Sprint(expected) {
			t.Errorf("%s = %v, want %v", key, values[key], expected)
		}
	}
	encoded, _ := json.Marshal(values)
	for _, forbidden := range []string{"password", "ticket", "hmac", "sid", "reason", "internal_id"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Errorf("outbox leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestRolePermissionAuditWriterPersistsExactPublicBeforeAfterFacts(t *testing.T) {
	db, script := newA08WriterDB(t, actionsecurity.ActionUsersPromote)
	base := rolePermissionTestExecution(t, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{
		TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1,
		Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}}, Reason: " approved promotion ",
	})
	execution, err := newRolePermissionTransactionalExecution(base, &rolePermissionTxRevoker{})
	if err != nil {
		t.Fatal(err)
	}
	before := RolePermissionTargetSnapshot{ActorGUID: 4001, TargetGUID: 6001, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 7}
	policy := &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 1}
	plan := &RolePermissionTransitionPlan{DesiredRole: models.UserRoleAdmin, NextAuthVersion: 8, ExpectedPolicyVersion: 2, NextPolicyVersion: 3,
		CatalogVersion: 1, DesiredRules: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}}}
	if err := execution.recordAuditFacts(61, before, policy, []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}, plan); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if err := execution.Write(context.Background(), tx, a08AuditEvent(actionsecurity.ActionUsersPromote)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	values := script.singleCommittedInsert(t, "audit_logs").columnValues(t)
	if fmt.Sprint(values["user_id"]) != "61" || values["action"] != "users.promote" || values["resource"] != "user:6001" || values["ip"] != nil {
		t.Fatalf("audit envelope = %v", values)
	}
	detail := decodeDeleteWriterJSON(t, values["detail"])
	want := map[string]any{
		"operation_ref": deleteWriterPublicRef, "actor_guid": float64(4001), "target_guid": float64(6001), "reason": "approved promotion",
		"before_role": "user", "after_role": "admin", "before_auth_version": float64(7), "after_auth_version": float64(8),
		"before_permissions_version": float64(2), "after_permissions_version": float64(3), "catalog_version": float64(1),
		"before_rule_count": float64(1), "after_rule_count": float64(1),
	}
	if len(detail) != len(want) {
		t.Fatalf("detail keys = %v, want %v", detail, want)
	}
	for key, expected := range want {
		if fmt.Sprint(detail[key]) != fmt.Sprint(expected) {
			t.Errorf("detail %s = %v, want %v", key, detail[key], expected)
		}
	}
	encoded, _ := json.Marshal(detail)
	for _, forbidden := range []string{"password", "ticket", "hmac", "sid", "session_guid", "internal_id"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Errorf("audit leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestRolePermissionAuditWriterRejectsDifferentA08ActionBinding(t *testing.T) {
	db, script := newA08WriterDB(t, actionsecurity.ActionUsersDemote)
	base := rolePermissionTestExecution(t, actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{
		TargetGUID: 6001, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "bound promote",
	})
	execution, err := newRolePermissionTransactionalExecution(base, &rolePermissionTxRevoker{})
	if err != nil {
		t.Fatal(err)
	}
	before := RolePermissionTargetSnapshot{ActorGUID: 4001, TargetGUID: 6001, Role: models.UserRoleUser, Status: models.UserStatusActive, AuthVersion: 7}
	plan := &RolePermissionTransitionPlan{DesiredRole: models.UserRoleAdmin, NextAuthVersion: 8, ExpectedPolicyVersion: 0, NextPolicyVersion: 1, CatalogVersion: 1}
	if err := execution.recordAuditFacts(61, before, nil, nil, plan); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if err := execution.Write(context.Background(), tx, a08AuditEvent(actionsecurity.ActionUsersDemote)); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("cross-action audit accepted: %v", err)
	}
	_ = tx.Rollback().Error
	if script.committedCount() != 0 {
		t.Fatal("cross-action audit committed")
	}
}

func newA08WriterDB(t *testing.T, action actionsecurity.Action) (*gorm.DB, *deleteWriterScript) {
	t.Helper()
	db, script := newDeleteWriterDB(t)
	script.operation.Action = int(action)
	script.verification.Action = int(action)
	return db, script
}

func a08OutboxEvent(action actionsecurity.Action) ActionOutboxEvent {
	target, kind, result := int64(6001), models.ResultUser, int64(6001)
	return ActionOutboxEvent{PublicRef: deleteWriterPublicRef, ActorGUID: 4001, SessionGUID: 5001, Action: action,
		TargetKind: actionsecurity.TargetUser, TargetGUID: &target, State: models.OperationSucceeded, ResultKind: &kind, ResultGUID: &result, OccurredAt: 8001}
}

func a08AuditEvent(action actionsecurity.Action) ActionAuditEvent {
	outbox := a08OutboxEvent(action)
	return ActionAuditEvent{PublicRef: outbox.PublicRef, ActorGUID: outbox.ActorGUID, SessionGUID: outbox.SessionGUID, Action: outbox.Action,
		TargetKind: outbox.TargetKind, TargetGUID: outbox.TargetGUID, State: outbox.State, ResultKind: outbox.ResultKind, ResultGUID: outbox.ResultGUID, OccurredAt: outbox.OccurredAt}
}
