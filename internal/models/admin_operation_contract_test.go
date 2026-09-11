package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestAdminActionVerificationAndAdminOperationSchemaContract(t *testing.T) {
	if got := (AdminActionVerification{}).TableName(); got != "admin_action_verifications" {
		t.Fatalf("AdminActionVerification table = %q", got)
	}
	if got := (AdminOperation{}).TableName(); got != "admin_operations" {
		t.Fatalf("AdminOperation table = %q", got)
	}

	tests := []struct {
		value any
		want  map[string]struct {
			dbType  string
			notNull bool
		}
	}{
		{&AdminActionVerification{}, map[string]struct {
			dbType  string
			notNull bool
		}{
			"id": {"bigint", true}, "guid": {"bigint", true},
			"created_at": {"bigint", true}, "created_by": {"bigint", false},
			"updated_at": {"bigint", true}, "updated_by": {"bigint", false},
			"is_deleted": {"int", true}, "actor_user_id": {"bigint", true},
			"actor_auth_version": {"int", true}, "session_id": {"bigint", true},
			"action": {"int", true}, "target_kind": {"int", true},
			"target_guid": {"bigint", false}, "intent_hmac": {"char(64)", true},
			"ticket_hmac": {"char(64)", true}, "expires_at": {"bigint", true},
			"consumed_at": {"bigint", false},
		}},
		{&AdminOperation{}, map[string]struct {
			dbType  string
			notNull bool
		}{
			"id": {"bigint", true}, "guid": {"bigint", true},
			"created_at": {"bigint", true}, "created_by": {"bigint", false},
			"updated_at": {"bigint", true}, "updated_by": {"bigint", false},
			"is_deleted": {"int", true}, "actor_user_id": {"bigint", true},
			"actor_auth_version": {"int", true}, "session_id": {"bigint", true},
			"action": {"int", true}, "idempotency_key_hmac": {"char(64)", true},
			"request_hmac": {"char(64)", true}, "verification_id": {"bigint", false},
			"state": {"int", true}, "public_ref": {"char(46)", true},
			"lease_owner_hmac": {"char(64)", false}, "lease_expires_at": {"bigint", false},
			"finished_at": {"bigint", false}, "query_expires_at": {"bigint", true},
			"error_code": {"int", false}, "result_kind": {"int", false},
			"result_guid": {"bigint", false}, "result_auth_version": {"int", false},
			"result_permissions_version": {"bigint", false}, "result_role": {"int", false}, "result_http_status": {"int", false},
		}},
	}

	for _, tc := range tests {
		parsed, err := schema.Parse(tc.value, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %T: %v", tc.value, err)
		}
		if len(parsed.DBNames) != len(tc.want) {
			t.Errorf("%s columns = %v, want exactly %d columns", parsed.Name, parsed.DBNames, len(tc.want))
		}
		for column, expected := range tc.want {
			field := parsed.FieldsByDBName[column]
			if field == nil {
				t.Errorf("%s missing column %s", parsed.Name, column)
				continue
			}
			if string(field.DataType) != expected.dbType || field.NotNull != expected.notNull {
				t.Errorf("%s.%s type/not-null = %q/%t, want %q/%t", parsed.Name, column, field.DataType, field.NotNull, expected.dbType, expected.notNull)
			}
		}
		for _, required := range []string{"id", "guid", "created_at", "created_by", "updated_at", "updated_by", "is_deleted"} {
			if parsed.FieldsByDBName[required] == nil {
				t.Errorf("%s violates persisted model contract: missing %s", parsed.Name, required)
			}
		}
	}
}

func TestAdminOperationRolePermissionResultTypesAreStable(t *testing.T) {
	typ := reflect.TypeOf(AdminOperation{})
	permissions, ok := typ.FieldByName("ResultPermissionsVersion")
	if !ok || permissions.Type != reflect.TypeOf((*int64)(nil)) {
		t.Fatalf("ResultPermissionsVersion type = %v, want *int64", permissions.Type)
	}
	role, ok := typ.FieldByName("ResultRole")
	if !ok || role.Type != reflect.TypeOf((*UserRole)(nil)) {
		t.Fatalf("ResultRole type = %v, want *UserRole", role.Type)
	}
}

