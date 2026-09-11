package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

const deleteWriterPublicRef = "op_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type deleteWriterClock int64

func (clock deleteWriterClock) NowMillis() int64 { return int64(clock) }

func deleteWriterIntent() actionsecurity.DeleteUserIntent {
	return actionsecurity.DeleteUserIntent{TargetGUID: 6001, ExpectedAuthVersion: 7, Reason: "  approved deletion  "}
}

func TestNewDeleteUserExecutionValidatesAndOwnsNormalizedIntent(t *testing.T) {
	execution, err := NewDeleteUserExecution(deleteWriterIntent(), func() int64 { return 7001 }, deleteWriterClock(8001), createAccountTestCrypto(t))
	if err != nil {
		t.Fatal(err)
	}
	if execution.intent.Reason != "approved deletion" || execution.intent.TargetGUID != 6001 || execution.intent.ExpectedAuthVersion != 7 {
		t.Fatalf("owned intent = %#v", execution.intent)
	}
	for _, rendered := range []string{
		fmt.Sprint(execution), fmt.Sprintf("%+v", execution), fmt.Sprintf("%#v", execution),
		fmt.Sprint(*execution), fmt.Sprintf("%+v", *execution), fmt.Sprintf("%#v", *execution),
	} {
		if strings.Contains(rendered, "approved deletion") {
			t.Fatalf("formatted execution leaked reason: %s", rendered)
		}
	}
	encoded, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "approved deletion") {
		t.Fatalf("JSON leaked reason: %s", encoded)
	}
}

func TestDeleteUserWriterConstructorsFailClosed(t *testing.T) {
	validNext := func() int64 { return 1 }
	validClock := deleteWriterClock(2)
	invalidIntents := []actionsecurity.DeleteUserIntent{
		{},
		{TargetGUID: 1, ExpectedAuthVersion: 1, Reason: "   "},
		{TargetGUID: -1, ExpectedAuthVersion: 1, Reason: "reason"},
		{TargetGUID: 1, ExpectedAuthVersion: 0, Reason: "reason"},
	}
	for _, intent := range invalidIntents {
		if got, err := NewDeleteUserExecution(intent, validNext, validClock, createAccountTestCrypto(t)); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
			t.Fatalf("invalid intent accepted: got=%v err=%v", got, err)
		}
	}
	if got, err := NewDeleteUserExecution(deleteWriterIntent(), nil, validClock, createAccountTestCrypto(t)); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil GUID dependency accepted: got=%v err=%v", got, err)
	}
	if got, err := NewDeleteUserExecution(deleteWriterIntent(), validNext, nil, createAccountTestCrypto(t)); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil clock accepted: got=%v err=%v", got, err)
	}
	if got, err := NewDeleteUserExecution(deleteWriterIntent(), validNext, validClock, nil); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil response integrity key accepted: got=%v err=%v", got, err)
	}
	if got, err := NewAdminActionOutboxWriter(nil, validClock); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil outbox GUID dependency accepted: got=%v err=%v", got, err)
	}
	if got, err := NewAdminActionOutboxWriter(validNext, nil); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil outbox clock accepted: got=%v err=%v", got, err)
	}
}

func TestDeleteUserExecutionRecordsAuditFactsExactlyOnce(t *testing.T) {
	execution := newDeleteWriterExecution(t, func() int64 { return 7001 })
	if err := execution.recordAuditFacts(0, 6001, models.UserStatusActive); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("invalid facts error = %v", err)
	}
	if err := execution.recordAuditFacts(61, 6002, models.UserStatusActive); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("mismatched target error = %v", err)
	}
	if err := execution.recordAuditFacts(61, 6001, models.UserStatus(99)); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("invalid status error = %v", err)
	}

	var successes atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 16; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if execution.recordAuditFacts(61, 6001, models.UserStatusDisabled) == nil {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if successes.Load() != 1 {
		t.Fatalf("successful fact recordings = %d, want 1", successes.Load())
	}
}

