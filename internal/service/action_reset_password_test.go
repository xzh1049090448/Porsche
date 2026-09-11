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
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

func TestResetPasswordExecutionRedactsSecrets(t *testing.T) {
	password := []byte("Strong!Pass1")
	hash := []byte("encoded-password-hash")
	execution, err := NewResetPasswordExecution(actionsecurity.ResetPasswordIntent{TargetGUID: 91, ExpectedAuthVersion: 7, NewPassword: password, Reason: "rotation"}, hash, nil, persistence.SystemClock(), persistence.NextGUID, nil)
	if err == nil || execution != nil {
		t.Fatal("constructor accepted missing Redis dependency")
	}
	if got := fmt.Sprintf("%v", actionsecurity.ResetPasswordIntent{TargetGUID: 91, ExpectedAuthVersion: 7, NewPassword: []byte("Strong!Pass1"), Reason: "rotation"}); got == "" {
		t.Fatal("unreachable")
	}
	encoded, _ := json.Marshal(execution)
	if string(encoded) != "null" {
		t.Fatalf("nil execution JSON=%s", encoded)
	}
}

func TestResetPasswordExecutionBindingFailsClosed(t *testing.T) {
	verificationID := int64(51)
	targetGUID := int64(6001)
	requestHMAC := strings.Repeat("a", 64)
	leaseHMAC := strings.Repeat("b", 64)
	leaseExpiresAt := int64(9_000)
	execution := &ResetPasswordExecution{intent: actionsecurity.ResetPasswordIntent{TargetGUID: targetGUID}, requestHMAC: requestHMAC}
	operation := models.AdminOperation{ID: 31, AuditFields: models.AuditFields{Guid: 3101}, ActorUserID: 41, ActorAuthVersion: 3, SessionID: 45,
		Action: int(actionsecurity.ActionUsersResetPassword), VerificationID: &verificationID,
		State: models.OperationProcessing, PublicRef: deleteWriterPublicRef, RequestHMAC: requestHMAC,
		LeaseOwnerHMAC: &leaseHMAC, LeaseExpiresAt: &leaseExpiresAt, QueryExpiresAt: 10_000}
	verification := models.AdminActionVerification{ID: verificationID, ActorUserID: 41, ActorAuthVersion: 3, SessionID: 45,
		Action: int(actionsecurity.ActionUsersResetPassword), TargetKind: int(actionsecurity.TargetUser), TargetGUID: &targetGUID, IntentHMAC: requestHMAC, ExpiresAt: 8_000}
	if !execution.validExecutionBinding(operation, verification) {
		t.Fatal("valid reset execution binding rejected")
	}
	for _, test := range []struct {
		name   string
		mutate func(*models.AdminOperation, *models.AdminActionVerification)
	}{
		{"operation request digest", func(op *models.AdminOperation, _ *models.AdminActionVerification) {
			op.RequestHMAC = strings.Repeat("b", 64)
		}},
		{"verification request digest", func(_ *models.AdminOperation, v *models.AdminActionVerification) {
			v.IntentHMAC = strings.Repeat("b", 64)
		}},
		{"verification id", func(_ *models.AdminOperation, v *models.AdminActionVerification) { v.ID++ }},
		{"actor", func(_ *models.AdminOperation, v *models.AdminActionVerification) { v.ActorUserID++ }},
		{"actor auth version", func(_ *models.AdminOperation, v *models.AdminActionVerification) { v.ActorAuthVersion++ }},
		{"session", func(_ *models.AdminOperation, v *models.AdminActionVerification) { v.SessionID++ }},
		{"action", func(_ *models.AdminOperation, v *models.AdminActionVerification) {
			v.Action = int(actionsecurity.ActionUsersDelete)
		}},
		{"target kind", func(_ *models.AdminOperation, v *models.AdminActionVerification) {
			v.TargetKind = int(actionsecurity.TargetNone)
		}},
		{"target", func(_ *models.AdminOperation, v *models.AdminActionVerification) {
			other := targetGUID + 1
			v.TargetGUID = &other
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			op, v := operation, verification
			test.mutate(&op, &v)
			if execution.validExecutionBinding(op, v) {
				t.Fatal("corrupted execution binding accepted")
			}
		})
	}
}

