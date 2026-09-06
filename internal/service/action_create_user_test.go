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

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const createAccountTestPasswordHash = "$argon2id$v=19$m=65536,t=3,p=4$MDEyMzQ1Njc4OWFiY2RlZg$MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY"

func TestCreateAccountExecutionOwnsInputsAndRedactsHash(t *testing.T) {
	descriptor := createAccountTestDescriptor(t, actionsecurity.ActionUsersCreateAdmin)
	nickname := "Alice"
	groupGUID := int64(7001)
	modelsInput := make([]string, 0, 2)
	overrides := []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}
	hash := []byte(createAccountTestPasswordHash)
	intent := actionsecurity.CreateAccountIntent{
		Username: "alice", Nickname: &nickname, Role: "admin", GroupGUID: &groupGUID,
		PlanType: int(models.PlanProfessional), AllowedModels: modelsInput, DailyCallLimit: 100, Overrides: overrides,
	}
	execution, err := NewCreateAccountExecution(descriptor, intent, hash, CreateAccountRequestMetadata{
		RequestID: "request.create-1", TrustedIP: "203.0.113.7",
	}, func() int64 { return 9001 }, persistence.SystemClock())
	if err != nil {
		t.Fatal(err)
	}

	nickname = "mutated"
	groupGUID = 7002
	modelsInput = append(modelsInput, "mutated")
	overrides[0].Capability = "users.create"
	hash[0] = '!'
	if execution.intent.Nickname == nil || *execution.intent.Nickname != "Alice" || execution.intent.GroupGUID == nil || *execution.intent.GroupGUID != 7001 ||
		len(execution.intent.AllowedModels) != 0 || execution.intent.Overrides[0].Capability != "users.read" || string(execution.state.passwordHash) != createAccountTestPasswordHash {
		t.Fatal("constructor retained caller-owned mutable input")
	}

	for _, rendered := range []string{fmt.Sprint(execution), fmt.Sprintf("%+v", execution), fmt.Sprintf("%#v", execution)} {
		if strings.Contains(rendered, "private-hash") || strings.Contains(rendered, "$argon2id$") {
			t.Fatalf("hash leaked through formatting: %s", rendered)
		}
	}
	encoded, err := json.Marshal(execution)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private-hash") || string(encoded) != "{}" {
		t.Fatalf("hash leaked through JSON: %s", encoded)
	}
}

func TestCreateAccountExecutionCreatesOrdinaryUserAndRegistrationAudit(t *testing.T) {
	db, script := newCreateAccountTestDB(t)
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
		Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, createAccountTestGUIDs(9001, 9002))

	tx := db.Begin()
	outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate))
	if err != nil {
		t.Fatalf("execute: %v; calls=%v", err, script.observedKinds())
	}
	if outcome.Failure != nil || outcome.HTTPStatus != 201 || outcome.ResultKind != models.ResultUser || outcome.ResultGUID == nil || *outcome.ResultGUID != 9001 {
		t.Fatalf("outcome = %#v", outcome)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if got := script.committedKinds(); fmt.Sprint(got) != fmt.Sprint([]string{
		"query:users", "query:user_sessions", "query:user_permission_heads", "query:user_permission_overrides",
		"query:business_groups", "query:users", "exec:users", "exec:auth_audit_events",
	}) {
		t.Fatalf("call order = %v", got)
	}
	user := createAccountInsertValues(t, script.committedTableCall("users", 0))
	for column, want := range map[string]any{
		"guid": int64(9001), "created_at": int64(8001), "created_by": int64(41), "updated_at": int64(8001), "updated_by": int64(41), "is_deleted": int64(0),
		"group_id": int64(61), "username": "alice", "nickname": nil, "phone": nil, "real_name": nil, "id_card_hash": nil, "is_verified": false,
		"plan_type": int64(models.PlanFree), "status": int64(models.UserStatusActive), "role": int64(models.UserRoleUser), "auth_version": int64(1),
		"last_login_at": nil, "allowed_models": "[]", "daily_call_limit": int64(100), "daily_calls_used": int64(0), "daily_calls_reset_at": nil, "total_tokens_used": int64(0),
	} {
		if fmt.Sprint(user[column]) != fmt.Sprint(want) {
			t.Errorf("user %s = %v, want %v", column, user[column], want)
		}
	}
	if fmt.Sprint(user["password_hash"]) != createAccountTestPasswordHash {
		t.Fatal("user password hash was not the precomputed value")
	}
	audit := createAccountInsertValues(t, script.committedTableCall("auth_audit_events", 0))
	if audit["guid"] != int64(9002) || audit["user_id"] != int64(501) || audit["event_type"] != int64(models.AuthAuditEventRegistered) ||
		audit["created_by"] != int64(41) || audit["ip"] != "203.0.113.7" || audit["login_method"] != int64(models.LoginMethodPassword) {
		t.Fatalf("registration audit = %#v", audit)
	}
	if script.containsSQL("amount") || script.containsSQL("balance") {
		t.Fatal("create path referenced forbidden amount/balance storage")
	}
	execution.state.mu.Lock()
	defer execution.state.mu.Unlock()
	if len(execution.state.passwordHash) != 0 {
		t.Fatal("execution retained password hash after consumer completion")
	}
}

func TestCreateAccountExecutionCreatesAdminPolicyAndCanonicalOverrides(t *testing.T) {
	db, script := newCreateAccountTestDB(t)
	groupGUID := int64(6002)
	script.groups = []models.BusinessGroup{{ID: 62, AuditFields: models.AuditFields{Guid: groupGUID}, Key: "enterprise", DisplayName: "Enterprise", Status: models.BusinessGroupStatusActive}}
	intent := actionsecurity.CreateAccountIntent{
		Username: "managed_admin", Role: "admin", GroupGUID: &groupGUID, PlanType: int(models.PlanEnterprise), AllowedModels: []string{}, DailyCallLimit: 100,
		Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}, {Capability: "users.read", Effect: 3}},
	}
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreateAdmin, intent, createAccountTestGUIDs(9101, 9102, 9103, 9104, 9105))
	tx := db.Begin()
	outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreateAdmin))
	if err != nil {
		t.Fatalf("execute: %v; calls=%v", err, script.observedKinds())
	}
	if outcome.Failure != nil || outcome.HTTPStatus != 201 || outcome.ResultGUID == nil || *outcome.ResultGUID != 9101 {
		t.Fatalf("outcome = %#v", outcome)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	if got := script.execCount("user_permission_heads"); got != 1 {
		t.Fatalf("permission head writes = %d", got)
	}
	if got := script.execCount("user_permission_overrides"); got != 2 {
		t.Fatalf("permission override writes = %d", got)
	}
	user := createAccountInsertValues(t, script.committedTableCall("users", 0))
	if fmt.Sprint(user["allowed_models"]) != "[]" || fmt.Sprint(user["daily_call_limit"]) != "100" {
		t.Fatalf("resolved create defaults drifted in persistence: %#v", user)
	}
	head := createAccountInsertValues(t, script.committedTableCall("user_permission_heads", 0))
	if head["guid"] != int64(9102) || head["user_id"] != int64(501) || head["policy_version"] != int64(1) ||
		fmt.Sprint(head["catalog_version"]) != fmt.Sprint(models.PermissionCatalogVersion) || fmt.Sprint(head["rule_count"]) != "2" || head["created_by"] != int64(41) {
		t.Fatalf("permission head = %#v", head)
	}
	wantOverrides := []struct {
		guid       int64
		capability string
		effect     int
	}{{9103, "users.read", 3}, {9104, "users.sessions.read", 2}}
	for index, want := range wantOverrides {
		row := createAccountInsertValues(t, script.committedTableCall("user_permission_overrides", index))
		capability, _ := models.PermissionCapabilityCode(want.capability)
		if row["guid"] != want.guid || row["user_id"] != int64(501) || row["policy_version"] != int64(1) ||
			fmt.Sprint(row["capability"]) != fmt.Sprint(capability) || fmt.Sprint(row["effect"]) != fmt.Sprint(want.effect) || row["created_by"] != int64(41) {
			t.Fatalf("override %d = %#v", index, row)
		}
	}
	if auth := createAccountInsertValues(t, script.committedTableCall("auth_audit_events", 0)); auth["guid"] != int64(9105) {
		t.Fatalf("auth audit GUID ordering = %#v", auth)
	}
}