func TestDeleteUserAuditWriterPersistsExactAllowlistedRow(t *testing.T) {
	db, script := newDeleteWriterDB(t)
	execution := newDeleteWriterExecution(t, func() int64 { return 7001 })
	if err := execution.recordAuditFacts(61, 6001, models.UserStatusDisabled); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	if err := execution.Write(context.Background(), tx, deleteAuditEvent()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}

	insert := script.singleCommittedInsert(t, "audit_logs")
	values := insert.columnValues(t)
	want := map[string]any{
		"guid": int64(7001), "created_at": int64(8001), "created_by": int64(41),
		"updated_at": int64(8001), "updated_by": int64(41), "is_deleted": int64(0),
		"user_id": int64(61), "action": "users.delete", "resource": "user:6001", "ip": nil,
	}
	for column, expected := range want {
		if fmt.Sprint(values[column]) != fmt.Sprint(expected) {
			t.Errorf("audit %s = %v, want %v", column, values[column], expected)
		}
	}
	detail := decodeDeleteWriterJSON(t, values["detail"])
	wantDetail := map[string]any{
		"operation_ref": deleteWriterPublicRef,
		"actor_guid":    float64(4001),
		"target_guid":   float64(6001),
		"reason":        "approved deletion",
		"before_status": "disabled",
		"after_status":  "deleted",
	}
	if len(detail) != len(wantDetail) {
		t.Fatalf("audit detail keys = %v, want exact allowlist %v", detail, wantDetail)
	}
	for key, expected := range wantDetail {
		if fmt.Sprint(detail[key]) != fmt.Sprint(expected) {
			t.Errorf("audit detail %s = %v, want %v", key, detail[key], expected)
		}
	}
	encoded, _ := json.Marshal(detail)
	for _, forbidden := range []string{"password", "ticket", "hmac", "session", "key", "internal_id"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Errorf("audit detail contains forbidden field: %s", encoded)
		}
	}
}

func TestDeleteUserAuditWriterRejectsInvalidContextTransactionEventAndBinding(t *testing.T) {
	db, _ := newDeleteWriterDB(t)
	fresh := func() *DeleteUserExecution {
		execution := newDeleteWriterExecution(t, func() int64 { return 7001 })
		if err := execution.recordAuditFacts(61, 6001, models.UserStatusActive); err != nil {
			t.Fatal(err)
		}
		return execution
	}
	nilContextTx := db.Begin()
	if err := fresh().Write(nil, nilContextTx, deleteAuditEvent()); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil context error = %v", err)
	}
	_ = nilContextTx.Rollback().Error
	if err := fresh().Write(context.Background(), nil, deleteAuditEvent()); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("nil tx error = %v", err)
	}
	if err := fresh().Write(context.Background(), db, deleteAuditEvent()); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("root DB error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ActionAuditEvent, *deleteWriterScript)
	}{
		{"bad public ref", func(event *ActionAuditEvent, _ *deleteWriterScript) { event.PublicRef = "op_bad" }},
		{"actor mismatch", func(event *ActionAuditEvent, _ *deleteWriterScript) { event.ActorGUID++ }},
		{"wrong action", func(event *ActionAuditEvent, _ *deleteWriterScript) { event.Action = actionsecurity.ActionUsersPromote }},
		{"wrong target kind", func(event *ActionAuditEvent, _ *deleteWriterScript) { event.TargetKind = actionsecurity.TargetNone }},
		{"wrong target", func(event *ActionAuditEvent, _ *deleteWriterScript) { value := int64(6002); event.TargetGUID = &value }},
		{"failed without failure", func(event *ActionAuditEvent, _ *deleteWriterScript) {
			event.State = models.OperationFailed
			event.ResultKind = nil
			event.ResultGUID = nil
		}},
		{"wrong result", func(event *ActionAuditEvent, _ *deleteWriterScript) { value := int64(6002); event.ResultGUID = &value }},
		{"future time", func(event *ActionAuditEvent, _ *deleteWriterScript) { event.OccurredAt++ }},
		{"operation terminal early", func(_ *ActionAuditEvent, script *deleteWriterScript) {
			script.operation.State = models.OperationSucceeded
		}},
		{"operation action mismatch", func(_ *ActionAuditEvent, script *deleteWriterScript) {
			script.operation.Action = int(actionsecurity.ActionUsersPromote)
		}},
		{"verification target mismatch", func(_ *ActionAuditEvent, script *deleteWriterScript) {
			value := int64(6002)
			script.verification.TargetGUID = &value
		}},
		{"verification session mismatch", func(_ *ActionAuditEvent, script *deleteWriterScript) {
			script.verification.SessionID++
		}},
		{"verification not consumed", func(_ *ActionAuditEvent, script *deleteWriterScript) {
			script.verification.ConsumedAt = nil
		}},
		{"session actor mismatch", func(_ *ActionAuditEvent, script *deleteWriterScript) {
			script.session.UserID++
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testDB, script := newDeleteWriterDB(t)
			event := deleteAuditEvent()
			tc.mutate(&event, script)
			tx := testDB.Begin()
			err := fresh().Write(context.Background(), tx, event)
			if !errors.Is(err, ErrActionOperationUnavailable) || err.Error() != ErrActionOperationUnavailable.Error() {
				t.Fatalf("error = %v", err)
			}
			_ = tx.Rollback().Error
			if script.committedCount() != 0 {
				t.Fatal("invalid audit event persisted")
			}
		})
	}
}