func TestLoadResetPasswordBindingRejectsCorruption(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*deleteWriterScript)
	}{
		{"operation actor auth version", func(s *deleteWriterScript) { s.operation.ActorAuthVersion = 0 }},
		{"verification actor", func(s *deleteWriterScript) { s.verification.ActorUserID++ }},
		{"verification actor auth version", func(s *deleteWriterScript) { s.verification.ActorAuthVersion++ }},
		{"verification session", func(s *deleteWriterScript) { s.verification.SessionID++ }},
		{"verification target kind", func(s *deleteWriterScript) { s.verification.TargetKind = int(actionsecurity.TargetNone) }},
		{"session actor", func(s *deleteWriterScript) { s.session.UserID++ }},
		{"public ref", func(s *deleteWriterScript) { s.operation.PublicRef = "op_" + strings.Repeat("B", 43) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, script := newDeleteWriterDB(t)
			script.operation.Action = int(actionsecurity.ActionUsersResetPassword)
			script.verification.Action = int(actionsecurity.ActionUsersResetPassword)
			test.mutate(script)
			err := db.Transaction(func(tx *gorm.DB) error {
				_, err := loadResetPasswordBinding(tx, deleteWriterPublicRef, script.actor.Guid, script.session.Guid, *script.verification.TargetGUID)
				return err
			})
			if !errors.Is(err, ErrActionOperationUnavailable) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestResetPasswordEngineLocksTargetSessionsBeforePolicy(t *testing.T) {
	operations, script, identity := actionExecuteFixture(t)
	descriptor, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersResetPassword)
	if !ok {
		t.Fatal("reset descriptor inactive")
	}
	script.resetPrelock = true
	script.state.operation.Action = int(actionsecurity.ActionUsersResetPassword)
	script.state.verification.Action = int(actionsecurity.ActionUsersResetPassword)
	script.state.verification.TargetKind = int(actionsecurity.TargetUser)
	script.state.operation.RequestHMAC = strings.Repeat("b", 64)
	script.state.verification.IntentHMAC = script.state.operation.RequestHMAC
	script.targetSessions = []models.Session{{ID: 71, AuditFields: models.AuditFields{Guid: 7101}, SID: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", UserID: script.target.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 2, ExpiresAt: script.now + 60_000}}
	operations.resolve = func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		return descriptor, action == actionsecurity.ActionUsersResetPassword
	}
	execution := &ResetPasswordExecution{
		descriptor: descriptor,
		intent:     actionsecurity.ResetPasswordIntent{TargetGUID: script.target.Guid, ExpectedAuthVersion: script.target.AuthVersion + 1, Reason: "rotation"},
		clock:      &actionIssueClock{now: script.now}, requestHMAC: script.state.operation.RequestHMAC,
		state: &resetPasswordExecutionState{hash: []byte("encoded-password-hash")},
	}
	view, err := operations.Execute(context.Background(), identity, execution, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
	if err != nil || view == nil || view.Status != "failed" {
		t.Fatalf("Execute=%#v err=%v validation=%s prelocked=%t started=%t queries=%v", view, err, script.lastError, execution.state.prelocked, execution.state.started, script.queries)
	}
	var locked []string
	for _, query := range script.queries {
		if strings.Contains(query, "FOR UPDATE") {
			locked = append(locked, executeQueryKind(query))
		}
	}
	want := []string{"actor", "session", "operation", "verification", "target", "target_sessions", "policy", "rules"}
	if fmt.Sprint(locked) != fmt.Sprint(want) {
		t.Fatalf("lock order=%v want=%v", locked, want)
	}
}

func TestResetPasswordPolicyAcceptsActiveAndDisabledExactVersion(t *testing.T) {
	descriptor, ok := actionsecurity.ResolveActiveAction(actionsecurity.ActionUsersResetPassword)
	if !ok {
		t.Fatal("reset descriptor inactive")
	}
	for _, status := range []models.UserStatus{models.UserStatusActive, models.UserStatusDisabled} {
		password := []byte("Strong!Pass1")
		intent := actionsecurity.ResetPasswordIntent{TargetGUID: 91, ExpectedAuthVersion: 7, NewPassword: password, Reason: "rotation"}
		target := &models.User{ID: 4, AuditFields: models.AuditFields{Guid: 91}, Status: status, AuthVersion: 7}
		if err := validateLockedResetPasswordIntent(descriptor, intent, target); err != nil {
			t.Fatalf("status %d rejected: %v", status, err)
		}
		intent.ExpectedAuthVersion++
		if err := validateLockedResetPasswordIntent(descriptor, intent, target); !errors.Is(err, ErrActionVerificationConflict) {
			t.Fatalf("stale version error = %v", err)
		}
		clear(password)
	}
}
