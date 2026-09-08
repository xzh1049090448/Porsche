package models

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
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

func TestSessionSIDMapsToMigratedColumn(t *testing.T) {
	parsed, err := schema.Parse(&Session{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse Session schema: %v", err)
	}

	field := parsed.LookUpField("SID")
	if field == nil {
		t.Fatal("Session schema is missing SID")
	}
	if field.DBName != "sid" {
		t.Fatalf("Session.SID database column = %q, want migrated column sid", field.DBName)
	}
}

func TestModelHealthUsesSingularMigratedTableName(t *testing.T) {
	if got := (ModelHealth{}).TableName(); got != "model_health" {
		t.Fatalf("ModelHealth table = %q, want migrated table model_health", got)
	}
}

func TestPlatformGenerationPersistenceEnumsAreStable(t *testing.T) {
	if PlatformGenerationReceiptModeSingle != 1 || PlatformGenerationReceiptModeCompare != 2 {
		t.Fatal("receipt mode integers changed")
	}
	if PlatformGenerationResultCompleted != 1 || PlatformGenerationResultFailed != 2 {
		t.Fatal("result status integers changed")
	}
	if (PlatformChatGenerationReceipt{}).TableName() != "platform_chat_generation_receipts" || (PlatformChatGenerationResult{}).TableName() != "platform_chat_generation_results" {
		t.Fatal("receipt table mapping changed")
	}
	for _, test := range []struct {
		value PlatformGenerationReceiptMode
		name  string
	}{
		{PlatformGenerationReceiptModeSingle, "single"},
		{PlatformGenerationReceiptModeCompare, "compare"},
	} {
		if got := test.value.String(); got != test.name {
			t.Fatalf("receipt mode %d string=%q, want %q", test.value, got, test.name)
		}
		if got, ok := ParsePlatformGenerationReceiptMode(test.name); !ok || got != test.value {
			t.Fatalf("parse receipt mode %q=(%d,%v), want (%d,true)", test.name, got, ok, test.value)
		}
	}
	if got, ok := ParsePlatformGenerationReceiptMode("unknown"); ok || got != 0 || PlatformGenerationReceiptMode(99).String() != "unknown" {
		t.Fatalf("invalid receipt mode did not fail closed: (%d,%v,%q)", got, ok, PlatformGenerationReceiptMode(99).String())
	}
	for _, test := range []struct {
		value PlatformGenerationResultStatus
		name  string
	}{
		{PlatformGenerationResultCompleted, "completed"},
		{PlatformGenerationResultFailed, "failed"},
	} {
		if got := test.value.String(); got != test.name {
			t.Fatalf("result status %d string=%q, want %q", test.value, got, test.name)
		}
		if got, ok := ParsePlatformGenerationResultStatus(test.name); !ok || got != test.value {
			t.Fatalf("parse result status %q=(%d,%v), want (%d,true)", test.name, got, ok, test.value)
		}
	}
	if got, ok := ParsePlatformGenerationResultStatus("unknown"); ok || got != 0 || PlatformGenerationResultStatus(99).String() != "unknown" {
		t.Fatalf("invalid result status did not fail closed: (%d,%v,%q)", got, ok, PlatformGenerationResultStatus(99).String())
	}
}

func TestPlatformGenerationPersistenceModelsMatchExactContract(t *testing.T) {
	type expectedField struct {
		goType  reflect.Type
		dbName  string
		gorm    string
		json    string
		size    int
		notNull bool
	}
	tests := []struct {
		value  any
		fields map[string]expectedField
	}{
		{PlatformChatGenerationReceipt{}, map[string]expectedField{
			"ID":                            {goType: reflect.TypeOf(int64(0)), dbName: "id", gorm: "primaryKey;type:bigint", json: "-"},
			"UserID":                        {goType: reflect.TypeOf(int64(0)), dbName: "user_id", gorm: "type:bigint;not null", json: "-", notNull: true},
			"GenerationID":                  {goType: reflect.TypeOf(""), dbName: "generation_id", gorm: "size:36;not null", json: "generation_id", size: 36, notNull: true},
			"Mode":                          {goType: reflect.TypeOf(PlatformGenerationReceiptMode(0)), dbName: "mode", gorm: "type:int;not null", json: "mode", notNull: true},
			"RequestedExistingConversation": {goType: reflect.TypeOf(int(0)), dbName: "requested_existing_conversation", gorm: "type:tinyint;not null", json: "-", notNull: true},
			"ConversationID":                {goType: reflect.TypeOf(int64(0)), dbName: "conversation_id", gorm: "type:bigint;not null", json: "-", notNull: true},
			"UserMessageID":                 {goType: reflect.TypeOf(int64(0)), dbName: "user_message_id", gorm: "type:bigint;not null", json: "-", notNull: true},
			"SuccessfulModelCount":          {goType: reflect.TypeOf(int(0)), dbName: "successful_model_count", gorm: "type:int;not null", json: "successful_model_count", notNull: true},
			"DailyCallsCharged":             {goType: reflect.TypeOf(int(0)), dbName: "daily_calls_charged", gorm: "type:int;not null", json: "daily_calls_charged", notNull: true},
			"TotalTokens":                   {goType: reflect.TypeOf(int64(0)), dbName: "total_tokens", gorm: "type:bigint;not null", json: "total_tokens", notNull: true},
			"CommittedAt":                   {goType: reflect.TypeOf(int64(0)), dbName: "committed_at", gorm: "type:bigint;not null", json: "committed_at", notNull: true},
		}},
		{PlatformChatGenerationResult{}, map[string]expectedField{
			"ID":                 {goType: reflect.TypeOf(int64(0)), dbName: "id", gorm: "primaryKey;type:bigint", json: "-"},
			"ReceiptID":          {goType: reflect.TypeOf(int64(0)), dbName: "receipt_id", gorm: "type:bigint;not null", json: "-", notNull: true},
			"ModelIndex":         {goType: reflect.TypeOf(int(0)), dbName: "model_index", gorm: "type:int;not null", json: "model_index", notNull: true},
			"Model":              {goType: reflect.TypeOf(""), dbName: "model", gorm: "size:128;not null", json: "model", size: 128, notNull: true},
			"Status":             {goType: reflect.TypeOf(PlatformGenerationResultStatus(0)), dbName: "status", gorm: "type:int;not null", json: "status", notNull: true},
			"AssistantMessageID": {goType: reflect.TypeOf((*int64)(nil)), dbName: "assistant_message_id", gorm: "type:bigint", json: "-"},
			"Tokens":             {goType: reflect.TypeOf(int64(0)), dbName: "tokens", gorm: "type:bigint;not null", json: "tokens", notNull: true},
			"ErrorCode":          {goType: reflect.TypeOf((*string)(nil)), dbName: "error_code", gorm: "size:64", json: "error_code,omitempty", size: 64},
		}},
	}
	for _, test := range tests {
		typ := reflect.TypeOf(test.value)
		if audit, ok := typ.FieldByName("AuditFields"); !ok || audit.Type != reflect.TypeOf(AuditFields{}) {
			t.Fatalf("%s must embed AuditFields exactly", typ.Name())
		}
		parsed, err := schema.Parse(reflect.New(typ).Interface(), &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatalf("parse %s schema: %v", typ.Name(), err)
		}
		for name, expected := range test.fields {
			field, ok := typ.FieldByName(name)
			if !ok || field.Type != expected.goType || field.Tag.Get("gorm") != expected.gorm || field.Tag.Get("json") != expected.json {
				t.Errorf("%s.%s contract mismatch: %#v", typ.Name(), name, field)
				continue
			}
			parsedField := parsed.LookUpField(name)
			if parsedField == nil || parsedField.DBName != expected.dbName {
				t.Errorf("%s.%s DBName=%v, want %q", typ.Name(), name, parsedField, expected.dbName)
				continue
			}
			if parsedField.NotNull != expected.notNull || (expected.size > 0 && parsedField.Size != expected.size) {
				t.Errorf("%s.%s schema null/size=(%v,%d), want (%v,%d)", typ.Name(), name, parsedField.NotNull, parsedField.Size, expected.notNull, expected.size)
			}
		}
	}

	encoded, err := json.Marshal(struct {
		Receipt PlatformChatGenerationReceipt `json:"receipt"`
		Result  PlatformChatGenerationResult  `json:"result"`
	}{Receipt: PlatformChatGenerationReceipt{ID: 1, UserID: 2, ConversationID: 3, UserMessageID: 4}, Result: PlatformChatGenerationResult{ID: 5, ReceiptID: 6, AssistantMessageID: new(int64)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"\"id\"", "user_id", "conversation_id", "user_message_id", "receipt_id", "assistant_message_id", "requested_existing_conversation"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("internal persistence ID leaked through JSON: %s", encoded)
		}
	}
}