func TestDeleteUserAuditWriterPersistsFailedTerminalWithoutBusinessFacts(t *testing.T) {
	db, script := newDeleteWriterDB(t)
	execution := newDeleteWriterExecution(t, func() int64 { return 7003 })
	tx := db.Begin()
	if err := execution.Write(context.Background(), tx, deleteFailedAuditEvent()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	values := script.singleCommittedInsert(t, "audit_logs").columnValues(t)
	if values["user_id"] != nil {
		t.Fatalf("failed audit user_id = %v, want nil without safe target row facts", values["user_id"])
	}
	detail := decodeDeleteWriterJSON(t, values["detail"])
	want := map[string]any{
		"operation_ref": deleteWriterPublicRef,
		"actor_guid":    float64(4001),
		"target_guid":   float64(6001),
		"reason":        "approved deletion",
	}
	if len(detail) != len(want) {
		t.Fatalf("failed audit detail = %v, want exact fields %v", detail, want)
	}
	for key, expected := range want {
		if fmt.Sprint(detail[key]) != fmt.Sprint(expected) {
			t.Errorf("failed audit detail %s = %v, want %v", key, detail[key], expected)
		}
	}
}

func TestDeleteUserAuditWriterFailedTerminalIncludesOnlyRecordedBeforeStatus(t *testing.T) {
	db, script := newDeleteWriterDB(t)
	execution := newDeleteWriterExecution(t, func() int64 { return 7004 })
	if err := execution.recordAuditFacts(61, 6001, models.UserStatusActive); err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if err := execution.Write(context.Background(), tx, deleteFailedAuditEvent()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	values := script.singleCommittedInsert(t, "audit_logs").columnValues(t)
	if fmt.Sprint(values["user_id"]) != "61" {
		t.Fatalf("failed audit user_id = %v", values["user_id"])
	}
	detail := decodeDeleteWriterJSON(t, values["detail"])
	if detail["before_status"] != "active" || detail["after_status"] != nil || len(detail) != 5 {
		t.Fatalf("failed audit facts = %v", detail)
	}
}

func TestDeleteUserAuditWriterCannotBeReused(t *testing.T) {
	db, script := newDeleteWriterDB(t)
	execution := newDeleteWriterExecution(t, func() int64 { return 7001 })
	if err := execution.recordAuditFacts(61, 6001, models.UserStatusActive); err != nil {
		t.Fatal(err)
	}
	first := db.Begin()
	if err := execution.Write(context.Background(), first, deleteAuditEvent()); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit().Error; err != nil {
		t.Fatal(err)
	}
	copied := *execution
	second := db.Begin()
	if err := copied.Write(context.Background(), second, deleteAuditEvent()); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("reuse error = %v", err)
	}
	_ = second.Rollback().Error
	if script.committedCount() != 1 {
		t.Fatalf("committed inserts = %d, want 1", script.committedCount())
	}
}

func TestAdminActionOutboxWriterPersistsExactPendingRow(t *testing.T) {
	db, script := newDeleteWriterDB(t)
	writer, err := NewAdminActionOutboxWriter(func() int64 { return 7002 }, deleteWriterClock(8001))
	if err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if err := writer.Write(context.Background(), tx, deleteOutboxEvent()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	insert := script.singleCommittedInsert(t, "admin_action_outbox")
	values := insert.columnValues(t)
	want := map[string]any{
		"guid": int64(7002), "created_at": int64(8001), "created_by": int64(41),
		"updated_at": int64(8001), "updated_by": int64(41), "is_deleted": int64(0),
		"operation_id": int64(31), "public_ref": deleteWriterPublicRef,
		"action": int64(actionsecurity.ActionUsersDelete), "target_kind": int64(actionsecurity.TargetUser),
		"target_guid": int64(6001), "state": int64(models.OperationSucceeded), "failure_code": nil, "result_kind": models.ResultUser, "result_guid": int64(6001),
		"delivery_state": int64(models.DeliveryPending), "available_at": int64(8001),
		"delivered_at": nil, "attempt_count": int64(0),
	}
	if len(values) != len(want) {
		t.Fatalf("outbox columns = %v, want exact row %v", values, want)
	}
	for column, expected := range want {
		if fmt.Sprint(values[column]) != fmt.Sprint(expected) {
			t.Errorf("outbox %s = %v, want %v", column, values[column], expected)
		}
	}
	encoded, _ := json.Marshal(values)
	for _, forbidden := range []string{"reason", "password", "ticket", "hmac", "session_guid", "key"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Errorf("outbox contains forbidden data: %s", encoded)
		}
	}
}

func TestAdminActionOutboxWriterPersistsFailedTerminalWithoutResult(t *testing.T) {
	db, script := newDeleteWriterDB(t)
	writer, err := NewAdminActionOutboxWriter(func() int64 { return 7005 }, deleteWriterClock(8001))
	if err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	if err := writer.Write(context.Background(), tx, deleteFailedOutboxEvent()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	values := script.singleCommittedInsert(t, "admin_action_outbox").columnValues(t)
	if fmt.Sprint(values["state"]) != "3" || fmt.Sprint(values["failure_code"]) != fmt.Sprint(models.FailureTargetVersionConflict) || values["result_guid"] != nil {
		t.Fatalf("failed outbox state/failure/result = %v/%v/%v", values["state"], values["failure_code"], values["result_guid"])
	}
}

func TestAdminActionOutboxWriterRejectsInvalidInputAndSanitizesDuplicate(t *testing.T) {
	db, script := newDeleteWriterDB(t)
	writer, err := NewAdminActionOutboxWriter(func() int64 { return 7002 }, deleteWriterClock(8001))
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Write(context.Background(), db, deleteOutboxEvent()); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("root DB error = %v", err)
	}
	failed := deleteOutboxEvent()
	failed.State = models.OperationFailed
	failed.ResultKind = nil
	failed.ResultGUID = nil
	tx := db.Begin()
	if err := writer.Write(context.Background(), tx, failed); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("failed event error = %v", err)
	}
	_ = tx.Rollback().Error

	first := db.Begin()
	if err := writer.Write(context.Background(), first, deleteOutboxEvent()); err != nil {
		t.Fatal(err)
	}
	if err := first.Commit().Error; err != nil {
		t.Fatal(err)
	}
	second := db.Begin()
	err = writer.Write(context.Background(), second, deleteOutboxEvent())
	if !errors.Is(err, ErrActionOperationUnavailable) || strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "private") {
		t.Fatalf("duplicate error was not sanitized: %v", err)
	}
	_ = second.Rollback().Error
	if script.committedCount() != 1 {
		t.Fatalf("committed inserts = %d, want 1", script.committedCount())
	}
}

