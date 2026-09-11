package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

func platformGenerationReceiptSchemaDB(t *testing.T) *gorm.DB {
	t.Helper()
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires isolated TEST_DATABASE_URL MySQL fixture")
	}
	return permissionSchemaDB(t)
}

func TestVerifyPlatformGenerationReceiptSchemaRejectsMissingAndPartialTables(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *gorm.DB)
	}{
		{name: "both_absent"},
		{name: "receipt_only", setup: func(t *testing.T, db *gorm.DB) {
			t.Helper()
			applyPlatformGenerationReceiptStatement(t, db, 0)
		}},
		{name: "result_only", setup: func(t *testing.T, db *gorm.DB) {
			t.Helper()
			if err := db.Exec(`CREATE TABLE platform_chat_generation_results (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`).Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "malformed_pair", setup: func(t *testing.T, db *gorm.DB) {
			t.Helper()
			if err := db.Exec(`CREATE TABLE platform_chat_generation_receipts (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(`CREATE TABLE platform_chat_generation_results (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`).Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := platformGenerationReceiptSchemaDB(t)
			applyThroughPlatformGenerationReceiptPredecessor(t, db)
			if tc.setup != nil {
				tc.setup(t, db)
			}
			err := VerifyPlatformGenerationReceiptSchema(context.Background(), db)
			if !errors.Is(err, ErrPlatformGenerationReceiptSchema) {
				t.Fatalf("VerifyPlatformGenerationReceiptSchema error = %v, want %v", err, ErrPlatformGenerationReceiptSchema)
			}
		})
	}
}

func TestUpRejectsPartialPlatformGenerationReceiptSchemaBeforeLedger(t *testing.T) {
	db := platformGenerationReceiptSchemaDB(t)
	applyThroughPlatformGenerationReceiptPredecessor(t, db)
	applyPlatformGenerationReceiptStatement(t, db, 0)

	generator := persistence.NewSnowflake(28, persistence.SystemClock())
	if err := Up(context.Background(), db, generator.Next, func() int64 { return 1_900_000_000_000 }); err == nil {
		t.Fatal("Up accepted a partial platform generation receipt schema")
	}
	var active int64
	if err := db.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version='0011' AND is_deleted=0").Row().Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("active 0011 ledger rows = %d, want 0", active)
	}
}

func TestUpVerifiesActivePlatformGenerationReceiptSchema(t *testing.T) {
	db := platformGenerationReceiptSchemaDB(t)
	permissionUp(t, db)
	if err := db.Exec("ALTER TABLE platform_chat_generation_results ADD COLUMN unexpected_value BIGINT NULL").Error; err != nil {
		t.Fatal(err)
	}
	generator := persistence.NewSnowflake(29, persistence.SystemClock())
	err := Up(context.Background(), db, generator.Next, func() int64 { return 1_900_000_000_000 })
	if !errors.Is(err, ErrPlatformGenerationReceiptSchema) {
		t.Fatalf("Up error = %v, want %v", err, ErrPlatformGenerationReceiptSchema)
	}
}

func applyThroughPlatformGenerationReceiptPredecessor(t *testing.T, db *gorm.DB) {
	t.Helper()
	generator := persistence.NewSnowflake(27, persistence.SystemClock())
	if err := Up(context.Background(), db, generator.Next, func() int64 { return 1_900_000_000_000 }); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP TABLE platform_chat_generation_results").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DROP TABLE platform_chat_generation_receipts").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE version='0011'").Error; err != nil {
		t.Fatal(err)
	}
}

func applyPlatformGenerationReceiptStatement(t *testing.T, db *gorm.DB, index int) {
	t.Helper()
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	statements := splitStatements(string(migrations[10].UpSQL))
	if index < 0 || index >= len(statements) {
		t.Fatalf("0011 statement index %d out of range %d", index, len(statements))
	}
	if err := db.Exec(statements[index]).Error; err != nil {
		t.Fatal(fmt.Errorf("apply 0011 statement %d: %w", index, err))
	}
}

// TestVerifyPlatformGenerationReceiptSchemaRejectsEveryMetadataDrift executes
// actual DDL against one fresh, test-owned MySQL child schema per subtest. The
// matcher-only branch matrix below is deliberately separate from this test.
func TestVerifyPlatformGenerationReceiptSchemaRejectsEveryMetadataDrift(t *testing.T) {
	tests := []struct {
		name   string
		ddl    []string
		mutate func(*testing.T, *gorm.DB)
	}{
		{name: "engine_real_with_required_foreign_keys_removed", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP FOREIGN KEY fk_platform_chat_generation_results_receipt",
			"ALTER TABLE platform_chat_generation_results DROP FOREIGN KEY fk_platform_chat_generation_results_message",
			"ALTER TABLE platform_chat_generation_results ENGINE=MyISAM",
		}},
		{name: "table_charset", ddl: []string{"ALTER TABLE platform_chat_generation_results DEFAULT CHARACTER SET latin1 COLLATE latin1_swedish_ci"}},
		{name: "extra_column", ddl: []string{"ALTER TABLE platform_chat_generation_results ADD COLUMN unexpected_value BIGINT NULL"}},
		{name: "column_order", ddl: []string{"ALTER TABLE platform_chat_generation_results MODIFY guid BIGINT NOT NULL AFTER receipt_id"}},
		{name: "column_type", ddl: []string{"ALTER TABLE platform_chat_generation_results MODIFY model VARCHAR(127) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL"}},
		{name: "column_signedness", ddl: []string{"ALTER TABLE platform_chat_generation_receipts MODIFY total_tokens BIGINT UNSIGNED NOT NULL"}},
		{name: "nullability", ddl: []string{"ALTER TABLE platform_chat_generation_results MODIFY error_code VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL"}},
		{name: "default", ddl: []string{"ALTER TABLE platform_chat_generation_results MODIFY tokens BIGINT NOT NULL DEFAULT 1"}},
		{name: "auto_increment", ddl: []string{"ALTER TABLE platform_chat_generation_results MODIFY id BIGINT NOT NULL"}},
		{name: "column_charset", ddl: []string{"ALTER TABLE platform_chat_generation_results MODIFY model VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL"}},
		{name: "column_collation", ddl: []string{"ALTER TABLE platform_chat_generation_results MODIFY model VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL"}},
		{name: "table_collation", ddl: []string{"ALTER TABLE platform_chat_generation_receipts DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"}},
		{name: "extra_index", ddl: []string{"ALTER TABLE platform_chat_generation_results ADD KEY idx_platform_chat_generation_results_unexpected (status)"}},
		{name: "missing_index", ddl: []string{"ALTER TABLE platform_chat_generation_receipts DROP INDEX idx_platform_chat_generation_receipts_owner_active_created"}},
		{name: "renamed_index", ddl: []string{"ALTER TABLE platform_chat_generation_receipts RENAME INDEX idx_platform_chat_generation_receipts_owner_active_created TO idx_platform_receipts_owner_drift"}},
		{name: "invisible_index", ddl: []string{"ALTER TABLE platform_chat_generation_receipts ALTER INDEX idx_platform_chat_generation_receipts_owner_active_created INVISIBLE"}},
		{name: "prefix_index", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP INDEX uk_platform_chat_generation_results_model",
			"ALTER TABLE platform_chat_generation_results ADD UNIQUE KEY uk_platform_chat_generation_results_model (receipt_id, model(64))",
		}},
		{name: "index_order", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP INDEX uk_platform_chat_generation_results_model",
			"ALTER TABLE platform_chat_generation_results ADD UNIQUE KEY uk_platform_chat_generation_results_model (model, receipt_id)",
		}},
		{name: "index_uniqueness", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP INDEX uk_platform_chat_generation_results_model",
			"ALTER TABLE platform_chat_generation_results ADD KEY uk_platform_chat_generation_results_model (receipt_id, model)",
		}},
		{name: "user_message_uniqueness_downgrade", ddl: []string{
			"ALTER TABLE platform_chat_generation_receipts DROP FOREIGN KEY fk_platform_chat_generation_receipts_user_message",
			"ALTER TABLE platform_chat_generation_receipts DROP INDEX uk_platform_chat_generation_receipts_user_message",
			"ALTER TABLE platform_chat_generation_receipts ADD KEY uk_platform_chat_generation_receipts_user_message (user_message_id)",
			"ALTER TABLE platform_chat_generation_receipts ADD CONSTRAINT fk_platform_chat_generation_receipts_user_message FOREIGN KEY (user_message_id) REFERENCES messages(id) ON DELETE RESTRICT ON UPDATE RESTRICT",
		}},
		{name: "foreign_key_target", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP FOREIGN KEY fk_platform_chat_generation_results_receipt",
			"ALTER TABLE platform_chat_generation_results ADD CONSTRAINT fk_platform_chat_generation_results_receipt FOREIGN KEY (receipt_id) REFERENCES platform_chat_generation_receipts(guid) ON DELETE RESTRICT ON UPDATE RESTRICT",
		}},
		{name: "missing_foreign_key", ddl: []string{"ALTER TABLE platform_chat_generation_results DROP FOREIGN KEY fk_platform_chat_generation_results_message"}},
		{name: "renamed_foreign_key", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP FOREIGN KEY fk_platform_chat_generation_results_message",
			"ALTER TABLE platform_chat_generation_results ADD CONSTRAINT fk_platform_chat_generation_results_message_drifted FOREIGN KEY (assistant_message_id) REFERENCES messages(id) ON DELETE RESTRICT ON UPDATE RESTRICT",
		}},
		{name: "extra_foreign_key", ddl: []string{"ALTER TABLE platform_chat_generation_results ADD CONSTRAINT fk_platform_chat_generation_results_unexpected FOREIGN KEY (guid) REFERENCES platform_chat_generation_receipts(guid) ON DELETE RESTRICT ON UPDATE RESTRICT"}},
		{name: "cross_schema_foreign_key", mutate: func(t *testing.T, source *gorm.DB) {
			t.Helper()
			target := platformGenerationReceiptSchemaDB(t)
			permissionUp(t, target)
			var targetSchema string
			if err := target.Raw("SELECT DATABASE()").Row().Scan(&targetSchema); err != nil || targetSchema == "" {
				t.Fatalf("load target test schema: %q %v", targetSchema, err)
			}
			if err := source.Exec("ALTER TABLE platform_chat_generation_results DROP FOREIGN KEY fk_platform_chat_generation_results_receipt").Error; err != nil {
				t.Fatal(err)
			}
			add := "ALTER TABLE platform_chat_generation_results ADD CONSTRAINT fk_platform_chat_generation_results_receipt FOREIGN KEY (receipt_id) REFERENCES `" + targetSchema + "`.platform_chat_generation_receipts(id) ON DELETE RESTRICT ON UPDATE RESTRICT"
			if err := source.Exec(add).Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := source.Exec("ALTER TABLE platform_chat_generation_results DROP FOREIGN KEY fk_platform_chat_generation_results_receipt").Error; err != nil {
					t.Errorf("drop cross-schema test foreign key: %v", err)
				}
			})
		}},
		{name: "foreign_key_delete_rule", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP FOREIGN KEY fk_platform_chat_generation_results_receipt",
			"ALTER TABLE platform_chat_generation_results ADD CONSTRAINT fk_platform_chat_generation_results_receipt FOREIGN KEY (receipt_id) REFERENCES platform_chat_generation_receipts(id) ON DELETE CASCADE ON UPDATE RESTRICT",
		}},
		{name: "foreign_key_update_rule", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP FOREIGN KEY fk_platform_chat_generation_results_receipt",
			"ALTER TABLE platform_chat_generation_results ADD CONSTRAINT fk_platform_chat_generation_results_receipt FOREIGN KEY (receipt_id) REFERENCES platform_chat_generation_receipts(id) ON DELETE RESTRICT ON UPDATE CASCADE",
		}},
		{name: "user_message_foreign_key_target", ddl: []string{
			"ALTER TABLE platform_chat_generation_receipts DROP FOREIGN KEY fk_platform_chat_generation_receipts_user_message",
			"ALTER TABLE platform_chat_generation_receipts ADD CONSTRAINT fk_platform_chat_generation_receipts_user_message FOREIGN KEY (user_message_id) REFERENCES conversations(id) ON DELETE RESTRICT ON UPDATE RESTRICT",
		}},
		{name: "missing_check", ddl: []string{"ALTER TABLE platform_chat_generation_results DROP CHECK chk_platform_chat_generation_results_tokens"}},
		{name: "requested_conversation_check", ddl: []string{
			"ALTER TABLE platform_chat_generation_receipts DROP CHECK chk_platform_chat_generation_receipts_requested_conversation",
			"ALTER TABLE platform_chat_generation_receipts ADD CONSTRAINT chk_platform_chat_generation_receipts_requested_conversation CHECK (requested_existing_conversation = 0)",
		}},
		{name: "renamed_check", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP CHECK chk_platform_chat_generation_results_tokens",
			"ALTER TABLE platform_chat_generation_results ADD CONSTRAINT chk_platform_chat_generation_results_tokens_drifted CHECK (tokens >= 0)",
		}},
		{name: "changed_check", ddl: []string{
			"ALTER TABLE platform_chat_generation_results DROP CHECK chk_platform_chat_generation_results_tokens",
			"ALTER TABLE platform_chat_generation_results ADD CONSTRAINT chk_platform_chat_generation_results_tokens CHECK (tokens >= 1)",
		}},
		{name: "extra_check", ddl: []string{"ALTER TABLE platform_chat_generation_results ADD CONSTRAINT chk_platform_chat_generation_results_unexpected CHECK (receipt_id > 0)"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db := platformGenerationReceiptSchemaDB(t)
			permissionUp(t, db)
			for _, ddl := range tc.ddl {
				if err := db.Exec(ddl).Error; err != nil {
					t.Fatalf("execute metadata drift %q: %v", ddl, err)
				}
			}
			if tc.mutate != nil {
				tc.mutate(t, db)
			}
			err := VerifyPlatformGenerationReceiptSchema(context.Background(), db)
			if !errors.Is(err, ErrPlatformGenerationReceiptSchema) {
				t.Fatalf("VerifyPlatformGenerationReceiptSchema error = %v, want typed mismatch", err)
			}
		})
	}
}

func TestVerifyReportsOldDatabaseAsUnmigratedBefore0011SchemaCheck(t *testing.T) {
	db := platformGenerationReceiptSchemaDB(t)
	permissionUp(t, db)
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range splitStatements(string(migrations[10].DownSQL)) {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec("DELETE FROM schema_migrations WHERE version='0011'").Error; err != nil {
		t.Fatal(err)
	}
	err = Verify(context.Background(), db)
	if err == nil || err.Error() != "database schema is not fully migrated" {
		t.Fatalf("Verify error = %v, want database schema is not fully migrated", err)
	}
	if errors.Is(err, ErrPlatformGenerationReceiptSchema) {
		t.Fatalf("old database reached 0011 live schema verifier: %v", err)
	}
}

func TestPlatformGenerationReceiptMigrationIsRerunnable(t *testing.T) {
	db := platformGenerationReceiptSchemaDB(t)
	permissionUp(t, db)
	permissionUp(t, db)
	var active int64
	if err := db.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version='0011' AND is_deleted=0").Row().Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active 0011 ledger rows = %d, want 1", active)
	}
}

type platformGenerationReceiptFixture struct {
	userID               int64
	conversationID       int64
	secondConversationID int64
	userMessageID        int64
	secondUserMessageID  int64
	assistantMessageID   int64
	receiptID            int64
}

func seedPlatformGenerationReceiptFixture(t *testing.T, db *gorm.DB) platformGenerationReceiptFixture {
	t.Helper()
	const now = int64(1_900_000_000_000)
	var groupID int64
	if err := db.Raw("SELECT id FROM business_groups WHERE BINARY group_key = BINARY 'default' AND status = 1 AND is_deleted = 0").Row().Scan(&groupID); err != nil || groupID <= 0 {
		t.Fatalf("load active default business group: id=%d err=%v", groupID, err)
	}
	if err := db.Exec(`INSERT INTO users (guid, group_id, allowed_models, created_at, updated_at, is_deleted) VALUES (?, ?, '[]', ?, ?, 0)`, 9_112_000_000_000_001, groupID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	var fixture platformGenerationReceiptFixture
	if err := db.Raw("SELECT id FROM users WHERE guid=?", 9_112_000_000_000_001).Row().Scan(&fixture.userID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO conversations (guid, user_id, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, 0)`, 9_112_000_000_000_002, fixture.userID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT id FROM conversations WHERE guid=?", 9_112_000_000_000_002).Row().Scan(&fixture.conversationID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO conversations (guid, user_id, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, 0)`, 9_112_000_000_000_020, fixture.userID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT id FROM conversations WHERE guid=?", 9_112_000_000_000_020).Row().Scan(&fixture.secondConversationID); err != nil {
		t.Fatal(err)
	}
	for _, message := range []struct {
		guid           int64
		conversationID int64
		role           int
	}{
		{guid: 9_112_000_000_000_003, conversationID: fixture.conversationID, role: 1},
		{guid: 9_112_000_000_000_004, conversationID: fixture.conversationID, role: 2},
		{guid: 9_112_000_000_000_021, conversationID: fixture.secondConversationID, role: 1},
	} {
		if err := db.Exec(`INSERT INTO messages (guid, conversation_id, role, content, created_at, updated_at, is_deleted) VALUES (?, ?, ?, 'receipt fixture', ?, ?, 0)`, message.guid, message.conversationID, message.role, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Raw("SELECT id FROM messages WHERE guid=?", 9_112_000_000_000_003).Row().Scan(&fixture.userMessageID); err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT id FROM messages WHERE guid=?", 9_112_000_000_000_004).Row().Scan(&fixture.assistantMessageID); err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT id FROM messages WHERE guid=?", 9_112_000_000_000_021).Row().Scan(&fixture.secondUserMessageID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO platform_chat_generation_receipts
		(guid,user_id,generation_id,mode,requested_existing_conversation,conversation_id,user_message_id,successful_model_count,daily_calls_charged,total_tokens,committed_at,created_at,updated_at,is_deleted)
		VALUES (?,?,'21111111-1111-4111-8111-111111111111',1,1,?,?,1,1,17,?,?,?,0)`,
		9_112_000_000_000_005, fixture.userID, fixture.conversationID, fixture.userMessageID, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Raw("SELECT id FROM platform_chat_generation_receipts WHERE guid=?", 9_112_000_000_000_005).Row().Scan(&fixture.receiptID); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO platform_chat_generation_results
      (guid,receipt_id,model_index,model,status,assistant_message_id,tokens,created_at,updated_at,is_deleted)
      VALUES (?,?,0,'owned-model',1,?,17,?,?,0)`, 9_112_000_000_000_006, fixture.receiptID, fixture.assistantMessageID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestPlatformGenerationReceiptUserMessageConstraints(t *testing.T) {
	db := platformGenerationReceiptSchemaDB(t)
	permissionUp(t, db)
	fixture := seedPlatformGenerationReceiptFixture(t, db)
	if fixture.secondConversationID <= 0 || fixture.secondUserMessageID <= 0 {
		t.Fatalf("second owned conversation/user message missing: %#v", fixture)
	}
	var ownedActiveUserMessages int64
	if err := db.Raw(`SELECT COUNT(*) FROM messages m JOIN conversations c ON c.id=m.conversation_id
      WHERE c.user_id=? AND c.is_deleted=0 AND m.role=1 AND m.is_deleted=0
      AND ((c.id=? AND m.id=?) OR (c.id=? AND m.id=?))`,
		fixture.userID, fixture.conversationID, fixture.userMessageID, fixture.secondConversationID, fixture.secondUserMessageID,
	).Row().Scan(&ownedActiveUserMessages); err != nil || ownedActiveUserMessages != 2 {
		t.Fatalf("active owned user-message fixture count = %d, err=%v; want 2", ownedActiveUserMessages, err)
	}
	const now = int64(1_900_000_000_000)
	insertReceipt := func(guid int64, generationID string, conversationID, userMessageID int64) error {
		return db.Exec(`INSERT INTO platform_chat_generation_receipts
		(guid,user_id,generation_id,mode,requested_existing_conversation,conversation_id,user_message_id,successful_model_count,daily_calls_charged,total_tokens,committed_at,created_at,updated_at,is_deleted)
		VALUES (?,?,?,1,1,?,?,1,1,0,?,?,?,0)`, guid, fixture.userID, generationID, conversationID, userMessageID, now, now, now).Error
	}
	if err := insertReceipt(9_112_000_000_000_007, "31111111-1111-4111-8111-111111111111", fixture.secondConversationID, fixture.userMessageID); err == nil {
		t.Fatal("global duplicate user_message_id in a different conversation was accepted")
	}
	if err := insertReceipt(9_112_000_000_000_008, "41111111-1111-4111-8111-111111111111", fixture.secondConversationID, 9_999_999_999); err == nil {
		t.Fatal("nonexistent user_message_id was accepted")
	}
	if err := db.Exec("DELETE FROM messages WHERE id=?", fixture.userMessageID).Error; err == nil {
		t.Fatal("deleting referenced user message was accepted")
	}
	if err := db.Exec("UPDATE messages SET id=id+1000000 WHERE id=?", fixture.userMessageID).Error; err == nil {
		t.Fatal("updating referenced user message id was accepted")
	}
}

func TestPlatformGenerationReceiptInvalidRowsAreRejected(t *testing.T) {
	t.Run("parent_successful_model_count_zero", func(t *testing.T) {
		db := platformGenerationReceiptSchemaDB(t)
		permissionUp(t, db)
		fixture := seedPlatformGenerationReceiptFixture(t, db)
		const now = int64(1_900_000_000_000)
		err := db.Exec(`INSERT INTO platform_chat_generation_receipts
		(guid,user_id,generation_id,mode,requested_existing_conversation,conversation_id,user_message_id,successful_model_count,daily_calls_charged,total_tokens,committed_at,created_at,updated_at,is_deleted)
		VALUES (?,?,?,1,1,?,?,0,0,0,?,?,?,0)`, 9_112_000_000_000_010, fixture.userID, "51111111-1111-4111-8111-111111111111", fixture.secondConversationID, fixture.secondUserMessageID, now, now, now).Error
		if err == nil {
			t.Fatal("receipt with successful_model_count=0 was accepted")
		}
	})
	t.Run("failed_child_with_assistant_message", func(t *testing.T) {
		db := platformGenerationReceiptSchemaDB(t)
		permissionUp(t, db)
		fixture := seedPlatformGenerationReceiptFixture(t, db)
		const now = int64(1_900_000_000_000)
		err := db.Exec(`INSERT INTO platform_chat_generation_results
        (guid,receipt_id,model_index,model,status,assistant_message_id,tokens,error_code,created_at,updated_at,is_deleted)
        VALUES (?,?,1,'failed-model',2,?,0,'upstream_failed',?,?,0)`, 9_112_000_000_000_011, fixture.receiptID, fixture.userMessageID, now, now).Error
		if err == nil {
			t.Fatal("failed result with non-null assistant_message_id was accepted")
		}
	})
}

func TestPlatformGenerationReceiptDownRemovesChildBeforeParent(t *testing.T) {
	db := platformGenerationReceiptSchemaDB(t)
	permissionUp(t, db)
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	statements := splitStatements(string(migrations[10].DownSQL))
	if len(statements) != 2 || !strings.Contains(statements[0], "platform_chat_generation_results") || !strings.Contains(statements[1], "platform_chat_generation_receipts") {
		t.Fatalf("0011 down order = %#v", statements)
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	var tables int64
	if err := db.Raw(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('platform_chat_generation_receipts','platform_chat_generation_results')`).Row().Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("0011 tables remaining after down = %d", tables)
	}
}

func TestVerifyPlatformGenerationReceiptSchemaRejectsNilDB(t *testing.T) {
	if err := VerifyPlatformGenerationReceiptSchema(context.Background(), nil); !errors.Is(err, ErrPlatformGenerationReceiptSchema) {
		t.Fatalf("nil DB error = %v, want typed mismatch", err)
	}
}

func TestPlatformGenerationReceiptContractsMatchExactMetadata(t *testing.T) {
	const schemaName = "receipt_contract_test"
	for _, contract := range platformGenerationReceiptTableContracts() {
		metadata := platformGenerationReceiptMetadataFromContract(contract, schemaName)
		if !matchesPlatformGenerationReceiptTableContract(contract, metadata, schemaName) {
			t.Fatalf("exact metadata rejected for %s", contract.table.name)
		}
	}
}

// TestPlatformGenerationReceiptMatcherRejectsEveryMetadataBranch covers each
// fail-closed comparison branch with one synthetic metadata mutation. It does
// not substitute for the real-MySQL drift cases above.
func TestPlatformGenerationReceiptMatcherRejectsEveryMetadataBranch(t *testing.T) {
	const schemaName = "receipt_matcher_test"
	contract := platformGenerationReceiptTableContracts()[1]
	tests := []struct {
		name   string
		mutate func(*businessGroupTableMetadata)
	}{
		{name: "engine", mutate: func(got *businessGroupTableMetadata) { got.engine = "MyISAM" }},
		{name: "table_charset", mutate: func(got *businessGroupTableMetadata) { got.characterSet = "latin1" }},
		{name: "table_collation", mutate: func(got *businessGroupTableMetadata) { got.collation = "utf8mb4_bin" }},
		{name: "missing_column", mutate: func(got *businessGroupTableMetadata) { got.columns = got.columns[:len(got.columns)-1] }},
		{name: "extra_column", mutate: func(got *businessGroupTableMetadata) {
			got.columns = append(got.columns, businessGroupColumnMetadata{name: "extra"})
		}},
		{name: "column_order", mutate: func(got *businessGroupTableMetadata) { got.columns[1], got.columns[2] = got.columns[2], got.columns[1] }},
		{name: "column_name", mutate: func(got *businessGroupTableMetadata) { got.columns[1].name = "guid_drifted" }},
		{name: "column_type", mutate: func(got *businessGroupTableMetadata) { got.columns[1].columnType = "int" }},
		{name: "column_unsigned", mutate: func(got *businessGroupTableMetadata) { got.columns[1].columnType = "bigint unsigned" }},
		{name: "column_nullability", mutate: func(got *businessGroupTableMetadata) { got.columns[1].nullable = "YES" }},
		{name: "column_default", mutate: func(got *businessGroupTableMetadata) {
			got.columns[7].defaultVal = sql.NullString{String: "1", Valid: true}
		}},
		{name: "column_extra", mutate: func(got *businessGroupTableMetadata) { got.columns[0].extra = "" }},
		{name: "column_charset", mutate: func(got *businessGroupTableMetadata) { got.columns[4].characterSet = "ascii" }},
		{name: "column_collation", mutate: func(got *businessGroupTableMetadata) { got.columns[4].collation = "utf8mb4_unicode_ci" }},
		{name: "missing_named_index", mutate: func(got *businessGroupTableMetadata) {
			got.indexes = removePlatformGenerationReceiptIndex(got.indexes, "uk_platform_chat_generation_results_model")
		}},
		{name: "extra_named_index", mutate: func(got *businessGroupTableMetadata) {
			got.indexes = append(got.indexes, validPlatformGenerationReceiptIndex("extra", "status", 1, false))
		}},
		{name: "index_column_count", mutate: func(got *businessGroupTableMetadata) { got.indexes = append(got.indexes[:3], got.indexes[4:]...) }},
		{name: "index_sequence", mutate: func(got *businessGroupTableMetadata) { got.indexes[2].sequence = 2 }},
		{name: "index_column", mutate: func(got *businessGroupTableMetadata) { got.indexes[2].column = "model" }},
		{name: "index_uniqueness", mutate: func(got *businessGroupTableMetadata) { got.indexes[2].nonUnique = 1 }},
		{name: "index_prefix", mutate: func(got *businessGroupTableMetadata) { got.indexes[2].subPart = sql.NullInt64{Int64: 64, Valid: true} }},
		{name: "index_missing_sort_collation", mutate: func(got *businessGroupTableMetadata) { got.indexes[2].collation = sql.NullString{} }},
		{name: "index_descending", mutate: func(got *businessGroupTableMetadata) {
			got.indexes[2].collation = sql.NullString{String: "D", Valid: true}
		}},
		{name: "index_non_btree", mutate: func(got *businessGroupTableMetadata) { got.indexes[2].indexType = "HASH" }},
		{name: "index_invisible", mutate: func(got *businessGroupTableMetadata) { got.indexes[2].visible = "NO" }},
		{name: "index_expression", mutate: func(got *businessGroupTableMetadata) {
			got.indexes[2].expression = sql.NullString{String: "(`model`)", Valid: true}
		}},
		{name: "missing_foreign_key", mutate: func(got *businessGroupTableMetadata) { got.foreignKeys = got.foreignKeys[:1] }},
		{name: "extra_foreign_key", mutate: func(got *businessGroupTableMetadata) {
			extra := got.foreignKeys[0]
			extra.name = "extra"
			got.foreignKeys = append(got.foreignKeys, extra)
		}},
		{name: "composite_foreign_key", mutate: func(got *businessGroupTableMetadata) {
			duplicate := got.foreignKeys[0]
			duplicate.ordinal = 2
			got.foreignKeys = append(got.foreignKeys, duplicate)
		}},
		{name: "foreign_key_ordinal", mutate: func(got *businessGroupTableMetadata) { got.foreignKeys[0].ordinal = 2 }},
		{name: "foreign_key_column", mutate: func(got *businessGroupTableMetadata) { got.foreignKeys[0].column = "guid" }},
		{name: "foreign_key_schema", mutate: func(got *businessGroupTableMetadata) { got.foreignKeys[0].targetSchema = "other_test" }},
		{name: "foreign_key_table", mutate: func(got *businessGroupTableMetadata) { got.foreignKeys[0].targetTable = "messages" }},
		{name: "foreign_key_target_column", mutate: func(got *businessGroupTableMetadata) { got.foreignKeys[0].targetColumn = "guid" }},
		{name: "foreign_key_delete_rule", mutate: func(got *businessGroupTableMetadata) { got.foreignKeys[0].deleteRule = "CASCADE" }},
		{name: "foreign_key_update_rule", mutate: func(got *businessGroupTableMetadata) { got.foreignKeys[0].updateRule = "CASCADE" }},
		{name: "missing_check", mutate: func(got *businessGroupTableMetadata) { got.checks = got.checks[:len(got.checks)-1] }},
		{name: "extra_check", mutate: func(got *businessGroupTableMetadata) {
			got.checks = append(got.checks, businessGroupCheckMetadata{name: "extra", clause: "id > 0", enforced: "YES"})
		}},
		{name: "duplicate_check", mutate: func(got *businessGroupTableMetadata) { got.checks = append(got.checks, got.checks[0]) }},
		{name: "check_not_enforced", mutate: func(got *businessGroupTableMetadata) { got.checks[0].enforced = "NO" }},
		{name: "check_changed", mutate: func(got *businessGroupTableMetadata) { got.checks[0].clause = "model_index >= 1" }},
		{name: "check_unparseable", mutate: func(got *businessGroupTableMetadata) { got.checks[0].clause = "model_index + 1 >= 0" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metadata := platformGenerationReceiptMetadataFromContract(contract, schemaName)
			tc.mutate(&metadata)
			if matchesPlatformGenerationReceiptTableContract(contract, metadata, schemaName) {
				t.Fatal("matcher accepted synthetic one-field metadata drift")
			}
		})
	}
}

func removePlatformGenerationReceiptIndex(indexes []businessGroupIndexMetadata, name string) []businessGroupIndexMetadata {
	filtered := make([]businessGroupIndexMetadata, 0, len(indexes))
	for _, index := range indexes {
		if index.name != name {
			filtered = append(filtered, index)
		}
	}
	return filtered
}

func validPlatformGenerationReceiptIndex(name, column string, sequence int, unique bool) businessGroupIndexMetadata {
	nonUnique := 1
	if unique {
		nonUnique = 0
	}
	return businessGroupIndexMetadata{
		name: name, column: column, sequence: sequence, nonUnique: nonUnique,
		collation: sql.NullString{String: "A", Valid: true}, indexType: "BTREE", visible: "YES",
	}
}

func platformGenerationReceiptMetadataFromContract(contract platformGenerationReceiptTableContract, schemaName string) businessGroupTableMetadata {
	metadata := businessGroupTableMetadata{engine: "InnoDB", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"}
	for _, column := range contract.table.columns {
		metadata.columns = append(metadata.columns, businessGroupColumnMetadata{
			name: column.name, columnType: column.columnType, nullable: column.nullable, defaultVal: column.defaultVal,
			extra: column.extra, characterSet: column.characterSet, collation: column.collation,
		})
	}
	for _, index := range contract.table.indexes {
		for position, column := range index.columns {
			nonUnique := 1
			if index.unique {
				nonUnique = 0
			}
			metadata.indexes = append(metadata.indexes, businessGroupIndexMetadata{
				name: index.name, column: column, sequence: position + 1, nonUnique: nonUnique,
				collation: sql.NullString{String: "A", Valid: true}, indexType: "BTREE", visible: "YES",
			})
		}
	}
	for _, foreignKey := range contract.foreignKeys {
		metadata.foreignKeys = append(metadata.foreignKeys, businessGroupForeignKeyMetadata{
			name: foreignKey.name, column: foreignKey.column, ordinal: 1, targetSchema: schemaName,
			targetTable: foreignKey.targetTable, targetColumn: foreignKey.targetColumn, deleteRule: "RESTRICT", updateRule: "RESTRICT",
		})
	}
	for _, check := range contract.table.checks {
		metadata.checks = append(metadata.checks, businessGroupCheckMetadata{name: check.name, clause: check.clause, enforced: "YES"})
	}
	return metadata
}

func TestAllIncludesPlatformGenerationReceiptMigration(t *testing.T) {
	assertPlatformGenerationReceiptMigrationContract(t)
}

func TestPlatformGenerationReceiptMigrationContract(t *testing.T) {
	assertPlatformGenerationReceiptMigrationContract(t)
}

func assertPlatformGenerationReceiptMigrationContract(t *testing.T) {
	t.Helper()
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 19 {
		t.Fatalf("All() returned %d migrations, want 13", len(migrations))
	}
	migration := migrations[10]
	if migration.Version != "0011" {
		t.Fatalf("All()[10] is %q, want platform generation migration 0011", migration.Version)
	}
	if len(migration.UpSQL) == 0 || len(migration.DownSQL) == 0 {
		t.Fatalf("0011 has empty SQL: up=%d down=%d", len(migration.UpSQL), len(migration.DownSQL))
	}

	up := strings.ToLower(string(migration.UpSQL))
	for _, fragment := range []string{
		"create table platform_chat_generation_receipts",
		"id bigint not null auto_increment primary key",
		"guid bigint not null",
		"user_id bigint not null",
		"generation_id char(36) character set ascii collate ascii_bin not null",
		"mode int not null",
		"requested_existing_conversation tinyint not null",
		"conversation_id bigint not null",
		"user_message_id bigint not null",
		"successful_model_count int not null",
		"daily_calls_charged int not null",
		"total_tokens bigint not null",
		"committed_at bigint not null",
		"created_at bigint not null",
		"created_by bigint null",
		"updated_at bigint not null",
		"updated_by bigint null",
		"is_deleted int not null default 0",
		"unique key uk_platform_chat_generation_receipts_guid (guid)",
		"unique key uk_platform_chat_generation_receipts_owner_generation (user_id, generation_id)",
		"unique key uk_platform_chat_generation_receipts_user_message (user_message_id)",
		"key idx_platform_chat_generation_receipts_owner_active_created (user_id, is_deleted, created_at)",
		"key idx_platform_chat_generation_receipts_conversation_active (conversation_id, is_deleted)",
		"constraint fk_platform_chat_generation_receipts_user foreign key (user_id) references users(id) on delete restrict on update restrict",
		"constraint fk_platform_chat_generation_receipts_conversation foreign key (conversation_id) references conversations(id) on delete restrict on update restrict",
		"constraint fk_platform_chat_generation_receipts_user_message foreign key (user_message_id) references messages(id) on delete restrict on update restrict",
		"constraint chk_platform_chat_generation_receipts_mode check (mode in (1, 2))",
		"constraint chk_platform_chat_generation_receipts_requested_conversation check (requested_existing_conversation in (0, 1))",
		"constraint chk_platform_chat_generation_receipts_counts check (successful_model_count >= 1 and daily_calls_charged = successful_model_count and total_tokens >= 0)",
		"constraint chk_platform_chat_generation_receipts_time check (committed_at > 0 and updated_at = created_at)",
		"constraint chk_platform_chat_generation_receipts_deleted check (is_deleted in (0, 1))",
		"create table platform_chat_generation_results",
		"receipt_id bigint not null",
		"model_index int not null",
		"model varchar(128) character set utf8mb4 collate utf8mb4_bin not null",
		"status int not null",
		"assistant_message_id bigint null",
		"tokens bigint not null default 0",
		"error_code varchar(64) null",
		"unique key uk_platform_chat_generation_results_guid (guid)",
		"unique key uk_platform_chat_generation_results_model (receipt_id, model)",
		"unique key uk_platform_chat_generation_results_position (receipt_id, model_index)",
		"unique key uk_platform_chat_generation_results_message (assistant_message_id)",
		"key idx_platform_chat_generation_results_receipt_active (receipt_id, is_deleted)",
		"constraint fk_platform_chat_generation_results_receipt foreign key (receipt_id) references platform_chat_generation_receipts(id) on delete restrict on update restrict",
		"constraint fk_platform_chat_generation_results_message foreign key (assistant_message_id) references messages(id) on delete restrict on update restrict",
		"constraint chk_platform_chat_generation_results_position check (model_index >= 0)",
		"constraint chk_platform_chat_generation_results_status check (status in (1, 2))",
		"constraint chk_platform_chat_generation_results_tokens check (tokens >= 0)",
		"constraint chk_platform_chat_generation_results_shape check (",
		"status = 1 and assistant_message_id is not null and error_code is null",
		"status = 2 and assistant_message_id is null and tokens = 0 and error_code is not null",
		"constraint chk_platform_chat_generation_results_time check (updated_at = created_at)",
		"constraint chk_platform_chat_generation_results_deleted check (is_deleted in (0, 1))",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0011 up missing %q", fragment)
		}
	}
	for _, forbidden := range []string{
		"timestamp",
		"datetime",
		"enum(",
		"prompt",
		"authorization",
		"response_content",
		"drop table",
	} {
		if strings.Contains(up, forbidden) {
			t.Errorf("0011 up contains forbidden %q", forbidden)
		}
	}
	if got := strings.Count(up, ") engine=innodb default charset=utf8mb4 collate=utf8mb4_unicode_ci;"); got != 2 {
		t.Errorf("0011 up has %d required table option clauses, want 2", got)
	}

	down := strings.ToLower(string(migration.DownSQL))
	const rollbackComment = "-- disposable local/test rollback only. production migrations remain forward-only."
	if !strings.Contains(down, rollbackComment) {
		t.Errorf("0011 down missing local/test rollback comment")
	}
	child := strings.Index(down, "drop table if exists platform_chat_generation_results")
	parent := strings.Index(down, "drop table if exists platform_chat_generation_receipts")
	if child < 0 || parent < 0 {
		t.Fatalf("0011 down missing drops: child=%d parent=%d", child, parent)
	}
	if child > parent {
		t.Fatal("0011 down must drop results before receipts")
	}
}

func TestPlatformGenerationReceiptMigrationPreservesCaseDistinctModelsOnIsolatedMySQL(t *testing.T) {
	gdb := platformGenerationReceiptSchemaDB(t)
	permissionUp(t, gdb)

	const now = int64(1_900_000_000_000)
	var groupID int64
	if err := gdb.Raw("SELECT id FROM business_groups WHERE BINARY group_key = BINARY 'default' AND status = 1 AND is_deleted = 0").Row().Scan(&groupID); err != nil || groupID <= 0 {
		t.Fatalf("load active default business group: id=%d err=%v", groupID, err)
	}
	if err := gdb.Exec(`INSERT INTO users (guid, group_id, allowed_models, created_at, updated_at, is_deleted) VALUES (?, ?, '[]', ?, ?, 0)`, 9_111_000_000_000_001, groupID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := gdb.Raw("SELECT id FROM users WHERE guid = ?", 9_111_000_000_000_001).Row().Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO conversations (guid, user_id, created_at, updated_at, is_deleted) VALUES (?, ?, ?, ?, 0)`, 9_111_000_000_000_002, userID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	var conversationID int64
	if err := gdb.Raw("SELECT id FROM conversations WHERE guid = ?", 9_111_000_000_000_002).Row().Scan(&conversationID); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO messages (guid, conversation_id, role, content, created_at, updated_at, is_deleted) VALUES (?, ?, 1, 'case-sensitive model fixture', ?, ?, 0)`, 9_111_000_000_000_003, conversationID, now, now).Error; err != nil {
		t.Fatal(err)
	}
	var userMessageID int64
	if err := gdb.Raw("SELECT id FROM messages WHERE guid = ?", 9_111_000_000_000_003).Row().Scan(&userMessageID); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO platform_chat_generation_receipts
		(guid, user_id, generation_id, mode, requested_existing_conversation, conversation_id, user_message_id, successful_model_count, daily_calls_charged, total_tokens, committed_at, created_at, updated_at, is_deleted)
		VALUES (?, ?, '11111111-1111-4111-8111-111111111111', 2, 1, ?, ?, 1, 1, 0, ?, ?, ?, 0)`,
		9_111_000_000_000_004, userID, conversationID, userMessageID, now, now, now).Error; err != nil {
		t.Fatal(err)
	}
	var receiptID int64
	if err := gdb.Raw("SELECT id FROM platform_chat_generation_receipts WHERE guid = ?", 9_111_000_000_000_004).Row().Scan(&receiptID); err != nil {
		t.Fatal(err)
	}
	for index, model := range []string{"model-a", "MODEL-A"} {
		if err := gdb.Exec(`INSERT INTO platform_chat_generation_results
          (guid, receipt_id, model_index, model, status, tokens, error_code, created_at, updated_at, is_deleted)
          VALUES (?, ?, ?, ?, 2, 0, 'upstream_failed', ?, ?, 0)`,
			9_111_000_000_000_005+int64(index), receiptID, index, model, now, now).Error; err != nil {
			t.Fatalf("insert case-distinct model %q: %v", model, err)
		}
	}
	var count int64
	if err := gdb.Raw("SELECT COUNT(*) FROM platform_chat_generation_results WHERE receipt_id = ?", receiptID).Row().Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("case-distinct model result count = %d, want 2", count)
	}
}
