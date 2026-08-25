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

func TestModelHealthUsesSingularMigratedTableName(t *testing.T) {
	if got := (ModelHealth{}).TableName(); got != "model_health" {
		t.Fatalf("ModelHealth table = %q, want migrated table model_health", got)
	}
}