func TestCreateAccountAdminWithoutOverridesStillCreatesVersionOneHead(t *testing.T) {
	db, script := newCreateAccountTestDB(t)
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreateAdmin, actionsecurity.CreateAccountIntent{
		Username: "managed_admin", Role: "admin", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, createAccountTestGUIDs(9151, 9152, 9153))
	tx := db.Begin()
	if outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreateAdmin)); err != nil || outcome.Failure != nil {
		t.Fatalf("consume = %#v/%v", outcome, err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	head := createAccountInsertValues(t, script.committedTableCall("user_permission_heads", 0))
	if fmt.Sprint(head["policy_version"]) != "1" || fmt.Sprint(head["catalog_version"]) != "1" || fmt.Sprint(head["rule_count"]) != "0" || script.execCount("user_permission_overrides") != 0 {
		t.Fatalf("zero-rule admin policy = %#v", head)
	}
}

func TestCreateAccountWritersPersistRedactedManagementAuditAndResultOutbox(t *testing.T) {
	db, script := newCreateAccountTestDB(t)
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
		Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, createAccountTestGUIDs(9201, 9202, 9203))
	operation := validCreateAccountTestOperation(actionsecurity.ActionUsersCreate)
	tx := db.Begin()
	outcome, err := execution.Execute(context.Background(), tx, operation)
	if err != nil || outcome.ResultGUID == nil {
		t.Fatalf("consumer setup = %#v/%v", outcome, err)
	}
	resultKind := models.ResultUser
	event := ActionAuditEvent{PublicRef: operation.PublicRef, ActorGUID: script.actor.Guid, SessionGUID: script.session.Guid,
		Action: actionsecurity.ActionUsersCreate, TargetKind: actionsecurity.TargetNone, State: models.OperationSucceeded,
		ResultKind: &resultKind, ResultGUID: outcome.ResultGUID, OccurredAt: 8001}
	if err := execution.Write(context.Background(), tx, event); err != nil {
		t.Fatal(err)
	}
	outbox, err := NewCreateAccountOutboxWriter(createAccountTestGUIDs(9204), &createAccountTestClock{now: 8001})
	if err != nil {
		t.Fatal(err)
	}
	if err := outbox.Write(context.Background(), tx, ActionOutboxEvent{
		PublicRef: event.PublicRef, ActorGUID: event.ActorGUID, SessionGUID: event.SessionGUID, Action: event.Action,
		TargetKind: event.TargetKind, State: event.State, ResultKind: event.ResultKind, ResultGUID: event.ResultGUID, OccurredAt: event.OccurredAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	audit := createAccountInsertValues(t, script.committedTableCall("audit_logs", 0))
	if audit["guid"] != int64(9203) || audit["user_id"] != int64(41) || audit["action"] != "users.create" ||
		audit["resource"] != "users/9201" || audit["ip"] != "203.0.113.7" {
		t.Fatalf("management audit = %#v", audit)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(audit["detail"])), &detail); err != nil {
		t.Fatalf("decode management detail: %v; audit=%#v", err, audit)
	}
	if detail["target_guid"] != float64(9201) || detail["role"] != "user" || detail["group_key"] != "default" || detail["plan"] != "free" ||
		detail["request_id"] != "request.create-1" || detail["operation_ref"] != operation.PublicRef || detail["password"] != "set" {
		t.Fatalf("management detail = %#v", detail)
	}
	encoded := fmt.Sprint(audit["detail"])
	for _, forbidden := range []string{"MDEyMzQ1Njc4OWFiY2RlZg", "$argon2id$", "ticket", "idempotency", "password_length", "password_hash"} {
		if strings.Contains(strings.ToLower(encoded), strings.ToLower(forbidden)) {
			t.Fatalf("management audit leaked %q: %s", forbidden, encoded)
		}
	}
	outboxRow := createAccountInsertValues(t, script.committedTableCall("admin_action_outbox", 0))
	if outboxRow["guid"] != int64(9204) || outboxRow["operation_id"] != int64(31) || fmt.Sprint(outboxRow["action"]) != fmt.Sprint(actionsecurity.ActionUsersCreate) ||
		fmt.Sprint(outboxRow["target_kind"]) != fmt.Sprint(actionsecurity.TargetNone) || outboxRow["target_guid"] != nil || outboxRow["result_guid"] != int64(9201) ||
		fmt.Sprint(outboxRow["delivery_state"]) != fmt.Sprint(models.DeliveryPending) {
		t.Fatalf("create outbox = %#v", outboxRow)
	}
}

func TestCreateAccountResultUserIsOwnedAndUnavailableBeforeCommit(t *testing.T) {
	db, _ := newCreateAccountTestDB(t)
	nickname := "Alice"
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
		Username: "alice", Nickname: &nickname, Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, createAccountTestGUIDs(9301, 9302))
	if result, ok := execution.ResultUser(); ok || result != nil {
		t.Fatal("result was visible before consumer success")
	}
	tx := db.Begin()
	if outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate)); err != nil || outcome.Failure != nil {
		t.Fatalf("consume = %#v/%v", outcome, err)
	}
	if result, ok := execution.ResultUser(); ok || result != nil {
		t.Fatal("result was visible before transaction commit confirmation")
	}
	if err := tx.Commit().Error; err != nil {
		t.Fatal(err)
	}
	execution.actionCommitConfirmed()
	result, ok := execution.ResultUser()
	if !ok || result == nil || result.GUID != "9301" || result.Username == nil || *result.Username != "alice" || result.Nickname == nil ||
		*result.Nickname != "Alice" || result.Group == nil || *result.Group != "default" || result.PlanType != "free" || result.Role != "user" ||
		result.Status != "active" || result.AuthVersion != 1 || result.CreatedAt != "1970-01-01T00:00:08.001Z" {
		t.Fatalf("created result = %#v", result)
	}
	*result.Username = "mutated"
	again, ok := execution.ResultUser()
	if !ok || again == nil || again.Username == nil || *again.Username != "alice" {
		t.Fatal("result projection exposed mutable stored state")
	}
}