func TestDeleteUserWritersReturnFixedErrorAndAllowCallerRollback(t *testing.T) {
	tests := []struct {
		table string
		call  func(*testing.T, *gorm.DB) error
	}{
		{"audit_logs", func(t *testing.T, tx *gorm.DB) error {
			execution := newDeleteWriterExecution(t, func() int64 { return 7001 })
			if err := execution.recordAuditFacts(61, 6001, models.UserStatusActive); err != nil {
				t.Fatal(err)
			}
			return execution.Write(context.Background(), tx, deleteAuditEvent())
		}},
		{"admin_action_outbox", func(t *testing.T, tx *gorm.DB) error {
			writer, err := NewAdminActionOutboxWriter(func() int64 { return 7002 }, deleteWriterClock(8001))
			if err != nil {
				t.Fatal(err)
			}
			return writer.Write(context.Background(), tx, deleteOutboxEvent())
		}},
	}
	for _, tc := range tests {
		t.Run(tc.table, func(t *testing.T) {
			db, script := newDeleteWriterDB(t)
			script.failInsert = tc.table
			tx := db.Begin()
			err := tc.call(t, tx)
			if !errors.Is(err, ErrActionOperationUnavailable) || err.Error() != ErrActionOperationUnavailable.Error() {
				t.Fatalf("writer error = %v", err)
			}
			if rollbackErr := tx.Rollback().Error; rollbackErr != nil {
				t.Fatalf("caller rollback failed: %v", rollbackErr)
			}
			if script.committedCount() != 0 {
				t.Fatal("failed writer committed a row")
			}
		})
	}
}

