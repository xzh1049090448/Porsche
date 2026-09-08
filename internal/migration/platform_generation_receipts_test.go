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
		"model varchar(128) not null",
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