func TestCreateAccountConstructorRejectsDescriptorRoleCatalogAndMetadataDrift(t *testing.T) {
	validIntent := actionsecurity.CreateAccountIntent{Username: "alice", Role: "admin", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	mutations := []struct {
		name   string
		mutate func(*actionsecurity.Descriptor, *actionsecurity.CreateAccountIntent, *CreateAccountRequestMetadata, *[]byte)
	}{
		{"descriptor name", func(d *actionsecurity.Descriptor, _ *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			d.Name = "users.create"
		}},
		{"descriptor capability", func(d *actionsecurity.Descriptor, _ *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			d.Capability = "users.read"
		}},
		{"descriptor root", func(d *actionsecurity.Descriptor, _ *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			d.RootOnly = false
		}},
		{"descriptor ticket", func(d *actionsecurity.Descriptor, _ *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			d.RequiresTicket = false
		}},
		{"descriptor target", func(d *actionsecurity.Descriptor, _ *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			d.TargetKind = actionsecurity.TargetUser
		}},
		{"descriptor encoder", func(d *actionsecurity.Descriptor, _ *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			d.Encode = nil
		}},
		{"role", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.Role = "user"
		}},
		{"raw password", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.Password = []byte("raw-password")
		}},
		{"unnormalized username", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.Username = " alice "
		}},
		{"unsorted models", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.AllowedModels = []string{"z", "a"}
		}},
		{"duplicate models", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.AllowedModels = []string{"a", "a"}
		}},
		{"non-default allowed models", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.AllowedModels = []string{"model-a"}
		}},
		{"zero daily limit", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.DailyCallLimit = 0
		}},
		{"non-default daily limit", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.DailyCallLimit = 101
		}},
		{"request id", func(_ *actionsecurity.Descriptor, _ *actionsecurity.CreateAccountIntent, m *CreateAccountRequestMetadata, _ *[]byte) {
			m.RequestID = "bad request"
		}},
		{"trusted ip", func(_ *actionsecurity.Descriptor, _ *actionsecurity.CreateAccountIntent, m *CreateAccountRequestMetadata, _ *[]byte) {
			m.TrustedIP = "not-an-ip"
		}},
		{"empty hash", func(_ *actionsecurity.Descriptor, _ *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, h *[]byte) {
			*h = nil
		}},
		{"unknown override", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "unknown", Effect: 2}}
		}},
		{"ungrantable override", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "users.quota.adjust", Effect: 2}}
		}},
		{"root only override", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "users.promote", Effect: 2}}
		}},
		{"duplicate override", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.read", Effect: 3}}
		}},
		{"invalid effect", func(_ *actionsecurity.Descriptor, i *actionsecurity.CreateAccountIntent, _ *CreateAccountRequestMetadata, _ *[]byte) {
			i.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 1}}
		}},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			descriptor := createAccountTestDescriptor(t, actionsecurity.ActionUsersCreateAdmin)
			intent := validIntent
			metadata := CreateAccountRequestMetadata{RequestID: "request-1", TrustedIP: "203.0.113.7"}
			hash := []byte(createAccountTestPasswordHash)
			test.mutate(&descriptor, &intent, &metadata, &hash)
			result, err := NewCreateAccountExecution(descriptor, intent, hash, metadata, func() int64 { return 1 }, &createAccountTestClock{now: 8001})
			if result != nil || !errors.Is(err, ErrActionOperationUnavailable) {
				t.Fatalf("invalid constructor result = %#v/%v", result, err)
			}
			for _, value := range hash {
				if value != 0 {
					t.Fatal("constructor error did not clear caller hash")
				}
			}
		})
	}

	hash := []byte(createAccountTestPasswordHash)
	ordinary := actionsecurity.CreateAccountIntent{Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
		Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}}
	if result, err := NewCreateAccountExecution(createAccountTestDescriptor(t, actionsecurity.ActionUsersCreate), ordinary, hash,
		CreateAccountRequestMetadata{RequestID: "request-1", TrustedIP: "203.0.113.7"}, func() int64 { return 1 }, &createAccountTestClock{now: 8001}); result != nil || err == nil {
		t.Fatal("ordinary creation accepted permission overrides")
	}
}

func TestCreateAccountConstructorRejectsRawOrMalformedPasswordHash(t *testing.T) {
	intent := actionsecurity.CreateAccountIntent{Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100}
	for _, raw := range []string{"A03-Strong-Password!", "$argon2id$malformed", ""} {
		hash := []byte(raw)
		result, err := NewCreateAccountExecution(createAccountTestDescriptor(t, actionsecurity.ActionUsersCreate), intent, hash,
			CreateAccountRequestMetadata{RequestID: "request-1", TrustedIP: "203.0.113.7"}, func() int64 { return 1 }, &createAccountTestClock{now: 8001})
		if result != nil || !errors.Is(err, ErrActionOperationUnavailable) {
			t.Fatalf("non-hash %q accepted: %#v/%v", raw, result, err)
		}
	}
}

func TestCreateAccountUsernameConflictIncludesActiveAndTombstone(t *testing.T) {
	for _, deleted := range []int{0, 1} {
		t.Run(fmt.Sprintf("deleted_%d", deleted), func(t *testing.T) {
			db, script := newCreateAccountTestDB(t)
			script.conflicts = []models.User{{ID: 99, AuditFields: models.AuditFields{Guid: 9901, IsDeleted: deleted}}}
			execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
				Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
			}, func() int64 { t.Fatal("GUID called for conflict"); return 0 })
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate))
			if err != nil || outcome.Failure == nil || *outcome.Failure != models.FailureConsumerValidation || outcome.HTTPStatus != 409 {
				t.Fatalf("conflict outcome = %#v/%v", outcome, err)
			}
			_ = tx.Rollback().Error
			if script.writeCount() != 0 || !script.queryContains("username = ?") || script.queryContains("username = ? AND is_deleted") {
				t.Fatalf("username conflict query/writes = %v/%d", script.observedKinds(), script.writeCount())
			}
			assertCreateHashCleared(t, execution)
		})
	}
}

func TestCreateAccountGroupResolutionRejectsMissingDeletedInactiveDuplicateAndCorrupt(t *testing.T) {
	valid := models.BusinessGroup{ID: 61, AuditFields: models.AuditFields{Guid: 6001}, Key: "default", DisplayName: "Default", Status: models.BusinessGroupStatusActive}
	tests := []struct {
		name   string
		groups []models.BusinessGroup
	}{
		{"missing", nil},
		{"deleted", []models.BusinessGroup{{ID: 61, AuditFields: models.AuditFields{Guid: 6001, IsDeleted: 1}, Key: "default", DisplayName: "Default", Status: models.BusinessGroupStatusActive}}},
		{"inactive", []models.BusinessGroup{{ID: 61, AuditFields: models.AuditFields{Guid: 6001}, Key: "default", DisplayName: "Default", Status: models.BusinessGroupStatusInactive}}},
		{"duplicate", []models.BusinessGroup{valid, {ID: 62, AuditFields: models.AuditFields{Guid: 6002}, Key: "default", DisplayName: "Duplicate", Status: models.BusinessGroupStatusActive}}},
		{"zero id", []models.BusinessGroup{{AuditFields: models.AuditFields{Guid: 6001}, Key: "default", DisplayName: "Default", Status: models.BusinessGroupStatusActive}}},
		{"zero guid", []models.BusinessGroup{{ID: 61, Key: "default", DisplayName: "Default", Status: models.BusinessGroupStatusActive}}},
		{"case key", []models.BusinessGroup{{ID: 61, AuditFields: models.AuditFields{Guid: 6001}, Key: "Default", DisplayName: "Default", Status: models.BusinessGroupStatusActive}}},
		{"empty display", []models.BusinessGroup{{ID: 61, AuditFields: models.AuditFields{Guid: 6001}, Key: "default", Status: models.BusinessGroupStatusActive}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, script := newCreateAccountTestDB(t)
			script.groups = test.groups
			execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
				Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
			}, func() int64 { t.Fatal("GUID called for invalid group"); return 0 })
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate))
			if err != nil || outcome.Failure == nil || *outcome.Failure != models.FailureConsumerValidation || outcome.HTTPStatus != 404 {
				t.Fatalf("group outcome = %#v/%v", outcome, err)
			}
			_ = tx.Rollback().Error
			if script.writeCount() != 0 {
				t.Fatal("invalid group wrote rows")
			}
		})
	}
}