func TestDeleteUserWritersRejectForgedPositiveSessionGUID(t *testing.T) {
	tests := []struct {
		name string
		call func(*testing.T, *gorm.DB) error
	}{
		{"audit", func(t *testing.T, tx *gorm.DB) error {
			execution := newDeleteWriterExecution(t, func() int64 { return 7006 })
			if err := execution.recordAuditFacts(61, 6001, models.UserStatusActive); err != nil {
				t.Fatal(err)
			}
			event := deleteAuditEvent()
			event.SessionGUID = 5002
			return execution.Write(context.Background(), tx, event)
		}},
		{"outbox", func(t *testing.T, tx *gorm.DB) error {
			writer, err := NewAdminActionOutboxWriter(func() int64 { return 7007 }, deleteWriterClock(8001))
			if err != nil {
				t.Fatal(err)
			}
			event := deleteOutboxEvent()
			event.SessionGUID = 5002
			return writer.Write(context.Background(), tx, event)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, script := newDeleteWriterDB(t)
			tx := db.Begin()
			err := tc.call(t, tx)
			if !errors.Is(err, ErrActionOperationUnavailable) || err.Error() != ErrActionOperationUnavailable.Error() {
				t.Fatalf("forged session error = %v", err)
			}
			if rollbackErr := tx.Rollback().Error; rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			if script.committedCount() != 0 {
				t.Fatal("forged session persisted a row")
			}
			script.assertSafeSessionLookup(t, 45, 5002)
		})
	}
}

func newDeleteWriterExecution(t *testing.T, nextGUID func() int64) *DeleteUserExecution {
	t.Helper()
	execution, err := NewDeleteUserExecution(deleteWriterIntent(), nextGUID, deleteWriterClock(8001), createAccountTestCrypto(t))
	if err != nil {
		t.Fatal(err)
	}
	return execution
}

func deleteAuditEvent() ActionAuditEvent {
	target := int64(6001)
	result := int64(6001)
	kind := models.ResultUser
	return ActionAuditEvent{PublicRef: deleteWriterPublicRef, ActorGUID: 4001, SessionGUID: 5001,
		Action: actionsecurity.ActionUsersDelete, TargetKind: actionsecurity.TargetUser, TargetGUID: &target,
		State: models.OperationSucceeded, ResultKind: &kind, ResultGUID: &result, OccurredAt: 8001}
}

func deleteFailedAuditEvent() ActionAuditEvent {
	event := deleteAuditEvent()
	failure := models.FailureTargetVersionConflict
	event.State = models.OperationFailed
	event.Failure = &failure
	event.ResultKind = nil
	event.ResultGUID = nil
	return event
}

func deleteOutboxEvent() ActionOutboxEvent {
	audit := deleteAuditEvent()
	return ActionOutboxEvent{PublicRef: audit.PublicRef, ActorGUID: audit.ActorGUID, SessionGUID: audit.SessionGUID,
		Action: audit.Action, TargetKind: audit.TargetKind, TargetGUID: audit.TargetGUID, State: audit.State,
		Failure: audit.Failure, ResultKind: audit.ResultKind, ResultGUID: audit.ResultGUID, OccurredAt: audit.OccurredAt}
}