func TestAdminOperationEnumMappingsAreStable(t *testing.T) {
	states := []struct {
		value AdminOperationState
		code  int
		name  string
	}{{OperationProcessing, 1, "processing"}, {OperationSucceeded, 2, "succeeded"}, {OperationFailed, 3, "failed"}, {OperationPendingRecovery, 4, "pending_recovery"}, {OperationExpired, 5, "expired"}}
	for _, tc := range states {
		if int(tc.value) != tc.code || tc.value.String() != tc.name {
			t.Errorf("state mapping = %d/%q, want %d/%q", tc.value, tc.value.String(), tc.code, tc.name)
		}
		if got, ok := ParseAdminOperationState(tc.name); !ok || got != tc.value {
			t.Errorf("ParseAdminOperationState(%q) = %d/%t", tc.name, got, ok)
		}
	}

	failures := []struct {
		value AdminOperationFailure
		code  int
		name  string
	}{{FailureActionRejected, 1, "action_rejected"}, {FailureTargetVersionConflict, 2, "target_version_conflict"}, {FailurePolicyVersionConflict, 3, "policy_version_conflict"}, {FailureTargetStateConflict, 4, "target_state_conflict"}, {FailureConsumerValidation, 5, "consumer_validation_failed"}}
	for _, tc := range failures {
		if int(tc.value) != tc.code || tc.value.String() != tc.name {
			t.Errorf("failure mapping = %d/%q, want %d/%q", tc.value, tc.value.String(), tc.code, tc.name)
		}
		if got, ok := ParseAdminOperationFailure(tc.name); !ok || got != tc.value {
			t.Errorf("ParseAdminOperationFailure(%q) = %d/%t", tc.name, got, ok)
		}
	}

	results := []struct {
		value AdminResultKind
		code  int
		name  string
	}{{ResultNone, 1, "none"}, {ResultUser, 2, "user"}, {ResultPublicContent, 3, "public_content"}}
	for _, tc := range results {
		if int(tc.value) != tc.code || tc.value.String() != tc.name {
			t.Errorf("result mapping = %d/%q, want %d/%q", tc.value, tc.value.String(), tc.code, tc.name)
		}
		if got, ok := ParseAdminResultKind(tc.name); !ok || got != tc.value {
			t.Errorf("ParseAdminResultKind(%q) = %d/%t", tc.name, got, ok)
		}
	}

	if state, ok := ParseAdminOperationState("unknown"); ok || state != 0 {
		t.Error("unknown operation state must be rejected")
	}
	if failure, ok := ParseAdminOperationFailure("consumer_validation"); ok || failure != 0 {
		t.Error("non-canonical failure name must be rejected")
	}
	if kind, ok := ParseAdminResultKind("operation"); ok || kind != 0 {
		t.Error("unknown result kind must be rejected")
	}
}

func TestAdminOperationLegalTransitions(t *testing.T) {
	allowed := map[[2]AdminOperationState]bool{
		{OperationProcessing, OperationSucceeded}:       true,
		{OperationProcessing, OperationFailed}:          true,
		{OperationProcessing, OperationPendingRecovery}: true,
		{OperationSucceeded, OperationExpired}:          true,
		{OperationFailed, OperationExpired}:             true,
		{OperationPendingRecovery, OperationExpired}:    true,
	}
	states := []AdminOperationState{OperationProcessing, OperationSucceeded, OperationFailed, OperationPendingRecovery, OperationExpired}
	for _, from := range states {
		for _, to := range states {
			if got, want := from.CanTransitionTo(to), allowed[[2]AdminOperationState{from, to}]; got != want {
				t.Errorf("%s -> %s allowed = %t, want %t", from, to, got, want)
			}
		}
	}
	if AdminOperationState(99).CanTransitionTo(OperationProcessing) {
		t.Error("unknown state must not transition")
	}
}

func TestAdminActionVerificationStatusIsDerived(t *testing.T) {
	now := int64(1_000)
	if got := (AdminActionVerification{ExpiresAt: now + 1}).StatusAt(now); got != VerificationActive {
		t.Errorf("active status = %q", got)
	}
	consumed := now - 1
	if got := (AdminActionVerification{ExpiresAt: now + 1, ConsumedAt: &consumed, AuditFields: AuditFields{IsDeleted: 1}}).StatusAt(now); got != VerificationConsumed {
		t.Errorf("consumed status must take precedence, got %q", got)
	}
	for _, verification := range []AdminActionVerification{{ExpiresAt: now}, {ExpiresAt: now - 1}, {ExpiresAt: now + 1, AuditFields: AuditFields{IsDeleted: 1}}} {
		if got := verification.StatusAt(now); got != VerificationExpired {
			t.Errorf("expired status = %q", got)
		}
	}
}

func TestAdminOperationModelsDoNotExposeOrPersistPlaintextSecrets(t *testing.T) {
	for _, value := range []any{AdminActionVerification{}, AdminOperation{}} {
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			lower := strings.ToLower(field.Name)
			if field.Name == "SID" || strings.Contains(lower, "password") || strings.Contains(lower, "refresh") ||
				((strings.Contains(lower, "ticket") || strings.Contains(lower, "idempotency")) && !strings.HasSuffix(field.Name, "HMAC")) {
				t.Errorf("%s contains forbidden plaintext field %s", typ.Name(), field.Name)
			}
			if field.Name != "AuditFields" && field.Tag.Get("json") != "-" {
				t.Errorf("%s.%s must not be serialized directly", typ.Name(), field.Name)
			}
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != "{}" {
			t.Errorf("%s JSON = %s, want no model fields exposed", typ.Name(), encoded)
		}
	}
}
