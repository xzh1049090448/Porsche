package models

import (
	"reflect"
	"testing"
)

// TestPersistedModelsUseRequiredIdentityAndAuditColumns protects the MySQL
// schema contract: every persisted model exposes the common business identity,
// audit, and soft-delete fields using database-compatible Go types.
func TestPersistedModelsUseRequiredIdentityAndAuditColumns(t *testing.T) {
	for _, value := range []any{User{}, Conversation{}, Message{}, UsageRecord{}, Order{}, AuditLog{}, ModelHealth{}, GatewayAPIToken{}} {
		typ := reflect.TypeOf(value)
		for _, expected := range []struct {
			name string
			typ  reflect.Type
		}{
			{"ID", reflect.TypeOf(int64(0))},
			{"Guid", reflect.TypeOf(int64(0))},
			{"CreatedAt", reflect.TypeOf(int64(0))},
			{"UpdatedAt", reflect.TypeOf(int64(0))},
			{"IsDeleted", reflect.TypeOf(0)},
		} {
			field, ok := typ.FieldByName(expected.name)
			if !ok {
				t.Errorf("%s is missing %s", typ.Name(), expected.name)
				continue
			}
			if field.Type != expected.typ {
				t.Errorf("%s.%s = %s, want %s", typ.Name(), expected.name, field.Type, expected.typ)
			}
		}
	}
}

// TestPersistedEnumsAreIntegers ensures enum names never become database
// values again.
func TestPersistedEnumsAreIntegers(t *testing.T) {
	for _, value := range []any{UserStatusActive, PlanFree, OrderPending, GatewayTokenActive, MessageRoleUser, UsageRecordChat} {
		if reflect.TypeOf(value).Kind() != reflect.Int {
			t.Errorf("%T must use an integer-backed enum", value)
		}
	}
}

// TestAuthPersistedModelsContract fixes the schema-facing shape for the
// session and authentication audit entities before auth services are added.
func TestAuthPersistedModelsContract(t *testing.T) {
	for _, value := range []any{Session{}, AuthAuditEvent{}} {
		typ := reflect.TypeOf(value)
		for _, expected := range []struct {
			name string
			typ  reflect.Type
		}{
			{"ID", reflect.TypeOf(int64(0))},
			{"Guid", reflect.TypeOf(int64(0))},
			{"CreatedAt", reflect.TypeOf(int64(0))},
			{"CreatedBy", reflect.TypeOf((*int64)(nil))},
			{"UpdatedAt", reflect.TypeOf(int64(0))},
			{"UpdatedBy", reflect.TypeOf((*int64)(nil))},
			{"IsDeleted", reflect.TypeOf(0)},
		} {
			field, ok := typ.FieldByName(expected.name)
			if !ok {
				t.Errorf("%s is missing %s", typ.Name(), expected.name)
				continue
			}
			if field.Type != expected.typ {
				t.Errorf("%s.%s = %s, want %s", typ.Name(), expected.name, field.Type, expected.typ)
			}
		}
	}

	if reflect.TypeOf(UserRoleUser).Kind() != reflect.Int || UserRoleUser != 1 || UserRoleAdmin != 10 || UserRoleRoot != 100 {
		t.Errorf("user roles must have stable integer values: %d, %d, %d", UserRoleUser, UserRoleAdmin, UserRoleRoot)
	}
	if reflect.TypeOf(LoginMethodPassword).Kind() != reflect.Int || LoginMethodPassword != 1 {
		t.Errorf("login methods must be integer-backed with stable values")
	}
	if reflect.TypeOf(AuthAuditEventRegistered).Kind() != reflect.Int || AuthAuditEventRegistered != 1 {
		t.Errorf("auth audit events must be integer-backed with stable values")
	}
	if role, ok := ParseUserRole("root"); !ok || role != UserRoleRoot || role.String() != "root" {
		t.Errorf("user role mapping must round-trip through its stable integer")
	}
	if method, ok := ParseLoginMethod("password"); !ok || method != LoginMethodPassword || method.String() != "password" {
		t.Errorf("login method mapping must round-trip through its stable integer")
	}
	if event, ok := ParseAuthAuditEventType("session_revoked"); !ok || event != AuthAuditEventSessionRevoked || event.String() != "session_revoked" {
		t.Errorf("auth event mapping must round-trip through its stable integer")
	}

	userPhone, ok := reflect.TypeOf(User{}).FieldByName("Phone")
	if !ok || userPhone.Type != reflect.TypeOf((*string)(nil)) || userPhone.Tag.Get("json") != "-" {
		t.Errorf("users.phone must be nullable and omitted from DTO serialization: %#v", userPhone)
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(Session{}), reflect.TypeOf(AuthAuditEvent{})} {
		for _, forbidden := range []string{"RefreshToken", "AccessToken", "Authorization", "Cookie", "Password", "RawHeader"} {
			if _, exists := typ.FieldByName(forbidden); exists {
				t.Errorf("%s must not persist raw credential field %s", typ.Name(), forbidden)
			}
		}
	}
	for _, expected := range []string{"RefreshHMAC", "PreviousRefreshHMAC"} {
		field, ok := reflect.TypeOf(Session{}).FieldByName(expected)
		if !ok || field.Tag.Get("json") != "-" {
			t.Errorf("Session.%s must be a non-serializable HMAC field", expected)
		}
	}
}

func TestModelHealthUsesSingularMigratedTableName(t *testing.T) {
	if got := (ModelHealth{}).TableName(); got != "model_health" {
		t.Fatalf("ModelHealth table = %q, want migrated table model_health", got)
	}
}