func deleteFailedOutboxEvent() ActionOutboxEvent {
	audit := deleteFailedAuditEvent()
	return ActionOutboxEvent{PublicRef: audit.PublicRef, ActorGUID: audit.ActorGUID, SessionGUID: audit.SessionGUID,
		Action: audit.Action, TargetKind: audit.TargetKind, TargetGUID: audit.TargetGUID, State: audit.State,
		Failure: audit.Failure, ResultKind: audit.ResultKind, ResultGUID: audit.ResultGUID, OccurredAt: audit.OccurredAt}
}

const deleteWriterDriverName = "porsche_delete_action_writers"

var (
	deleteWriterDriverOnce sync.Once
	deleteWriterDriverSeq  atomic.Uint64
	deleteWriterScripts    sync.Map
)

type deleteWriterInsert struct {
	query string
	args  []driver.NamedValue
}

type deleteWriterQuery struct {
	query string
	args  []driver.NamedValue
}

func (insert deleteWriterInsert) columnValues(t *testing.T) map[string]any {
	t.Helper()
	open := strings.Index(insert.query, "(")
	close := strings.Index(insert.query, ") VALUES")
	if open < 0 || close <= open {
		t.Fatalf("unrecognized insert SQL: %s", insert.query)
	}
	columns := strings.Split(insert.query[open+1:close], ",")
	if len(columns) != len(insert.args) {
		t.Fatalf("columns=%d args=%d SQL=%s", len(columns), len(insert.args), insert.query)
	}
	values := make(map[string]any, len(columns))
	for i, column := range columns {
		values[strings.Trim(strings.TrimSpace(column), "`")] = insert.args[i].Value
	}
	return values
}

type deleteWriterScript struct {
	mu           sync.Mutex
	operation    models.AdminOperation
	actor        models.User
	session      models.Session
	verification models.AdminActionVerification
	queries      []deleteWriterQuery
	committed    []deleteWriterInsert
	failInsert   string
}

func (script *deleteWriterScript) assertSafeSessionLookup(t *testing.T, internalID, forgedGUID int64) {
	t.Helper()
	script.mu.Lock()
	defer script.mu.Unlock()
	var matched []deleteWriterQuery
	for _, query := range script.queries {
		if strings.Contains(query.query, "FROM `user_sessions`") {
			matched = append(matched, query)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("session queries = %d, want 1: %v", len(matched), script.queries)
	}
	query := matched[0]
	if !strings.HasPrefix(query.query, "SELECT `id`,`guid`,`user_id` FROM `user_sessions` WHERE id = ?") ||
		strings.Contains(query.query, "`sid`") || strings.Contains(query.query, "session_guid") {
		t.Fatalf("unsafe session query: %s", query.query)
	}
	if len(query.args) == 0 || fmt.Sprint(query.args[0].Value) != fmt.Sprint(internalID) {
		t.Fatalf("session selector args = %v, want internal id %d", query.args, internalID)
	}
	for _, arg := range query.args {
		if fmt.Sprint(arg.Value) == fmt.Sprint(forgedGUID) {
			t.Fatalf("session lookup used public GUID: %v", query.args)
		}
	}
}

func (script *deleteWriterScript) committedCount() int {
	script.mu.Lock()
	defer script.mu.Unlock()
	return len(script.committed)
}

func (script *deleteWriterScript) singleCommittedInsert(t *testing.T, table string) deleteWriterInsert {
	t.Helper()
	script.mu.Lock()
	defer script.mu.Unlock()
	var matched []deleteWriterInsert
	for _, insert := range script.committed {
		if strings.Contains(insert.query, "INSERT INTO `"+table+"`") {
			matched = append(matched, insert)
		}
	}
	if len(matched) != 1 {
		t.Fatalf("%s inserts = %d; all=%v", table, len(matched), script.committed)
	}
	return matched[0]
}

type deleteWriterDriver struct{}
type deleteWriterConn struct {
	script *deleteWriterScript
	tx     *deleteWriterTx
}
type deleteWriterTx struct {
	conn    *deleteWriterConn
	inserts []deleteWriterInsert
}
type deleteWriterRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}
type deleteWriterResult int64

