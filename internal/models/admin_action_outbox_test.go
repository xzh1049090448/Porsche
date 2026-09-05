package models

import (
	"encoding/json"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

func TestAdminActionOutboxSchemaContract(t *testing.T) {
	if got := (AdminActionOutbox{}).TableName(); got != "admin_action_outbox" {
		t.Fatalf("AdminActionOutbox table = %q", got)
	}

	parsed, err := schema.Parse(&AdminActionOutbox{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]struct {
		dbType  string
		notNull bool
	}{
		"id": {"bigint", true}, "guid": {"bigint", true},
		"created_at": {"bigint", true}, "created_by": {"bigint", false},
		"updated_at": {"bigint", true}, "updated_by": {"bigint", false},
		"is_deleted": {"int", true}, "operation_id": {"bigint", true},
		"public_ref": {"char(46)", true}, "action": {"int", true},
		"target_kind": {"int", true}, "target_guid": {"bigint", false},
		"state": {"int", true}, "result_guid": {"bigint", false},
		"delivery_state": {"int", true}, "available_at": {"bigint", true},
		"delivered_at": {"bigint", false}, "attempt_count": {"int", true},
	}
	if len(parsed.DBNames) != len(want) {
		t.Fatalf("columns = %v, want exactly %d", parsed.DBNames, len(want))
	}
	for column, expected := range want {
		field := parsed.FieldsByDBName[column]
		if field == nil {
			t.Errorf("missing column %s", column)
			continue
		}
		if string(field.DataType) != expected.dbType || field.NotNull != expected.notNull {
			t.Errorf("%s type/not-null = %q/%t, want %q/%t", column, field.DataType, field.NotNull, expected.dbType, expected.notNull)
		}
	}
	encoded, err := json.Marshal(AdminActionOutbox{})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("AdminActionOutbox JSON = %s, want no model fields exposed", encoded)
	}
}

func TestAdminActionDeliveryStateMappingsAreStable(t *testing.T) {
	tests := []struct {
		state AdminActionDeliveryState
		code  int
		name  string
	}{
		{DeliveryPending, 1, "pending"},
		{DeliveryDelivered, 2, "delivered"},
		{DeliveryDead, 3, "dead"},
	}
	for _, tc := range tests {
		if int(tc.state) != tc.code || tc.state.String() != tc.name {
			t.Errorf("delivery mapping = %d/%q, want %d/%q", tc.state, tc.state.String(), tc.code, tc.name)
		}
		if got, ok := ParseAdminActionDeliveryState(tc.name); !ok || got != tc.state {
			t.Errorf("ParseAdminActionDeliveryState(%q) = %d/%t", tc.name, got, ok)
		}
	}
	if state, ok := ParseAdminActionDeliveryState("unknown"); ok || state != 0 {
		t.Error("unknown delivery state must be rejected")
	}
}
