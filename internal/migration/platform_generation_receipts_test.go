package migration

import (
	"strings"
	"testing"
)

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
	if len(migrations) != 11 {
		t.Fatalf("All() returned %d migrations, want 11", len(migrations))
	}
	migration := migrations[10]
	if migration.Version != "0011" {
		t.Fatalf("All() ends at %q, want 0011", migration.Version)
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
	gdb := permissionSchemaDB(t)
	permissionUp(t, gdb)

	const now = int64(1_900_000_000_000)
	if err := gdb.Exec(`INSERT INTO users (guid, allowed_models, created_at, updated_at, is_deleted) VALUES (?, '[]', ?, ?, 0)`, 9_111_000_000_000_001, now, now).Error; err != nil {
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
      (guid, user_id, generation_id, mode, conversation_id, user_message_id, successful_model_count, daily_calls_charged, total_tokens, committed_at, created_at, updated_at, is_deleted)
      VALUES (?, ?, '11111111-1111-4111-8111-111111111111', 2, ?, ?, 1, 1, 0, ?, ?, ?, 0)`,
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