func (deleteWriterDriver) Open(name string) (driver.Conn, error) {
	value, ok := deleteWriterScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown delete writer script")
	}
	return &deleteWriterConn{script: value.(*deleteWriterScript)}, nil
}

func (conn *deleteWriterConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare disabled")
}
func (conn *deleteWriterConn) Close() error { return nil }
func (conn *deleteWriterConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}
func (conn *deleteWriterConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if conn.tx != nil {
		return nil, errors.New("nested transaction")
	}
	conn.tx = &deleteWriterTx{conn: conn}
	return conn.tx, nil
}
func (conn *deleteWriterConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case actionsecurity.Action:
		value.Value = int64(typed)
	case actionsecurity.TargetKind:
		value.Value = int64(typed)
	case models.AdminOperationState:
		value.Value = int64(typed)
	case models.AdminActionDeliveryState:
		value.Value = int64(typed)
	case models.JSONMap:
		converted, err := typed.Value()
		if err != nil {
			return err
		}
		value.Value = converted
	case *int64:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = *typed
		}
	case *int:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = int64(*typed)
		}
	case *string:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = *typed
		}
	}
	return nil
}

func (conn *deleteWriterConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	conn.script.mu.Lock()
	defer conn.script.mu.Unlock()
	if conn.tx == nil {
		return nil, errors.New("private query outside transaction")
	}
	conn.script.queries = append(conn.script.queries, deleteWriterQuery{query: query, args: append([]driver.NamedValue(nil), args...)})
	switch {
	case strings.Contains(query, "FROM `admin_operations`"):
		op := conn.script.operation
		if strings.Contains(query, "`request_hmac`") {
			return deleteWriterRow([]string{"id", "actor_user_id", "actor_auth_version", "session_id", "action", "verification_id", "state", "public_ref", "request_hmac"},
				[]driver.Value{op.ID, op.ActorUserID, int64(op.ActorAuthVersion), op.SessionID, int64(op.Action),
					pointerDriverValue(op.VerificationID), int64(op.State), op.PublicRef, op.RequestHMAC}), nil
		}
		return deleteWriterRow([]string{"id", "actor_user_id", "actor_auth_version", "session_id", "action", "verification_id", "state", "public_ref"},
			[]driver.Value{op.ID, op.ActorUserID, int64(op.ActorAuthVersion), op.SessionID, int64(op.Action),
				pointerDriverValue(op.VerificationID), int64(op.State), op.PublicRef}), nil
	case strings.Contains(query, "FROM `users`"):
		actor := conn.script.actor
		return deleteWriterRow([]string{"id", "guid"}, []driver.Value{actor.ID, actor.Guid}), nil
	case strings.Contains(query, "FROM `user_sessions`"):
		session := conn.script.session
		return deleteWriterRow([]string{"id", "guid", "user_id"}, []driver.Value{session.ID, session.Guid, session.UserID}), nil
	case strings.Contains(query, "FROM `admin_action_verifications`"):
		verification := conn.script.verification
		if strings.Contains(query, "`intent_hmac`") {
			return deleteWriterRow([]string{"id", "actor_user_id", "actor_auth_version", "session_id", "action", "target_kind", "target_guid", "intent_hmac", "consumed_at", "is_deleted"},
				[]driver.Value{verification.ID, verification.ActorUserID, int64(verification.ActorAuthVersion), verification.SessionID,
					int64(verification.Action), int64(verification.TargetKind), pointerDriverValue(verification.TargetGUID), verification.IntentHMAC,
					pointerDriverValue(verification.ConsumedAt), int64(verification.IsDeleted)}), nil
		}
		return deleteWriterRow([]string{"id", "actor_user_id", "actor_auth_version", "session_id", "action", "target_kind", "target_guid", "consumed_at", "is_deleted"},
			[]driver.Value{verification.ID, verification.ActorUserID, int64(verification.ActorAuthVersion), verification.SessionID,
				int64(verification.Action), int64(verification.TargetKind), pointerDriverValue(verification.TargetGUID),
				pointerDriverValue(verification.ConsumedAt), int64(verification.IsDeleted)}), nil
	default:
		return nil, fmt.Errorf("private unexpected query: %s", query)
	}
}