func TestCreateAccountAdditionalGroupAndPlanCapabilitiesUseLockedPolicy(t *testing.T) {
	tests := []struct {
		name, capability string
		allow            bool
		plan             models.PlanType
		nonDefault       bool
	}{
		{"group denied", "users.group.change", false, models.PlanFree, true},
		{"group allowed", "users.group.change", true, models.PlanFree, true},
		{"plan denied", "users.plan.change", false, models.PlanProfessional, false},
		{"plan allowed", "users.plan.change", true, models.PlanProfessional, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, script := newCreateAccountTestDB(t)
			script.actor.Role = models.UserRoleAdmin
			actorID := script.actor.ID
			script.actorHead = &models.PermissionPolicyHead{ID: 70, AuditFields: models.AuditFields{Guid: 7001}, UserID: actorID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion}
			if test.allow {
				capability, _ := models.PermissionCapabilityCode(test.capability)
				script.actorHead.RuleCount = 1
				script.actorRules = []models.PermissionOverride{{ID: 71, AuditFields: models.AuditFields{Guid: 7101}, UserID: actorID, PolicyVersion: 1, Capability: capability, Effect: 2}}
			}
			var groupGUID *int64
			if test.nonDefault {
				value := int64(6002)
				groupGUID = &value
				script.groups = []models.BusinessGroup{{ID: 62, AuditFields: models.AuditFields{Guid: value}, Key: "custom", DisplayName: "Custom", Status: models.BusinessGroupStatusActive}}
			}
			execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
				Username: "alice", Role: "user", GroupGUID: groupGUID, PlanType: int(test.plan), AllowedModels: []string{}, DailyCallLimit: 100,
			}, createAccountTestGUIDs(9401, 9402))
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate))
			if err != nil {
				t.Fatalf("execute: %v; calls=%v", err, script.observedKinds())
			}
			if test.allow {
				if outcome.Failure != nil || outcome.HTTPStatus != 201 {
					t.Fatalf("allowed outcome = %#v", outcome)
				}
			} else if outcome.Failure == nil || *outcome.Failure != models.FailureActionRejected || outcome.HTTPStatus != 403 {
				t.Fatalf("denied outcome = %#v", outcome)
			}
			_ = tx.Rollback().Error
		})
	}
}

func TestCreateAccountAdminDescriptorRequiresFreshRootAuthority(t *testing.T) {
	db, script := newCreateAccountTestDB(t)
	script.actor.Role = models.UserRoleAdmin
	script.actorHead = &models.PermissionPolicyHead{ID: 70, AuditFields: models.AuditFields{Guid: 7001}, UserID: script.actor.ID, PolicyVersion: 1, CatalogVersion: models.PermissionCatalogVersion, RuleCount: 0}
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreateAdmin, actionsecurity.CreateAccountIntent{
		Username: "managed_admin", Role: "admin", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, func() int64 { t.Fatal("GUID called for non-root admin creation"); return 0 })
	tx := db.Begin()
	outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreateAdmin))
	if err != nil || outcome.Failure == nil || *outcome.Failure != models.FailureActionRejected || outcome.HTTPStatus != 403 || script.writeCount() != 0 {
		t.Fatalf("non-root admin-create outcome/writes = %#v/%v/%d", outcome, err, script.writeCount())
	}
	_ = tx.Rollback().Error
}

func TestCreateAccountExplicitGroupRejectsMissingDeletedInactiveDuplicateAndCorrupt(t *testing.T) {
	guid := int64(6201)
	valid := models.BusinessGroup{ID: 62, AuditFields: models.AuditFields{Guid: guid}, Key: "custom", DisplayName: "Custom", Status: models.BusinessGroupStatusActive}
	tests := []struct {
		name   string
		groups []models.BusinessGroup
	}{
		{"missing", nil},
		{"deleted", []models.BusinessGroup{{ID: 62, AuditFields: models.AuditFields{Guid: guid, IsDeleted: 1}, Key: "custom", DisplayName: "Custom", Status: models.BusinessGroupStatusActive}}},
		{"inactive", []models.BusinessGroup{{ID: 62, AuditFields: models.AuditFields{Guid: guid}, Key: "custom", DisplayName: "Custom", Status: models.BusinessGroupStatusInactive}}},
		{"duplicate", []models.BusinessGroup{valid, {ID: 63, AuditFields: models.AuditFields{Guid: guid}, Key: "other", DisplayName: "Other", Status: models.BusinessGroupStatusActive}}},
		{"guid mismatch", []models.BusinessGroup{{ID: 62, AuditFields: models.AuditFields{Guid: guid + 1}, Key: "custom", DisplayName: "Custom", Status: models.BusinessGroupStatusActive}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, script := newCreateAccountTestDB(t)
			script.groups = test.groups
			execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
				Username: "alice", Role: "user", GroupGUID: &guid, PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
			}, func() int64 { t.Fatal("GUID called for rejected explicit group"); return 0 })
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate))
			if err != nil || outcome.Failure == nil || *outcome.Failure != models.FailureConsumerValidation || outcome.HTTPStatus != 404 || script.writeCount() != 0 {
				t.Fatalf("explicit group outcome/writes = %#v/%v/%d", outcome, err, script.writeCount())
			}
			_ = tx.Rollback().Error
		})
	}
}

func TestCreateAccountAdminSnowflakeFailuresAfterUserRollback(t *testing.T) {
	tests := []struct {
		name  string
		guids []int64
	}{
		{"head", []int64{9751, 0}},
		{"first override", []int64{9751, 9752, 0}},
		{"second override", []int64{9751, 9752, 9753, 0}},
		{"auth audit", []int64{9751, 9752, 9753, 9754, 0}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, script := newCreateAccountTestDB(t)
			execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreateAdmin, actionsecurity.CreateAccountIntent{
				Username: "managed_admin", Role: "admin", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
				Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.sessions.read", Effect: 3}},
			}, createAccountTestGUIDs(test.guids...))
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreateAdmin))
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) {
				t.Fatalf("snowflake outcome = %#v/%v", outcome, err)
			}
			_ = tx.Rollback().Error
			if len(script.committed) != 0 {
				t.Fatal("snowflake failure committed partial writes")
			}
		})
	}
}

func TestCreateAccountClockGUIDStorageAndOperationFailuresClearHashAndRollback(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*createAccountScript, *CreateAccountExecution, *models.AdminOperation)
	}{
		{"clock", func(_ *createAccountScript, e *CreateAccountExecution, _ *models.AdminOperation) {
			e.clock = &createAccountTestClock{now: 0}
		}},
		{"user guid", func(_ *createAccountScript, e *CreateAccountExecution, _ *models.AdminOperation) {
			e.nextGUID = createAccountTestGUIDs(0)
		}},
		{"auth guid", func(_ *createAccountScript, e *CreateAccountExecution, _ *models.AdminOperation) {
			e.nextGUID = createAccountTestGUIDs(9501, 0)
		}},
		{"actor query", func(s *createAccountScript, _ *CreateAccountExecution, _ *models.AdminOperation) {
			s.failQuery = "users"
		}},
		{"group query", func(s *createAccountScript, _ *CreateAccountExecution, _ *models.AdminOperation) {
			s.failQuery = "business_groups"
		}},
		{"user write", func(s *createAccountScript, _ *CreateAccountExecution, _ *models.AdminOperation) {
			s.failExec = "users"
		}},
		{"auth write", func(s *createAccountScript, _ *CreateAccountExecution, _ *models.AdminOperation) {
			s.failExec = "auth_audit_events"
		}},
		{"operation action", func(_ *createAccountScript, _ *CreateAccountExecution, op *models.AdminOperation) {
			op.Action = int(actionsecurity.ActionUsersDelete)
		}},
		{"ordinary verification", func(_ *createAccountScript, _ *CreateAccountExecution, op *models.AdminOperation) {
			value := int64(51)
			op.VerificationID = &value
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, script := newCreateAccountTestDB(t)
			execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
				Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
			}, createAccountTestGUIDs(9501, 9502))
			op := validCreateAccountTestOperation(actionsecurity.ActionUsersCreate)
			test.mutate(script, execution, &op)
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, op)
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) || err.Error() != ErrActionOperationUnavailable.Error() {
				t.Fatalf("failure outcome = %#v/%v", outcome, err)
			}
			_ = tx.Rollback().Error
			if len(script.committed) != 0 {
				t.Fatalf("failure committed writes: %v", script.committedKinds())
			}
			assertCreateHashCleared(t, execution)
		})
	}
}

func TestCreateAccountInvalidTransactionFailureClearsHash(t *testing.T) {
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
		Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, createAccountTestGUIDs(9551, 9552))
	outcome, err := execution.Execute(context.Background(), nil, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate))
	if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("invalid transaction outcome = %#v/%v", outcome, err)
	}
	assertCreateHashCleared(t, execution)
}

func TestCreateAccountClearSecretsIsIdempotentAndConcurrent(t *testing.T) {
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
		Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, createAccountTestGUIDs(9571, 9572))
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		copyExecution := *execution
		wait.Add(1)
		go func(candidate *CreateAccountExecution) {
			defer wait.Done()
			<-start
			candidate.ClearSecrets()
			candidate.ClearSecrets()
		}(&copyExecution)
	}
	close(start)
	wait.Wait()
	assertCreateHashCleared(t, execution)
	if outcome, err := execution.Execute(context.Background(), nil, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate)); outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("cleared execution was reusable: %#v/%v", outcome, err)
	}
}

func TestCreateAccountOperationOwnerClearsSecretsOnPreConsumerFailures(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*ActionOperationService, *actionExecuteScript, **OperationIdentity, *createAccountLifecycleConsumer)
		wantConsume int32
	}{
		{"identity", func(_ *ActionOperationService, _ *actionExecuteScript, identity **OperationIdentity, _ *createAccountLifecycleConsumer) {
			*identity = nil
		}, 0},
		{"redis", func(service *ActionOperationService, _ *actionExecuteScript, _ **OperationIdentity, _ *createAccountLifecycleConsumer) {
			service.authRedis.client.(*actionIssueRedisClient).err = errors.New("private redis failure")
		}, 0},
		{"actor", func(_ *ActionOperationService, script *actionExecuteScript, _ **OperationIdentity, _ *createAccountLifecycleConsumer) {
			script.failAt = "actor"
		}, 0},
		{"session", func(_ *ActionOperationService, script *actionExecuteScript, _ **OperationIdentity, _ *createAccountLifecycleConsumer) {
			script.failAt = "session"
		}, 0},
		{"descriptor", func(service *ActionOperationService, _ *actionExecuteScript, _ **OperationIdentity, _ *createAccountLifecycleConsumer) {
			service.resolve = func(actionsecurity.Action) (actionsecurity.Descriptor, bool) {
				return actionsecurity.Descriptor{}, false
			}
		}, 0},
		{"consumer", func(_ *ActionOperationService, _ *actionExecuteScript, _ **OperationIdentity, consumer *createAccountLifecycleConsumer) {
			consumer.err = errors.New("private consumer failure")
		}, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, script, identity := actionExecuteFixture(t)
			execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
				Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
			}, createAccountTestGUIDs(9581, 9582))
			consumer := &createAccountLifecycleConsumer{CreateAccountExecution: execution,
				outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
			test.mutate(service, script, &identity, consumer)
			view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
			if view != nil || err == nil || consumer.calls.Load() != test.wantConsume {
				t.Fatalf("pre-consumer failure view/error/calls = %#v/%v/%d", view, err, consumer.calls.Load())
			}
			assertCreateHashCleared(t, execution)
		})
	}
}

func TestCreateAccountResultPublishesOnlyAfterConfirmedCommit(t *testing.T) {
	tests := []struct {
		name          string
		failAt        string
		commitUnknown bool
		wantResult    bool
	}{
		{name: "confirmed commit", wantResult: true},
		{name: "management audit rollback", failAt: "official_audit"},
		{name: "outbox rollback", failAt: "official_outbox"},
		{name: "terminal rollback", failAt: "terminal_success"},
		{name: "commit unknown", commitUnknown: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, script, identity := actionExecuteFixture(t)
			script.failAt = test.failAt
			script.commitUnknown = test.commitUnknown
			execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
				Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
			}, createAccountTestGUIDs(9591, 9592))
			consumer := &createAccountLifecycleConsumer{CreateAccountExecution: execution, publishPending: true,
				outcome: TerminalOutcome{ResultKind: models.ResultNone, HTTPStatus: 204}}
			view, err := service.Execute(context.Background(), identity, consumer, &fixtureActionAuditWriter{}, &fixtureActionOutboxWriter{})
			result, ok := execution.ResultUser()
			if test.wantResult {
				if err != nil || view == nil || !ok || result == nil || result.GUID != "9591" {
					t.Fatalf("confirmed result = %#v/%#v/%v last=%s queries=%v execs=%v", view, result, err, script.lastError, script.queries, script.execs)
				}
			} else if view != nil || err == nil || ok || result != nil {
				t.Fatalf("unconfirmed result leaked = %#v/%#v/%v", view, result, err)
			}
			assertCreateHashCleared(t, execution)
		})
	}
}

func TestCreateAccountUniqueKeyRaceReturnsPermanentUsernameConflict(t *testing.T) {
	db, script := newCreateAccountTestDB(t)
	script.execErrors["users"] = &mysqlDriver.MySQLError{Number: 1062, Message: "Duplicate entry 'alice' for key 'users.uk_users_username'"}
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
		Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, createAccountTestGUIDs(9601))
	tx := db.Begin()
	outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate))
	if err != nil || outcome.Failure == nil || *outcome.Failure != models.FailureConsumerValidation || outcome.HTTPStatus != 409 {
		t.Fatalf("unique race outcome = %#v/%v", outcome, err)
	}
	_ = tx.Rollback().Error
	if script.execCount("auth_audit_events") != 0 || len(script.committed) != 0 {
		t.Fatal("unique-key race retained partial writes")
	}
	assertCreateHashCleared(t, execution)
}