func (conn *deleteWriterConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	conn.script.mu.Lock()
	defer conn.script.mu.Unlock()
	if conn.tx == nil {
		return nil, errors.New("private write outside transaction")
	}
	table := ""
	for _, candidate := range []string{"audit_logs", "admin_action_outbox"} {
		if strings.Contains(query, "INSERT INTO `"+candidate+"`") {
			table = candidate
			break
		}
	}
	if table == "" {
		return nil, fmt.Errorf("private unexpected write: %s", query)
	}
	if conn.script.failInsert == table {
		return nil, errors.New("private secret database failure")
	}
	if table == "admin_action_outbox" {
		for _, existing := range conn.script.committed {
			if strings.Contains(existing.query, "INSERT INTO `admin_action_outbox`") {
				return nil, errors.New("private duplicate operation")
			}
		}
	}
	copyArgs := append([]driver.NamedValue(nil), args...)
	conn.tx.inserts = append(conn.tx.inserts, deleteWriterInsert{query: query, args: copyArgs})
	return deleteWriterResult(1), nil
}

func (tx *deleteWriterTx) Commit() error {
	tx.conn.script.mu.Lock()
	defer tx.conn.script.mu.Unlock()
	tx.conn.script.committed = append(tx.conn.script.committed, tx.inserts...)
	tx.conn.tx = nil
	return nil
}
func (tx *deleteWriterTx) Rollback() error {
	tx.conn.script.mu.Lock()
	defer tx.conn.script.mu.Unlock()
	tx.conn.tx = nil
	return nil
}
func (rows *deleteWriterRows) Columns() []string { return rows.columns }
func (rows *deleteWriterRows) Close() error      { return nil }
func (rows *deleteWriterRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}
func (result deleteWriterResult) LastInsertId() (int64, error) { return 1, nil }
func (result deleteWriterResult) RowsAffected() (int64, error) { return int64(result), nil }

func deleteWriterRow(columns []string, values []driver.Value) *deleteWriterRows {
	return &deleteWriterRows{columns: columns, values: [][]driver.Value{values}}
}

func pointerDriverValue(value *int64) driver.Value {
	if value == nil {
		return nil
	}
	return *value
}

func newDeleteWriterDB(t *testing.T) (*gorm.DB, *deleteWriterScript) {
	t.Helper()
	deleteWriterDriverOnce.Do(func() { sql.Register(deleteWriterDriverName, deleteWriterDriver{}) })
	verificationID := int64(51)
	targetGUID := int64(6001)
	sessionID := int64(45)
	consumedAt := int64(8001)
	script := &deleteWriterScript{
		operation: models.AdminOperation{ID: 31, ActorUserID: 41, ActorAuthVersion: 3, SessionID: sessionID,
			Action: int(actionsecurity.ActionUsersDelete), VerificationID: &verificationID, State: models.OperationProcessing, PublicRef: deleteWriterPublicRef},
		actor:   models.User{ID: 41, AuditFields: models.AuditFields{Guid: 4001}},
		session: models.Session{ID: sessionID, AuditFields: models.AuditFields{Guid: 5001}, UserID: 41},
		verification: models.AdminActionVerification{ID: verificationID, ActorUserID: 41, ActorAuthVersion: 3, SessionID: sessionID,
			Action: int(actionsecurity.ActionUsersDelete), TargetKind: int(actionsecurity.TargetUser), TargetGUID: &targetGUID,
			ConsumedAt: &consumedAt, AuditFields: models.AuditFields{IsDeleted: 1}},
	}
	dsn := fmt.Sprintf("delete-writer-%d", deleteWriterDriverSeq.Add(1))
	deleteWriterScripts.Store(dsn, script)
	t.Cleanup(func() { deleteWriterScripts.Delete(dsn) })
	sqlDB, err := sql.Open(deleteWriterDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	return db, script
}

func decodeDeleteWriterJSON(t *testing.T, value any) map[string]any {
	t.Helper()
	var raw []byte
	switch typed := value.(type) {
	case []byte:
		raw = typed
	case string:
		raw = []byte(typed)
	default:
		t.Fatalf("detail value type = %T", value)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}