func TestCreateAccountAdminWriteStageFailuresRollbackAllSideEffects(t *testing.T) {
	stages := []struct {
		name, table string
		occurrence  int
	}{
		{"user", "users", 1},
		{"permission head", "user_permission_heads", 1},
		{"first override batch", "user_permission_overrides", 1},
		{"second override batch", "user_permission_overrides", 2},
		{"registration audit", "auth_audit_events", 1},
	}
	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			db, script := newCreateAccountTestDB(t)
			script.failExec = stage.table
			script.failExecOccurrence = stage.occurrence
			execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreateAdmin, actionsecurity.CreateAccountIntent{
				Username: "managed_admin", Role: "admin", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
				Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}, {Capability: "users.sessions.read", Effect: 3}},
			}, createAccountTestGUIDs(9701, 9702, 9703, 9704, 9705))
			tx := db.Begin()
			outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreateAdmin))
			if outcome != (TerminalOutcome{}) || !errors.Is(err, ErrActionOperationUnavailable) {
				t.Fatalf("stage outcome = %#v/%v", outcome, err)
			}
			_ = tx.Rollback().Error
			if len(script.committed) != 0 {
				t.Fatalf("stage committed partial writes: %v", script.committedKinds())
			}
			assertCreateHashCleared(t, execution)
		})
	}
}

func TestCreateAccountExecutionIsOneShotAcrossCopiesAndConcurrency(t *testing.T) {
	db, script := newCreateAccountTestDB(t)
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
		Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, createAccountTestGUIDs(9801, 9802))
	var successes, failures atomic.Int32
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < 16; index++ {
		copyExecution := *execution
		wait.Add(1)
		go func(candidate CreateAccountExecution) {
			defer wait.Done()
			<-start
			tx := db.Begin()
			outcome, err := candidate.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate))
			if err == nil && outcome.Failure == nil {
				successes.Add(1)
				_ = tx.Commit().Error
				return
			}
			if errors.Is(err, ErrActionOperationUnavailable) {
				failures.Add(1)
			}
			_ = tx.Rollback().Error
		}(copyExecution)
	}
	close(start)
	wait.Wait()
	if successes.Load() != 1 || failures.Load() != 15 || script.execCount("auth_audit_events") != 1 {
		t.Fatalf("success/failure/auth audit = %d/%d/%d", successes.Load(), failures.Load(), script.execCount("auth_audit_events"))
	}
}

func TestCreateAccountWritersRejectDescriptorAndBindingDriftWithoutWrites(t *testing.T) {
	db, script := newCreateAccountTestDB(t)
	execution := newCreateAccountTestExecution(t, actionsecurity.ActionUsersCreate, actionsecurity.CreateAccountIntent{
		Username: "alice", Role: "user", PlanType: int(models.PlanFree), AllowedModels: []string{}, DailyCallLimit: 100,
	}, createAccountTestGUIDs(9901, 9902, 9903))
	tx := db.Begin()
	outcome, err := execution.Execute(context.Background(), tx, validCreateAccountTestOperation(actionsecurity.ActionUsersCreate))
	if err != nil {
		t.Fatal(err)
	}
	resultKind := models.ResultUser
	event := ActionAuditEvent{PublicRef: deleteWriterPublicRef, ActorGUID: script.actor.Guid, SessionGUID: script.session.Guid,
		Action: actionsecurity.ActionUsersCreateAdmin, TargetKind: actionsecurity.TargetNone, State: models.OperationSucceeded,
		ResultKind: &resultKind, ResultGUID: outcome.ResultGUID, OccurredAt: 8001}
	if err := execution.Write(context.Background(), tx, event); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("audit descriptor drift = %v", err)
	}
	outbox, _ := NewCreateAccountOutboxWriter(createAccountTestGUIDs(9904), &createAccountTestClock{now: 8001})
	target := int64(9901)
	if err := outbox.Write(context.Background(), tx, ActionOutboxEvent{PublicRef: event.PublicRef, ActorGUID: event.ActorGUID, SessionGUID: event.SessionGUID,
		Action: actionsecurity.ActionUsersCreate, TargetKind: actionsecurity.TargetNone, TargetGUID: &target, State: event.State,
		ResultKind: event.ResultKind, ResultGUID: event.ResultGUID, OccurredAt: event.OccurredAt}); !errors.Is(err, ErrActionOperationUnavailable) {
		t.Fatalf("outbox target drift = %v", err)
	}
	_ = tx.Rollback().Error
	if script.writeCount() != 2 { // user and registration audit from the consumer only
		t.Fatalf("drift writers emitted writes: %v", script.observedKinds())
	}
}

func createAccountTestDescriptor(t *testing.T, action actionsecurity.Action) actionsecurity.Descriptor {
	t.Helper()
	for _, descriptor := range actionsecurity.FutureActionDescriptors() {
		if descriptor.Action == action {
			return descriptor
		}
	}
	t.Fatalf("future descriptor %d missing", action)
	return actionsecurity.Descriptor{}
}

type createAccountTestClock struct{ now int64 }

func (clock *createAccountTestClock) NowMillis() int64 { return clock.now }

type createAccountLifecycleConsumer struct {
	*CreateAccountExecution
	outcome        TerminalOutcome
	err            error
	publishPending bool
	calls          atomic.Int32
}

func (consumer *createAccountLifecycleConsumer) Execute(_ context.Context, _ *gorm.DB, _ models.AdminOperation) (TerminalOutcome, error) {
	consumer.calls.Add(1)
	if consumer.err != nil {
		return TerminalOutcome{}, consumer.err
	}
	if consumer.publishPending {
		username := "alice"
		consumer.state.mu.Lock()
		consumer.state.resultUser = models.User{ID: 501, AuditFields: models.AuditFields{Guid: 9591, CreatedAt: 8001, UpdatedAt: 8001},
			GroupID: 61, Username: &username, PlanType: models.PlanFree, Status: models.UserStatusActive, Role: models.UserRoleUser,
			AuthVersion: 1, AllowedModels: models.JSONSlice{}}
		consumer.state.resultGroup = models.BusinessGroup{ID: 61, AuditFields: models.AuditFields{Guid: 6001}, Key: "default", DisplayName: "Default", Status: models.BusinessGroupStatusActive}
		consumer.state.resultRecorded = true
		consumer.state.mu.Unlock()
	}
	return consumer.outcome, nil
}

func newCreateAccountTestExecution(t *testing.T, action actionsecurity.Action, intent actionsecurity.CreateAccountIntent, nextGUID func() int64) *CreateAccountExecution {
	t.Helper()
	execution, err := NewCreateAccountExecution(createAccountTestDescriptor(t, action), intent, []byte(createAccountTestPasswordHash),
		CreateAccountRequestMetadata{RequestID: "request.create-1", TrustedIP: "203.0.113.7"}, nextGUID, &createAccountTestClock{now: 8001})
	if err != nil {
		t.Fatal(err)
	}
	return execution
}

func createAccountTestGUIDs(values ...int64) func() int64 {
	var index atomic.Int32
	return func() int64 {
		position := int(index.Add(1)) - 1
		if position >= len(values) {
			return 0
		}
		return values[position]
	}
}

func validCreateAccountTestOperation(action actionsecurity.Action) models.AdminOperation {
	actor := int64(41)
	operation := models.AdminOperation{
		ID: 31, AuditFields: models.AuditFields{Guid: 3001, CreatedAt: 7001, CreatedBy: &actor, UpdatedAt: 8001, UpdatedBy: &actor},
		ActorUserID: 41, ActorAuthVersion: 3, SessionID: 45, Action: int(action), State: models.OperationProcessing,
		PublicRef: deleteWriterPublicRef, QueryExpiresAt: 9001,
	}
	if action == actionsecurity.ActionUsersCreateAdmin {
		verificationID := int64(51)
		operation.VerificationID = &verificationID
	}
	return operation
}

const createAccountDriverName = "porsche_create_account_consumer"

var (
	createAccountDriverOnce    sync.Once
	createAccountDriverCounter atomic.Uint64
	createAccountScripts       sync.Map
)

type createAccountCall struct {
	kind, table, query string
	args               []driver.NamedValue
}

type createAccountScript struct {
	mu                 sync.Mutex
	actor              models.User
	session            models.Session
	actorHead          *models.PermissionPolicyHead
	actorRules         []models.PermissionOverride
	groups             []models.BusinessGroup
	conflicts          []models.User
	failQuery          string
	failExec           string
	failExecOccurrence int
	execSeen           map[string]int
	execErrors         map[string]error
	rowsAffected       map[string]int64
	observed           []createAccountCall
	committed          []createAccountCall
	nestedBegins       atomic.Int32
	lastInsertIDs      map[string]int64
	operation          models.AdminOperation
	verification       models.AdminActionVerification
}

type createAccountDriver struct{}
type createAccountConn struct {
	script *createAccountScript
	tx     *createAccountTx
}
type createAccountTx struct {
	conn    *createAccountConn
	pending []createAccountCall
}
type createAccountRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}
type createAccountResult struct{ affected, lastID int64 }

func (createAccountDriver) Open(name string) (driver.Conn, error) {
	value, ok := createAccountScripts.Load(name)
	if !ok {
		return nil, errors.New("unknown create-account script")
	}
	return &createAccountConn{script: value.(*createAccountScript)}, nil
}
func (conn *createAccountConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements disabled")
}
func (conn *createAccountConn) Close() error { return nil }
func (conn *createAccountConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}
func (conn *createAccountConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	if conn.tx != nil {
		conn.script.nestedBegins.Add(1)
		return nil, errors.New("nested transaction")
	}
	conn.tx = &createAccountTx{conn: conn}
	return conn.tx, nil
}
func (conn *createAccountConn) CheckNamedValue(value *driver.NamedValue) error {
	switch typed := value.Value.(type) {
	case actionsecurity.Action:
		value.Value = int64(typed)
	case models.UserRole:
		value.Value = int64(typed)
	case models.UserStatus:
		value.Value = int64(typed)
	case models.PlanType:
		value.Value = int64(typed)
	case models.BusinessGroupStatus:
		value.Value = int64(typed)
	case models.AuthAuditEventType:
		value.Value = int64(typed)
	case models.LoginMethod:
		value.Value = int64(typed)
	case *models.LoginMethod:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = int64(*typed)
		}
	case models.JSONMap:
		encoded, err := typed.Value()
		if err != nil {
			return err
		}
		value.Value = encoded
	case *int64:
		if typed == nil {
			value.Value = nil
		} else {
			value.Value = *typed
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
func (conn *createAccountConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if conn.tx == nil {
		return nil, errors.New("query outside transaction")
	}
	table := createAccountTable(query)
	call := createAccountCall{kind: "query", table: table, query: query, args: append([]driver.NamedValue(nil), args...)}
	conn.script.mu.Lock()
	conn.script.observed = append(conn.script.observed, call)
	fail := conn.script.failQuery == table
	conn.script.mu.Unlock()
	if fail {
		return nil, errors.New("private create query failure")
	}
	switch table {
	case "users":
		if strings.Contains(query, "username = ?") {
			values := make([][]driver.Value, 0, len(conn.script.conflicts))
			for _, user := range conn.script.conflicts {
				values = append(values, []driver.Value{user.ID, user.Guid, int64(user.IsDeleted)})
			}
			return &createAccountRows{columns: []string{"id", "guid", "is_deleted"}, values: values}, nil
		}
		user := conn.script.actor
		return createAccountRow([]string{"id", "guid", "role", "status", "is_deleted", "auth_version"},
			[]driver.Value{user.ID, user.Guid, int64(user.Role), int64(user.Status), int64(user.IsDeleted), int64(user.AuthVersion)}), nil
	case "user_sessions":
		session := conn.script.session
		return createAccountRow([]string{"id", "guid", "user_id", "session_version", "expires_at", "revoked_at", "is_deleted"},
			[]driver.Value{session.ID, session.Guid, session.UserID, int64(session.SessionVersion), session.ExpiresAt, session.RevokedAt, int64(session.IsDeleted)}), nil
	case "user_permission_heads":
		if conn.script.actorHead == nil {
			return &createAccountRows{columns: []string{"id", "guid", "is_deleted", "policy_version", "catalog_version", "rule_count"}}, nil
		}
		head := *conn.script.actorHead
		return createAccountRow([]string{"id", "guid", "is_deleted", "policy_version", "catalog_version", "rule_count"},
			[]driver.Value{head.ID, head.Guid, int64(head.IsDeleted), head.PolicyVersion, int64(head.CatalogVersion), int64(head.RuleCount)}), nil
	case "user_permission_overrides":
		values := make([][]driver.Value, 0, len(conn.script.actorRules))
		for _, rule := range conn.script.actorRules {
			values = append(values, []driver.Value{rule.ID, rule.Guid, int64(rule.IsDeleted), rule.PolicyVersion, int64(rule.Capability), int64(rule.Effect)})
		}
		return &createAccountRows{columns: []string{"id", "guid", "is_deleted", "policy_version", "capability", "effect"}, values: values}, nil
	case "business_groups":
		values := make([][]driver.Value, 0, len(conn.script.groups))
		for _, group := range conn.script.groups {
			values = append(values, []driver.Value{group.ID, group.Guid, group.Key, group.DisplayName, int64(group.Status), int64(group.IsDeleted)})
		}
		return &createAccountRows{columns: []string{"id", "guid", "group_key", "display_name", "status", "is_deleted"}, values: values}, nil
	case "admin_operations":
		op := conn.script.operation
		return createAccountRow([]string{"id", "actor_user_id", "actor_auth_version", "session_id", "action", "verification_id", "state", "public_ref"},
			[]driver.Value{op.ID, op.ActorUserID, int64(op.ActorAuthVersion), op.SessionID, int64(op.Action), op.VerificationID, int64(op.State), op.PublicRef}), nil
	case "admin_action_verifications":
		verification := conn.script.verification
		return createAccountRow([]string{"id", "actor_user_id", "actor_auth_version", "session_id", "action", "target_kind", "target_guid", "consumed_at", "is_deleted"},
			[]driver.Value{verification.ID, verification.ActorUserID, int64(verification.ActorAuthVersion), verification.SessionID, int64(verification.Action), int64(verification.TargetKind), verification.TargetGUID, verification.ConsumedAt, int64(verification.IsDeleted)}), nil
	default:
		return nil, fmt.Errorf("unexpected create query: %s", query)
	}
}
func (conn *createAccountConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if conn.tx == nil {
		return nil, errors.New("write outside transaction")
	}
	table := createAccountTable(query)
	call := createAccountCall{kind: "exec", table: table, query: query, args: append([]driver.NamedValue(nil), args...)}
	conn.script.mu.Lock()
	conn.script.observed = append(conn.script.observed, call)
	conn.script.execSeen[table]++
	occurrence := conn.script.execSeen[table]
	fail := conn.script.failExec == table && (conn.script.failExecOccurrence == 0 || conn.script.failExecOccurrence == occurrence)
	execError := conn.script.execErrors[table]
	affected := int64(1)
	if override, ok := conn.script.rowsAffected[table]; ok {
		affected = override
	}
	lastID := conn.script.lastInsertIDs[table]
	conn.script.mu.Unlock()
	if fail {
		return nil, errors.New("private create write failure")
	}
	if execError != nil {
		return nil, execError
	}
	conn.tx.pending = append(conn.tx.pending, call)
	return createAccountResult{affected: affected, lastID: lastID}, nil
}
func (tx *createAccountTx) Commit() error {
	tx.conn.script.mu.Lock()
	tx.conn.script.committed = append(tx.conn.script.committed, tx.pending...)
	tx.conn.script.mu.Unlock()
	tx.conn.tx = nil
	return nil
}
func (tx *createAccountTx) Rollback() error       { tx.conn.tx = nil; return nil }
func (rows *createAccountRows) Columns() []string { return rows.columns }
func (rows *createAccountRows) Close() error      { return nil }
func (rows *createAccountRows) Next(destination []driver.Value) error {
	if rows.index >= len(rows.values) {
		return io.EOF
	}
	copy(destination, rows.values[rows.index])
	rows.index++
	return nil
}
func (result createAccountResult) LastInsertId() (int64, error) { return result.lastID, nil }
func (result createAccountResult) RowsAffected() (int64, error) { return result.affected, nil }

func createAccountRow(columns []string, values []driver.Value) *createAccountRows {
	return &createAccountRows{columns: columns, values: [][]driver.Value{values}}
}

func newCreateAccountTestDB(t *testing.T) (*gorm.DB, *createAccountScript) {
	t.Helper()
	createAccountDriverOnce.Do(func() { sql.Register(createAccountDriverName, createAccountDriver{}) })
	actorHash := "actor-hash"
	actor := models.User{ID: 41, AuditFields: models.AuditFields{Guid: 4001}, PasswordHash: &actorHash, Role: models.UserRoleRoot, Status: models.UserStatusActive, AuthVersion: 3}
	operation := validCreateAccountTestOperation(actionsecurity.ActionUsersCreate)
	script := &createAccountScript{
		actor:     actor,
		session:   models.Session{ID: 45, AuditFields: models.AuditFields{Guid: 4501}, UserID: 41, SessionVersion: 2, ExpiresAt: 10000},
		groups:    []models.BusinessGroup{{ID: 61, AuditFields: models.AuditFields{Guid: 6001}, Key: "default", DisplayName: "Default", Status: models.BusinessGroupStatusActive}},
		operation: operation, rowsAffected: map[string]int64{}, lastInsertIDs: map[string]int64{
			"users": 501, "user_permission_heads": 601, "user_permission_overrides": 701, "auth_audit_events": 801, "audit_logs": 901, "admin_action_outbox": 1001,
		},
		execSeen: map[string]int{}, execErrors: map[string]error{},
	}
	dsn := fmt.Sprintf("create-account-%d", createAccountDriverCounter.Add(1))
	createAccountScripts.Store(dsn, script)
	t.Cleanup(func() { createAccountScripts.Delete(dsn) })
	sqlDB, err := sql.Open(createAccountDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	return db, script
}

func createAccountTable(query string) string {
	for _, table := range []string{"admin_action_verifications", "admin_action_outbox", "admin_operations", "auth_audit_events", "audit_logs", "user_permission_overrides", "user_permission_heads", "business_groups", "user_sessions", "users"} {
		if strings.Contains(query, "`"+table+"`") {
			return table
		}
	}
	return ""
}
func (script *createAccountScript) committedKinds() []string {
	script.mu.Lock()
	defer script.mu.Unlock()
	out := make([]string, 0, len(script.observed)+len(script.committed))
	for _, call := range script.observed {
		if call.kind == "query" {
			out = append(out, call.kind+":"+call.table)
		}
	}
	for _, call := range script.committed {
		out = append(out, call.kind+":"+call.table)
	}
	return out
}
func (script *createAccountScript) observedKinds() []string {
	script.mu.Lock()
	defer script.mu.Unlock()
	out := make([]string, 0, len(script.observed))
	for _, call := range script.observed {
		out = append(out, call.kind+":"+call.table)
	}
	return out
}
func (script *createAccountScript) committedTableCall(table string, occurrence int) createAccountCall {
	script.mu.Lock()
	defer script.mu.Unlock()
	for _, call := range script.committed {
		if call.table == table {
			if occurrence == 0 {
				return call
			}
			occurrence--
		}
	}
	return createAccountCall{}
}
func (script *createAccountScript) containsSQL(fragment string) bool {
	script.mu.Lock()
	defer script.mu.Unlock()
	for _, call := range script.observed {
		if strings.Contains(strings.ToLower(call.query), strings.ToLower(fragment)) {
			return true
		}
	}
	return false
}
func (script *createAccountScript) execCount(table string) int {
	script.mu.Lock()
	defer script.mu.Unlock()
	count := 0
	for _, call := range script.committed {
		if call.kind == "exec" && call.table == table {
			count++
		}
	}
	return count
}
func (script *createAccountScript) writeCount() int {
	script.mu.Lock()
	defer script.mu.Unlock()
	count := 0
	for _, call := range script.observed {
		if call.kind == "exec" {
			count++
		}
	}
	return count
}
func (script *createAccountScript) queryContains(fragment string) bool {
	script.mu.Lock()
	defer script.mu.Unlock()
	for _, call := range script.observed {
		if call.kind == "query" && strings.Contains(call.query, fragment) {
			return true
		}
	}
	return false
}
func assertCreateHashCleared(t *testing.T, execution *CreateAccountExecution) {
	t.Helper()
	execution.state.mu.Lock()
	defer execution.state.mu.Unlock()
	if len(execution.state.passwordHash) != 0 {
		t.Fatal("execution retained password hash")
	}
}
func createAccountInsertValues(t *testing.T, call createAccountCall) map[string]any {
	t.Helper()
	open, close := strings.Index(call.query, "("), strings.Index(call.query, ") VALUES")
	if open < 0 || close <= open {
		t.Fatalf("unrecognized insert: %s", call.query)
	}
	columns := strings.Split(call.query[open+1:close], ",")
	if len(columns) != len(call.args) {
		t.Fatalf("insert columns/args = %d/%d: %s", len(columns), len(call.args), call.query)
	}
	values := make(map[string]any, len(columns))
	for index, column := range columns {
		values[strings.Trim(strings.TrimSpace(column), "`")] = call.args[index].Value
	}
	return values
}
