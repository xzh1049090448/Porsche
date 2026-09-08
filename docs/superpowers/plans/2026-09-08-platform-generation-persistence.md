# Platform Generation Persistence Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the BE03 MySQL generation receipt schema and atomic single/compare finalization primitives that make successful SSE v2 generations durable, idempotent, quota-correct, and recoverable without activating any HTTP route.

**Architecture:** Migration `0011` adds an immutable parent receipt that uniquely references the exchange's user message plus ordered per-model result references; message content remains only in existing `messages` rows. `PlatformGenerationPersistence` validates a BE02 `committing` snapshot, commits conversation/messages/usage/quota/receipt in one MySQL transaction, and uses the receipt as the only recovery proof before reconciling Redis to `completed` or stale `committing` to `failed`.

**Tech Stack:** Go 1.22+, GORM, MySQL 8.4, Redis 7, embedded SQL migrations, Go `testing`, Docker-based loopback-only fixtures.

---

## Scope guard

This plan may create migration `0011`, persistence/reconciliation code, tests, verification evidence, and tracker updates for BE03. It must not activate v2 stream/status/cancel routes, modify frontend code, alter legacy chat behavior, run a production migration, deploy, push, merge, or call a real upstream model.

## Approved model-identifier compatibility decision

The user approved resolving the cross-tranche mismatch by tightening v2 model identifiers to well-formed UTF-8 of at most 128 bytes. BE01/BE02 previously accepted model identifiers up to 255 bytes, but the existing `conversations.model`, `messages.model`, and `usage_records.model` columns are `VARCHAR(128)`.

Keep the generic v2 opaque-identifier guard at 255 bytes while requiring well-formed UTF-8, add `platformSSEV2ModelIdentifier` with a 128-byte bound, and use it in the encoder, generation store, and BE03 persistence input for model values. Migration `0011` uses `VARCHAR(128)`. Malformed UTF-8 and model IDs of 129-255 bytes that the inactive BE01/BE02 primitives previously accepted are now rejected before Redis or MySQL. No v2 HTTP route is active, so there is no deployed v2 compatibility impact; catalog tests must prove every currently supported model ID fits 128 bytes.

**Alternative requiring broader authorization:** alter the three existing shared model columns to `VARCHAR(255)` in a separately designed migration. This affects legacy/non-v2 persistence and is outside the approved BE03 scope; this plan does not perform it.

All SQL, contracts, and tests below implement the approved 128-byte decision. BE03 must never silently truncate, store a null model, or allow an overlong model identifier to reach `committing` and then fail at MySQL.

## Explicit v2 user-message contract tightening

The current shared `whitelabel.ValidateRequest` accepts `messages: []`, and the legacy `lastUserMessage` helper returns an empty string when no user message exists. BE03 intentionally defines a narrower contract for the still-inactive v2 durable path: every successful exchange has exactly one final user message and its persisted content must be non-empty, well-formed UTF-8, and within the existing text-column byte bound. Completed assistant content has the same UTF-8 and byte requirements. Preserve the exact bytes, including whitespace; do not trim or normalize before idempotency comparison.

Task 3 adds the BE03 defensive validation. BE05 must also reject a v2 request with no non-empty final user message before Redis claim or upstream work. Do not change shared/legacy validation in this tranche, because doing so would alter existing gateway and legacy platform behavior. This compatibility delta is explicit and approved for the v2-only path; it does not authorize an HTTP route in BE03.

Before implementation, read:

- `docs/superpowers/specs/2026-09-08-platform-generation-persistence-design.md`
- `docs/conventions/database-standards.md`
- `docs/superpowers/plans/2026-09-07-platform-generation-registry.md`
- `docs/superpowers/reports/2026-09-08-platform-generation-registry.md`

## File responsibility map

| File | Responsibility |
| --- | --- |
| `internal/migration/sql/0011_platform_generation_receipts.up.sql` | Add the immutable receipt, its globally unique user-message reference, and ordered per-model result tables, indexes, foreign keys, and checks. |
| `internal/migration/sql/0011_platform_generation_receipts.down.sql` | Drop only the two BE03 tables in child-before-parent order for disposable fixture rollback. |
| `internal/migration/runner.go` | Embed/order `0011` and invoke its fail-closed structural verifier after application and at startup verification. |
| `internal/migration/platform_generation_receipts.go` | Verify the exact live MySQL schema for the two `0011` tables. |
| `internal/migration/platform_generation_receipts_test.go` | Static SQL contract plus real isolated MySQL migration, constraint, rerun, partial-schema, and down-order tests. |
| `internal/migration/runner_test.go` and published migration contract tests | Advance migration count/tail assertions from ten/`0010` to eleven/`0011` without changing prior checksums. |
| `internal/models/models.go` | Define stable receipt/result integer enums with bidirectional fail-closed mappings and GORM persistence entities. |
| `internal/models/models_contract_test.go` | Freeze enum integers/mappings, table names, exact Go types, signed IDs, audit fields, JSON exclusions, and GORM column/size/nullability mappings. |
| `internal/service/platform_sse_v2.go` and `internal/service/platform_sse_v2_test.go` | Add and freeze the approved 128-byte model-specific validator while retaining the 255-byte generic identifier guard. |
| `internal/service/platform_generation_store.go` and `internal/service/platform_generation_store_test.go` | Apply the approved model-specific validator consistently before Redis claim and model transitions. |
| `internal/service/platform_generation_persistence.go` | Own typed finalization input/output, pure validation, transaction runner seam, and atomic single/compare finalization. |
| `internal/service/platform_generation_receipt.go` | Load and integrity-check an owned receipt graph, retain the active user-message content only in the internal snapshot, and hydrate successful assistant content. |
| `internal/service/platform_generation_reconcile.go` | Resolve BE02 `committing` state from a valid receipt or fail a receipt-less stale commit after 30 seconds. |
| `internal/service/platform_generation_persistence_test.go` | Pure validation and real MySQL finalizer/reader/fault/idempotency/quota tests. |
| `internal/service/platform_generation_reconcile_test.go` | Real Redis + MySQL crash-window, CAS, and sensitive-data tests. |
| `internal/service/platform_generation_store.go` | Add only receipt-aware idempotent completion and stale-commit failure CAS operations. |
| `internal/service/platform_generation_store_test.go` | Freeze the new BE02-compatible CAS behavior and TTL preservation. |
| `internal/app/state.go` | Expose the persistence service when DB and the BE02 Redis store are available; register no route. |
| `internal/app/state_test.go` | Prove dependency construction and teardown without HTTP activation. |
| `feature_list.json`, `progress.md` | Record BE03 local evidence while keeping `go-018` in progress and routes/deployment unclaimed. |
| `docs/superpowers/reports/2026-09-08-platform-generation-persistence.md` | Preserve fixture identity, RED/GREEN commands, fault/race results, reviews, and explicit non-production boundary. |

### Task 1: Freeze and embed migration 0011

**Files:**

- Create: `internal/migration/sql/0011_platform_generation_receipts.up.sql`
- Create: `internal/migration/sql/0011_platform_generation_receipts.down.sql`
- Modify: `internal/migration/runner.go`
- Create: `internal/migration/platform_generation_receipts_test.go`
- Modify: `internal/migration/runner_test.go`
- Modify: migration count assertions returned by `rg -l 'len\(migrations\) != 10|ending at 0010|exactly ten migrations' internal/migration internal/service internal/handler`

- [ ] **Step 1: Write the failing embedded migration contract test**

Create the test with the exact name and core assertions below:

```go
func TestPlatformGenerationReceiptMigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 11 || migrations[10].Version != "0011" {
		t.Fatalf("All() = %d migrations ending at %q, want 11/0011", len(migrations), migrations[len(migrations)-1].Version)
	}
	up := strings.ToLower(string(migrations[10].UpSQL))
	for _, fragment := range []string{
		"create table platform_chat_generation_receipts",
		"generation_id char(36) character set ascii collate ascii_bin not null",
		"unique key uk_platform_chat_generation_receipts_owner_generation (user_id, generation_id)",
		"user_message_id bigint not null",
		"unique key uk_platform_chat_generation_receipts_user_message (user_message_id)",
		"foreign key (user_message_id) references messages(id) on delete restrict on update restrict",
		"create table platform_chat_generation_results",
		"model varchar(128) character set utf8mb4 collate utf8mb4_bin not null",
		"unique key uk_platform_chat_generation_results_model (receipt_id, model)",
		"unique key uk_platform_chat_generation_results_position (receipt_id, model_index)",
		"foreign key (assistant_message_id) references messages(id)",
		"check (mode in (1, 2))",
		"check (status in (1, 2))",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0011 missing %q:\n%s", fragment, up)
		}
	}
	for _, forbidden := range []string{"timestamp", "datetime", "enum(", "prompt", "authorization", "response_content", "drop table"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("0011 up contains forbidden %q", forbidden)
		}
	}
	down := strings.ToLower(string(migrations[10].DownSQL))
	if strings.Index(down, "drop table if exists platform_chat_generation_results") > strings.Index(down, "drop table if exists platform_chat_generation_receipts") {
		t.Fatal("0011 down must drop child before parent")
	}
}
```

Also add `TestPlatformGenerationReceiptMigrationPreservesCaseDistinctModelsOnIsolatedMySQL` in the same test file. It must use `permissionSchemaDB`, which reads only an explicit dedicated `TEST_DATABASE_URL` ending in `_test` and otherwise reports `SKIP`. Apply all migrations in the test-owned child database, insert one receipt, then prove its unique `(receipt_id, model)` index accepts two result rows whose opaque model IDs are `model-a` and `MODEL-A`.

Update every exact migration-count assertion found by the listed `rg` command to expect eleven migrations ending at `0011`; retain all prior positional checks for `0001`-`0010`.

- [ ] **Step 2: Run the contract test and confirm RED**

Run:

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/migration -run TestPlatformGenerationReceiptMigrationContract -count=1
```

Expected: FAIL because `All()` still returns ten migrations ending at `0010`.

- [ ] **Step 3: Add the complete up/down SQL**

Create `0011_platform_generation_receipts.up.sql` exactly as follows:

```sql
CREATE TABLE platform_chat_generation_receipts (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  generation_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  mode INT NOT NULL,
  conversation_id BIGINT NOT NULL,
  user_message_id BIGINT NOT NULL,
  successful_model_count INT NOT NULL,
  daily_calls_charged INT NOT NULL,
  total_tokens BIGINT NOT NULL,
  committed_at BIGINT NOT NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_platform_chat_generation_receipts_guid (guid),
  UNIQUE KEY uk_platform_chat_generation_receipts_owner_generation (user_id, generation_id),
  UNIQUE KEY uk_platform_chat_generation_receipts_user_message (user_message_id),
  KEY idx_platform_chat_generation_receipts_owner_active_created (user_id, is_deleted, created_at),
  KEY idx_platform_chat_generation_receipts_conversation_active (conversation_id, is_deleted),
  CONSTRAINT fk_platform_chat_generation_receipts_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT fk_platform_chat_generation_receipts_conversation FOREIGN KEY (conversation_id) REFERENCES conversations(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT fk_platform_chat_generation_receipts_user_message FOREIGN KEY (user_message_id) REFERENCES messages(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT chk_platform_chat_generation_receipts_mode CHECK (mode IN (1, 2)),
  CONSTRAINT chk_platform_chat_generation_receipts_counts CHECK (successful_model_count >= 1 AND daily_calls_charged = successful_model_count AND total_tokens >= 0),
  CONSTRAINT chk_platform_chat_generation_receipts_time CHECK (committed_at > 0 AND updated_at = created_at),
  CONSTRAINT chk_platform_chat_generation_receipts_deleted CHECK (is_deleted IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE platform_chat_generation_results (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  guid BIGINT NOT NULL,
  receipt_id BIGINT NOT NULL,
  model_index INT NOT NULL,
  model VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  status INT NOT NULL,
  assistant_message_id BIGINT NULL,
  tokens BIGINT NOT NULL DEFAULT 0,
  error_code VARCHAR(64) NULL,
  created_at BIGINT NOT NULL,
  created_by BIGINT NULL,
  updated_at BIGINT NOT NULL,
  updated_by BIGINT NULL,
  is_deleted INT NOT NULL DEFAULT 0,
  UNIQUE KEY uk_platform_chat_generation_results_guid (guid),
  UNIQUE KEY uk_platform_chat_generation_results_model (receipt_id, model),
  UNIQUE KEY uk_platform_chat_generation_results_position (receipt_id, model_index),
  UNIQUE KEY uk_platform_chat_generation_results_message (assistant_message_id),
  KEY idx_platform_chat_generation_results_receipt_active (receipt_id, is_deleted),
  CONSTRAINT fk_platform_chat_generation_results_receipt FOREIGN KEY (receipt_id) REFERENCES platform_chat_generation_receipts(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT fk_platform_chat_generation_results_message FOREIGN KEY (assistant_message_id) REFERENCES messages(id) ON DELETE RESTRICT ON UPDATE RESTRICT,
  CONSTRAINT chk_platform_chat_generation_results_position CHECK (model_index >= 0),
  CONSTRAINT chk_platform_chat_generation_results_status CHECK (status IN (1, 2)),
  CONSTRAINT chk_platform_chat_generation_results_tokens CHECK (tokens >= 0),
  CONSTRAINT chk_platform_chat_generation_results_shape CHECK (
    (status = 1 AND assistant_message_id IS NOT NULL AND error_code IS NULL) OR
    (status = 2 AND assistant_message_id IS NULL AND tokens = 0 AND error_code IS NOT NULL)
  ),
  CONSTRAINT chk_platform_chat_generation_results_time CHECK (updated_at = created_at),
  CONSTRAINT chk_platform_chat_generation_results_deleted CHECK (is_deleted IN (0, 1))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

Create `0011_platform_generation_receipts.down.sql` exactly as follows:

```sql
-- Disposable local/test rollback only. Production migrations remain forward-only.
DROP TABLE IF EXISTS platform_chat_generation_results;
DROP TABLE IF EXISTS platform_chat_generation_receipts;
```

- [ ] **Step 4: Embed and order 0011**

Add these declarations after the `0010` embeds in `runner.go`:

```go
//go:embed sql/0011_platform_generation_receipts.up.sql
var platformGenerationReceiptsUp []byte

//go:embed sql/0011_platform_generation_receipts.down.sql
var platformGenerationReceiptsDown []byte
```

Append this exact entry to the `All()` slice:

```go
{Version: "0011", UpSQL: platformGenerationReceiptsUp, DownSQL: platformGenerationReceiptsDown},
```

- [ ] **Step 5: Run the static migration suite and confirm GREEN**

Run:

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/migration -run 'TestPlatformGenerationReceiptMigrationContract|TestEmbeddedMigrations|Test.*MigrationContract' -count=1
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/migration -run TestPlatformGenerationReceiptMigrationPreservesCaseDistinctModelsOnIsolatedMySQL -count=1 -v
```

Expected: the static suite passes with no changed checksum expectation for `0001`-`0010`. The real MySQL constraint test passes when an explicit isolated fixture is available, or reports an explicit fixture `SKIP`; it must never fall back to production credentials.

- [ ] **Step 6: Commit the migration contract**

```bash
git add internal/migration/sql/0011_platform_generation_receipts.up.sql internal/migration/sql/0011_platform_generation_receipts.down.sql internal/migration/runner.go internal/migration/platform_generation_receipts_test.go internal/migration/runner_test.go internal/migration/*_test.go internal/service/*_test.go internal/handler/*_test.go
git commit -m "feat(migration): add generation receipt schema"
```

### Task 2: Add the fail-closed live schema verifier

**Files:**

- Create: `internal/migration/platform_generation_receipts.go`
- Modify: `internal/migration/runner.go`
- Modify: `internal/migration/platform_generation_receipts_test.go`

- [ ] **Step 1: Write RED tests for missing and partial schemas**

Add the exact tests:

```go
func TestVerifyPlatformGenerationReceiptSchemaRejectsMissingTable(t *testing.T) {
	gdb := permissionSchemaDB(t)
	if err := VerifyPlatformGenerationReceiptSchema(context.Background(), gdb); err == nil {
		t.Fatal("missing receipt tables were accepted")
	}
}

func TestPlatformGenerationReceiptMigrationOnIsolatedMySQL(t *testing.T) {
	gdb := permissionSchemaDB(t)
	permissionUp(t, gdb)
	if err := VerifyPlatformGenerationReceiptSchema(context.Background(), gdb); err != nil {
		t.Fatalf("verify 0011 schema: %v", err)
	}
	if err := gdb.Exec("DROP INDEX uk_platform_chat_generation_results_model ON platform_chat_generation_results").Error; err != nil {
		t.Fatal(err)
	}
	if err := VerifyPlatformGenerationReceiptSchema(context.Background(), gdb); err == nil {
		t.Fatal("partial 0011 schema was accepted")
	}
}
```

- [ ] **Step 2: Run verifier tests and confirm RED**

Run:

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/migration -run 'TestVerifyPlatformGenerationReceiptSchema|TestPlatformGenerationReceiptMigrationOnIsolatedMySQL' -count=1
```

Expected: FAIL to compile because `VerifyPlatformGenerationReceiptSchema` is undefined.

- [ ] **Step 3: Implement the exact schema verifier**

Create `platform_generation_receipts.go` with this implementation:

```go
package migration

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gorm.io/gorm"
)

var ErrPlatformGenerationReceiptSchema = errors.New("platform generation receipt schema mismatch or unavailable")

type platformGenerationForeignKeyContract struct {
	name, column, targetTable, targetColumn string
}

type platformGenerationTableContract struct {
	table       businessGroupTableContract
	foreignKeys []platformGenerationForeignKeyContract
}

func requiredPlatformGenerationColumn(name, columnType string) businessGroupColumnContract {
	return businessGroupColumnContract{name: name, columnType: columnType, nullable: "NO"}
}

func nullablePlatformGenerationColumn(name, columnType string) businessGroupColumnContract {
	return businessGroupColumnContract{name: name, columnType: columnType, nullable: "YES"}
}

func platformGenerationReceiptContracts() []platformGenerationTableContract {
	receiptColumns := []businessGroupColumnContract{
		requiredPlatformGenerationColumn("id", "bigint"),
		requiredPlatformGenerationColumn("guid", "bigint"),
		requiredPlatformGenerationColumn("user_id", "bigint"),
		requiredPlatformGenerationColumn("generation_id", "char(36)"),
		requiredPlatformGenerationColumn("mode", "int"),
		requiredPlatformGenerationColumn("conversation_id", "bigint"),
		requiredPlatformGenerationColumn("user_message_id", "bigint"),
		requiredPlatformGenerationColumn("successful_model_count", "int"),
		requiredPlatformGenerationColumn("daily_calls_charged", "int"),
		requiredPlatformGenerationColumn("total_tokens", "bigint"),
		requiredPlatformGenerationColumn("committed_at", "bigint"),
		requiredPlatformGenerationColumn("created_at", "bigint"),
		nullablePlatformGenerationColumn("created_by", "bigint"),
		requiredPlatformGenerationColumn("updated_at", "bigint"),
		nullablePlatformGenerationColumn("updated_by", "bigint"),
		requiredPlatformGenerationColumn("is_deleted", "int"),
	}
	receiptColumns[0].extra = "auto_increment"
	receiptColumns[3].characterSet, receiptColumns[3].collation = "ascii", "ascii_bin"
	receiptColumns[15].defaultVal = sql.NullString{String: "0", Valid: true}

	resultColumns := []businessGroupColumnContract{
		requiredPlatformGenerationColumn("id", "bigint"),
		requiredPlatformGenerationColumn("guid", "bigint"),
		requiredPlatformGenerationColumn("receipt_id", "bigint"),
		requiredPlatformGenerationColumn("model_index", "int"),
		requiredPlatformGenerationColumn("model", "varchar(128)"),
		requiredPlatformGenerationColumn("status", "int"),
		nullablePlatformGenerationColumn("assistant_message_id", "bigint"),
		requiredPlatformGenerationColumn("tokens", "bigint"),
		nullablePlatformGenerationColumn("error_code", "varchar(64)"),
		requiredPlatformGenerationColumn("created_at", "bigint"),
		nullablePlatformGenerationColumn("created_by", "bigint"),
		requiredPlatformGenerationColumn("updated_at", "bigint"),
		nullablePlatformGenerationColumn("updated_by", "bigint"),
		requiredPlatformGenerationColumn("is_deleted", "int"),
	}
	resultColumns[0].extra = "auto_increment"
	resultColumns[4].characterSet, resultColumns[4].collation = "utf8mb4", "utf8mb4_bin"
	resultColumns[7].defaultVal = sql.NullString{String: "0", Valid: true}
	resultColumns[8].characterSet, resultColumns[8].collation = "utf8mb4", "utf8mb4_unicode_ci"
	resultColumns[13].defaultVal = sql.NullString{String: "0", Valid: true}

	return []platformGenerationTableContract{
		{
			table: businessGroupTableContract{
				name: "platform_chat_generation_receipts", columns: receiptColumns,
				indexes: []businessGroupIndexContract{
					{name: "PRIMARY", columns: []string{"id"}, unique: true},
					{name: "uk_platform_chat_generation_receipts_guid", columns: []string{"guid"}, unique: true},
					{name: "uk_platform_chat_generation_receipts_owner_generation", columns: []string{"user_id", "generation_id"}, unique: true},
					{name: "uk_platform_chat_generation_receipts_user_message", columns: []string{"user_message_id"}, unique: true},
					{name: "idx_platform_chat_generation_receipts_owner_active_created", columns: []string{"user_id", "is_deleted", "created_at"}},
					{name: "idx_platform_chat_generation_receipts_conversation_active", columns: []string{"conversation_id", "is_deleted"}},
				},
				checks: []businessGroupCheckContract{
					{name: "chk_platform_chat_generation_receipts_mode", clause: "mode IN (1, 2)", enforced: "YES"},
					{name: "chk_platform_chat_generation_receipts_counts", clause: "successful_model_count >= 1 AND daily_calls_charged = successful_model_count AND total_tokens >= 0", enforced: "YES"},
					{name: "chk_platform_chat_generation_receipts_time", clause: "committed_at > 0 AND updated_at = created_at", enforced: "YES"},
					{name: "chk_platform_chat_generation_receipts_deleted", clause: "is_deleted IN (0, 1)", enforced: "YES"},
				},
			},
			foreignKeys: []platformGenerationForeignKeyContract{
				{name: "fk_platform_chat_generation_receipts_user", column: "user_id", targetTable: "users", targetColumn: "id"},
				{name: "fk_platform_chat_generation_receipts_conversation", column: "conversation_id", targetTable: "conversations", targetColumn: "id"},
				{name: "fk_platform_chat_generation_receipts_user_message", column: "user_message_id", targetTable: "messages", targetColumn: "id"},
			},
		},
		{
			table: businessGroupTableContract{
				name: "platform_chat_generation_results", columns: resultColumns,
				indexes: []businessGroupIndexContract{
					{name: "PRIMARY", columns: []string{"id"}, unique: true},
					{name: "uk_platform_chat_generation_results_guid", columns: []string{"guid"}, unique: true},
					{name: "uk_platform_chat_generation_results_model", columns: []string{"receipt_id", "model"}, unique: true},
					{name: "uk_platform_chat_generation_results_position", columns: []string{"receipt_id", "model_index"}, unique: true},
					{name: "uk_platform_chat_generation_results_message", columns: []string{"assistant_message_id"}, unique: true},
					{name: "idx_platform_chat_generation_results_receipt_active", columns: []string{"receipt_id", "is_deleted"}},
				},
				checks: []businessGroupCheckContract{
					{name: "chk_platform_chat_generation_results_position", clause: "model_index >= 0", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_status", clause: "status IN (1, 2)", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_tokens", clause: "tokens >= 0", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_shape", clause: "(status = 1 AND assistant_message_id IS NOT NULL AND error_code IS NULL) OR (status = 2 AND assistant_message_id IS NULL AND tokens = 0 AND error_code IS NOT NULL)", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_time", clause: "updated_at = created_at", enforced: "YES"},
					{name: "chk_platform_chat_generation_results_deleted", clause: "is_deleted IN (0, 1)", enforced: "YES"},
				},
			},
			foreignKeys: []platformGenerationForeignKeyContract{
				{name: "fk_platform_chat_generation_results_receipt", column: "receipt_id", targetTable: "platform_chat_generation_receipts", targetColumn: "id"},
				{name: "fk_platform_chat_generation_results_message", column: "assistant_message_id", targetTable: "messages", targetColumn: "id"},
			},
		},
	}
}

func VerifyPlatformGenerationReceiptSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPlatformGenerationReceiptSchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrPlatformGenerationReceiptSchema
	}
	for _, contract := range platformGenerationReceiptContracts() {
		actual, ok := loadBusinessGroupTableMetadata(ctx, db, contract.table.name)
		if !ok || !matchesPlatformGenerationReceiptTable(contract, actual, currentSchema) {
			return ErrPlatformGenerationReceiptSchema
		}
	}
	return nil
}

func matchesPlatformGenerationReceiptTable(want platformGenerationTableContract, got businessGroupTableMetadata, currentSchema string) bool {
	if got.engine != "InnoDB" || got.characterSet != "utf8mb4" || got.collation != "utf8mb4_unicode_ci" || len(got.columns) != len(want.table.columns) {
		return false
	}
	for index, expected := range want.table.columns {
		actual := got.columns[index]
		columnType := strings.ToLower(actual.columnType)
		if actual.name != expected.name || columnType != expected.columnType || strings.Contains(columnType, "unsigned") || actual.nullable != expected.nullable || actual.defaultVal != expected.defaultVal || strings.ToLower(actual.extra) != expected.extra || actual.characterSet != expected.characterSet || actual.collation != expected.collation {
			return false
		}
	}
	indexes := make(map[string][]businessGroupIndexMetadata, len(want.table.indexes))
	for _, row := range got.indexes {
		indexes[row.name] = append(indexes[row.name], row)
	}
	if len(indexes) != len(want.table.indexes) {
		return false
	}
	for _, expected := range want.table.indexes {
		rows := indexes[expected.name]
		if len(rows) != len(expected.columns) {
			return false
		}
		for index, row := range rows {
			if row.sequence != index+1 || row.column != expected.columns[index] || (row.nonUnique == 0) != expected.unique || !validRequiredBusinessGroupIndexMetadata(row) {
				return false
			}
		}
	}
	if len(got.foreignKeys) != len(want.foreignKeys) {
		return false
	}
	foreignKeys := make(map[string]businessGroupForeignKeyMetadata, len(got.foreignKeys))
	for _, row := range got.foreignKeys {
		if _, duplicate := foreignKeys[row.name]; duplicate {
			return false
		}
		foreignKeys[row.name] = row
	}
	for _, expected := range want.foreignKeys {
		row, ok := foreignKeys[expected.name]
		if !ok || row.ordinal != 1 || row.column != expected.column || row.targetSchema != currentSchema || row.targetTable != expected.targetTable || row.targetColumn != expected.targetColumn || !restrictRule(row.deleteRule) || !restrictRule(row.updateRule) {
			return false
		}
	}
	checks := make(map[string][]businessGroupCheckMetadata, len(want.table.checks))
	for _, row := range got.checks {
		checks[row.name] = append(checks[row.name], row)
	}
	if len(checks) != len(want.table.checks) {
		return false
	}
	for _, expected := range want.table.checks {
		rows := checks[expected.name]
		if len(rows) != 1 || rows[0].enforced != "YES" {
			return false
		}
		wantClause, wantOK := canonicalizeCheckClause(expected.clause)
		gotClause, gotOK := canonicalizeCheckClause(rows[0].clause)
		if !wantOK || !gotOK || wantClause != gotClause {
			return false
		}
	}
	return true
}
```

In `Up`, after applying or observing `0011`, call:

```go
if migration.Version == "0011" {
	if err := VerifyPlatformGenerationReceiptSchema(ctx, conn); err != nil {
		return err
	}
}
```

At the end of `Verify`, preserve all existing verifiers and then call:

```go
if err := VerifyAdminOperationResponseSchema(ctx, db); err != nil {
	return err
}
return VerifyPlatformGenerationReceiptSchema(ctx, db)
```

`VerifyApplied` must remain before every live-schema verifier. Consequently, an old database ending at `0010` reports the existing “database schema is not fully migrated” failure and never reaches the `0011` metadata query. `Up` invokes the receipt verifier only after successfully applying `0011` or after finding a checksum-matching active `0011` ledger row; a pre-ledger partial table pair fails closed and is not adopted.

- [ ] **Step 4: Prove real rerun, constraint enforcement, and down order**

Add the following drift matrix. Every subtest owns a fresh child database through `permissionSchemaDB(t)`, applies all migrations with `permissionUp`, performs exactly one metadata mutation, and requires the verifier to fail closed:

```go
func TestVerifyPlatformGenerationReceiptSchemaRejectsEveryMetadataDrift(t *testing.T) {
	cases := map[string]string{
		"extra column": `ALTER TABLE platform_chat_generation_receipts ADD COLUMN unexpected INT NULL`,
		"column order": `ALTER TABLE platform_chat_generation_receipts MODIFY COLUMN total_tokens BIGINT NOT NULL AFTER generation_id`,
		"column type and signedness": `ALTER TABLE platform_chat_generation_receipts MODIFY COLUMN total_tokens BIGINT UNSIGNED NOT NULL`,
		"column nullability": `ALTER TABLE platform_chat_generation_receipts MODIFY COLUMN total_tokens BIGINT NULL`,
		"column default": `ALTER TABLE platform_chat_generation_receipts ALTER COLUMN is_deleted DROP DEFAULT`,
		"column extra": `ALTER TABLE platform_chat_generation_receipts MODIFY COLUMN id BIGINT NOT NULL`,
		"column charset and collation": `ALTER TABLE platform_chat_generation_receipts MODIFY COLUMN generation_id CHAR(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci NOT NULL`,
		"table collation": `ALTER TABLE platform_chat_generation_receipts DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin`,
		"extra index": `ALTER TABLE platform_chat_generation_receipts ADD INDEX idx_unexpected (generation_id)`,
		"index order and uniqueness": `ALTER TABLE platform_chat_generation_receipts DROP INDEX uk_platform_chat_generation_receipts_owner_generation, ADD INDEX uk_platform_chat_generation_receipts_owner_generation (generation_id, user_id)`,
		"user message global uniqueness": `ALTER TABLE platform_chat_generation_receipts DROP FOREIGN KEY fk_platform_chat_generation_receipts_user_message, DROP INDEX uk_platform_chat_generation_receipts_user_message, ADD INDEX uk_platform_chat_generation_receipts_user_message (user_message_id), ADD CONSTRAINT fk_platform_chat_generation_receipts_user_message FOREIGN KEY (user_message_id) REFERENCES messages(id) ON DELETE RESTRICT ON UPDATE RESTRICT`,
		"foreign key target and rules": `ALTER TABLE platform_chat_generation_receipts DROP FOREIGN KEY fk_platform_chat_generation_receipts_user, ADD CONSTRAINT fk_platform_chat_generation_receipts_user FOREIGN KEY (user_id) REFERENCES users(guid) ON DELETE CASCADE ON UPDATE CASCADE`,
		"user message foreign key target": `ALTER TABLE platform_chat_generation_receipts DROP FOREIGN KEY fk_platform_chat_generation_receipts_user_message, ADD CONSTRAINT fk_platform_chat_generation_receipts_user_message FOREIGN KEY (user_message_id) REFERENCES messages(guid) ON DELETE RESTRICT ON UPDATE RESTRICT`,
		"missing check": `ALTER TABLE platform_chat_generation_receipts DROP CHECK chk_platform_chat_generation_receipts_counts`,
		"extra constraint": `ALTER TABLE platform_chat_generation_receipts ADD CONSTRAINT chk_unexpected CHECK (guid > 0)`,
	}
	for name, mutation := range cases {
		t.Run(name, func(t *testing.T) {
			gdb := permissionSchemaDB(t)
			permissionUp(t, gdb)
			if err := gdb.Exec(mutation).Error; err != nil {
				t.Fatalf("apply metadata mutation: %v", err)
			}
			if err := VerifyPlatformGenerationReceiptSchema(context.Background(), gdb); !errors.Is(err, ErrPlatformGenerationReceiptSchema) {
				t.Fatalf("VerifyPlatformGenerationReceiptSchema() error=%v, want fail-closed schema mismatch", err)
			}
		})
	}
}

func TestVerifyReportsOldDatabaseAsUnmigratedBefore0011SchemaCheck(t *testing.T) {
	gdb := permissionSchemaDB(t)
	permissionUp(t, gdb)
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range splitStatements(string(migrations[10].DownSQL)) {
		if err := gdb.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version='0011'").Error; err != nil {
		t.Fatal(err)
	}
	err = Verify(context.Background(), gdb)
	if err == nil || !strings.Contains(err.Error(), "database schema is not fully migrated") || errors.Is(err, ErrPlatformGenerationReceiptSchema) {
		t.Fatalf("Verify() error=%v, want migration-ledger failure before 0011 schema verification", err)
	}
}
```

Also split rerun, invalid-row constraints, and down-order assertions into fresh-database subtests. Call `permissionUp` twice and require one active `0011` ledger row. Add `TestPlatformGenerationReceiptUserMessageConstraints` that seeds two active owned conversations and two active user messages, inserts one valid receipt, then proves: a second receipt cannot reuse its `user_message_id`; a nonexistent `user_message_id` is rejected; and both deletion and primary-key update of the referenced message are rejected by the `RESTRICT` foreign key. Insert one parent with `successful_model_count=0` and one failed result with a non-null assistant message and require MySQL errors; execute `DownSQL` and require both tables absent. Do not combine these cases with the drift matrix because each mutation deliberately leaves its child schema invalid.

Run:

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/migration -run 'TestPlatformGenerationReceipt|TestVerifyPlatformGenerationReceipt|TestVerifyReportsOldDatabase' -count=1
```

Expected: PASS; every column/table/index/FK/CHECK drift fails closed, extra metadata is rejected, user-message global uniqueness and `RESTRICT` references are enforced, an old ten-migration database fails at ledger completeness, rerun does not add a second `0011` ledger row, invalid rows fail, and down removes child before parent.

- [ ] **Step 5: Commit the verifier**

```bash
git add internal/migration/platform_generation_receipts.go internal/migration/platform_generation_receipts_test.go internal/migration/runner.go
git commit -m "feat(migration): verify generation receipt schema"
```

### Task 3: Define persistence models and typed input validation

**Files:**

- Modify: `internal/models/models.go`
- Modify: `internal/models/models_contract_test.go`
- Modify: `internal/service/platform_sse_v2.go`
- Modify: `internal/service/platform_sse_v2_test.go`
- Modify: `internal/service/platform_generation_store.go`
- Modify: `internal/service/platform_generation_store_test.go`
- Create: `internal/service/platform_generation_persistence.go`
- Create: `internal/service/platform_generation_persistence_test.go`

- [ ] **Step 1: Write RED enum/model contract tests**

Add:

```go
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
}
```

Add `TestValidatePlatformGenerationPersistenceInputRejectsInvalidBeforeDependencies` as a table-driven pure-validation test covering zero user ID, malformed UUID, unsafe time, invalid mode/cardinality, duplicate/oversized model, missing/extra result, model-order mismatch, running/cancelling/cancelled result, single failure, compare all-failure, completed result with empty content or invalid token count, failed result with content/tokens/missing or unstable code, empty user message, and oversized user message. Every invalid case calls `validatePlatformGenerationPersistenceInput` directly, expects `ErrPlatformGenerationPersistenceInvalid`, and therefore requires no Redis or MySQL fixture.

The enum contract test must also round-trip `single`/`compare` and `completed`/`failed`, rejecting unknown strings and rendering unknown integer values as `unknown`. Add a GORM schema/reflection contract test for every new direct field's Go type, column name, explicit size/nullability tag, and internal-ID JSON exclusion. Add malformed UTF-8 cases for generic/model identifiers, Redis claim, user-message content, and completed assistant content. Add pure advisory-lock invalid-input tests plus an explicit `TEST_DATABASE_URL` integration test proving the callback runs on the connection that owns the lock and that success, callback error, and caller cancellation all leave the lock free; missing MySQL reports `BLOCKED_FIXTURE`.

Also add the approved compatibility-boundary tests:

```go
func TestPlatformSSEV2ModelIdentifierMatchesExistingPersistenceColumns(t *testing.T) {
	if !platformSSEV2ModelIdentifier(strings.Repeat("m", 128)) {
		t.Fatal("128-byte model identifier was rejected")
	}
	if platformSSEV2ModelIdentifier(strings.Repeat("m", 129)) {
		t.Fatal("129-byte model identifier would overflow existing model columns")
	}
	if !platformSSEV2Identifier(strings.Repeat("i", 255)) {
		t.Fatal("model-specific bound unexpectedly narrowed generic opaque identifiers")
	}
}

func TestPlatformGenerationStoreRejectsModelOverPersistenceLimitBeforeRedis(t *testing.T) {
	store, err := NewPlatformGenerationStore(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"}))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Claim(context.Background(), PlatformGenerationClaimInput{
		UserID: 1, GenerationID: generationTestID, Mode: PlatformGenerationModeSingle,
		Models: []string{strings.Repeat("m", 129)}, NowMillis: 1,
	})
	if !errors.Is(err, ErrPlatformGenerationInvalid) {
		t.Fatalf("Claim() error=%v, want invalid before Redis", err)
	}
}
```

- [ ] **Step 2: Run model/validation tests and confirm RED**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/models ./internal/service -run 'TestPlatformGenerationPersistenceEnumsAreStable|TestValidatePlatformGenerationPersistenceInput' -count=1
```

Expected: FAIL to compile because the model enums/entities and typed persistence input do not exist.

- [ ] **Step 3: Apply the approved model-specific 128-byte boundary**

Keep the BE01 generic bound and add the model-specific validator in `platform_sse_v2.go`:

```go
const platformSSEV2MaxModelIdentifierBytes = 128

func platformSSEV2ModelIdentifier(value string) bool {
	return platformSSEV2Identifier(value) && len([]byte(value)) <= platformSSEV2MaxModelIdentifierBytes
}
```

`platformSSEV2Identifier` itself must require `utf8.ValidString(value)` before applying whitespace and the generic 255-byte bound. This is v2-only; do not change shared/legacy validation.

Use `platformSSEV2ModelIdentifier` instead of `platformSSEV2Identifier` for every model value in `NewPlatformSSEV2Encoder`, `validatePlatformGenerationInput`, `RecordDelta`, `MarkModelDone`, `MarkModelFailed`, and stored-snapshot model validation. Keep `platformSSEV2Identifier` for conversation GUID and other generic opaque identifiers. Run:

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/service -run 'TestPlatformSSEV2ModelIdentifierMatchesExistingPersistenceColumns|TestPlatformGenerationStoreRejectsModelOverPersistenceLimitBeforeRedis|TestPlatform.*SSE.*V2|TestPlatformGenerationStoreRejectsInvalidInputBeforeRedis' -count=1
```

Expected: PASS; 129-255-byte model IDs are rejected before Redis while generic identifiers retain the BE01 255-byte bound.

- [ ] **Step 4: Add the exact model types**

Append these stable enums and entities to `models.go`:

```go
type PlatformGenerationReceiptMode int

const (
	PlatformGenerationReceiptModeSingle  PlatformGenerationReceiptMode = 1
	PlatformGenerationReceiptModeCompare PlatformGenerationReceiptMode = 2
)

type PlatformGenerationResultStatus int

const (
	PlatformGenerationResultCompleted PlatformGenerationResultStatus = 1
	PlatformGenerationResultFailed    PlatformGenerationResultStatus = 2
)

type PlatformChatGenerationReceipt struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	UserID               int64                         `gorm:"type:bigint;not null" json:"-"`
	GenerationID         string                        `gorm:"size:36;not null" json:"generation_id"`
	Mode                 PlatformGenerationReceiptMode `gorm:"type:int;not null" json:"mode"`
	ConversationID       int64                         `gorm:"type:bigint;not null" json:"-"`
	UserMessageID         int64                         `gorm:"type:bigint;not null" json:"-"`
	SuccessfulModelCount int                           `gorm:"type:int;not null" json:"successful_model_count"`
	DailyCallsCharged    int                           `gorm:"type:int;not null" json:"daily_calls_charged"`
	TotalTokens          int64                         `gorm:"type:bigint;not null" json:"total_tokens"`
	CommittedAt          int64                         `gorm:"type:bigint;not null" json:"committed_at"`
}

func (PlatformChatGenerationReceipt) TableName() string {
	return "platform_chat_generation_receipts"
}

type PlatformChatGenerationResult struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	ReceiptID         int64                          `gorm:"type:bigint;not null" json:"-"`
	ModelIndex        int                            `gorm:"type:int;not null" json:"model_index"`
	Model             string                         `gorm:"size:128;not null" json:"model"`
	Status            PlatformGenerationResultStatus `gorm:"type:int;not null" json:"status"`
	AssistantMessageID *int64                         `gorm:"type:bigint" json:"-"`
	Tokens            int64                          `gorm:"type:bigint;not null" json:"tokens"`
	ErrorCode         *string                        `gorm:"size:64" json:"error_code,omitempty"`
}

func (PlatformChatGenerationResult) TableName() string {
	return "platform_chat_generation_results"
}
```

Implement explicit `String()` and `ParsePlatformGenerationReceiptMode` / `ParsePlatformGenerationResultStatus` mappings for the four published names. Unknown integers return `"unknown"`; unknown strings return `(0, false)`.

Keep alignment through `gofmt`; do not expose internal IDs in public JSON.

- [ ] **Step 5: Add typed persistence values and pure validation**

Create the initial `platform_generation_persistence.go` with these names and signatures:

```go
package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strconv"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const (
	platformGenerationMessageTextMaxBytes         = 65535
	platformGenerationAdvisoryLockReleaseTimeout = 2 * time.Second
	platformGenerationReceiptRecoveryTimeout     = 2 * time.Second
)

var (
	ErrPlatformGenerationPersistenceInvalid     = errors.New("invalid platform generation persistence input")
	ErrPlatformGenerationPersistenceNotFound    = errors.New("platform generation receipt not found")
	ErrPlatformGenerationPersistenceIntegrity   = errors.New("platform generation receipt integrity failure")
	ErrPlatformGenerationPersistenceConflict    = errors.New("platform generation persistence conflict")
	ErrPlatformGenerationPersistenceUnavailable = errors.New("platform generation persistence unavailable")
	ErrPlatformGenerationPersistenceQuota       = errors.New("platform generation quota unavailable")
)

type PlatformGenerationPersistenceResult struct {
	Model     string
	State     PlatformGenerationState
	Content   string
	Tokens    int64
	Seq       int64
	ErrorCode string
}

type PlatformGenerationPersistenceInput struct {
	UserID           int64
	GenerationID     string
	Mode             PlatformGenerationMode
	Models           []string
	ConversationGUID *int64
	UserMessage      string
	Results          []PlatformGenerationPersistenceResult
	NowMillis        int64
}

type PlatformGenerationCommittedResult struct {
	Model                string
	State                PlatformGenerationState
	AssistantMessageGUID string
	Content              string
	Tokens               int64
	ErrorCode            string
}

type PlatformGenerationReceiptSnapshot struct {
	UserID               int64
	GenerationID         string
	Mode                 PlatformGenerationMode
	ConversationGUID     int64
	UserMessage          string `json:"-"`
	SuccessfulModelCount int
	DailyCallsCharged    int
	TotalTokens          int64
	CommittedAtMillis    int64
	Results              []PlatformGenerationCommittedResult
}

type platformGenerationLockRunner func(context.Context, *gorm.DB, string, func(*gorm.DB) error) error
type platformGenerationReceiptReader func(context.Context, *gorm.DB, int64, string) (PlatformGenerationReceiptSnapshot, error)

type PlatformGenerationPersistence struct {
	generations *PlatformGenerationStore
	runLocked   platformGenerationLockRunner
	loadReceipt platformGenerationReceiptReader
}

func NewPlatformGenerationPersistence(generations *PlatformGenerationStore) (*PlatformGenerationPersistence, error) {
	if generations == nil {
		return nil, ErrPlatformGenerationPersistenceUnavailable
	}
	return &PlatformGenerationPersistence{
		generations: generations,
		loadReceipt: LoadPlatformGenerationReceipt,
		runLocked:   withPlatformGenerationAdvisoryLock,
	}, nil
}

func platformGenerationAdvisoryLockName(userID int64, generationID string) string {
	digest := sha256.Sum256([]byte(strconv.FormatInt(userID, 10) + ":" + generationID))
	return "porsche:gen:v2:" + hex.EncodeToString(digest[:16])
}

func withPlatformGenerationAdvisoryLock(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
	if ctx == nil || db == nil || len(lockName) == 0 || len(lockName) > 64 || fn == nil {
		return ErrPlatformGenerationPersistenceInvalid
	}
	return db.WithContext(ctx).Connection(func(conn *gorm.DB) error {
		var acquired sql.NullInt64
		if err := conn.Raw("SELECT GET_LOCK(?, 5)", lockName).Scan(&acquired).Error; err != nil || !acquired.Valid || acquired.Int64 != 1 {
			return ErrPlatformGenerationPersistenceUnavailable
		}
		return runWithPlatformGenerationAdvisoryLockRelease(conn, lockName, fn)
	})
}

func runWithPlatformGenerationAdvisoryLockRelease(conn *gorm.DB, lockName string, fn func(*gorm.DB) error) (primaryErr error) {
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), platformGenerationAdvisoryLockReleaseTimeout)
		defer cancel()
		var released sql.NullInt64
		releaseErr := conn.WithContext(cleanupCtx).Raw("SELECT RELEASE_LOCK(?)", lockName).Scan(&released).Error
		if primaryErr == nil && (releaseErr != nil || !released.Valid || released.Int64 != 1) {
			primaryErr = ErrPlatformGenerationPersistenceUnavailable
		}
	}()
	primaryErr = fn(conn)
	return primaryErr
}

func validatePlatformGenerationPersistenceInput(input PlatformGenerationPersistenceInput) error {
	if validatePlatformGenerationIdentity(input.UserID, input.GenerationID) != nil ||
		!platformSSEV2SafeInteger(input.NowMillis) || input.NowMillis <= 0 ||
		input.UserMessage == "" || !utf8.ValidString(input.UserMessage) ||
		len([]byte(input.UserMessage)) > platformGenerationMessageTextMaxBytes ||
		len(input.Models) != len(input.Results) {
		return ErrPlatformGenerationPersistenceInvalid
	}
	if validatePlatformGenerationInput(PlatformGenerationClaimInput{
		UserID: input.UserID, GenerationID: input.GenerationID, Mode: input.Mode,
		Models: input.Models, NowMillis: input.NowMillis,
	}) != nil {
		return ErrPlatformGenerationPersistenceInvalid
	}
	successes := 0
	for index, result := range input.Results {
		if result.Model != input.Models[index] || !platformSSEV2ModelIdentifier(result.Model) ||
			!utf8.ValidString(result.Content) ||
			len([]byte(result.Content)) > platformGenerationMessageTextMaxBytes ||
			result.Tokens < 0 || result.Tokens > math.MaxInt32 ||
			result.Seq < 0 || !platformSSEV2SafeInteger(result.Seq) {
			return ErrPlatformGenerationPersistenceInvalid
		}
		switch result.State {
		case PlatformGenerationStateCompleted:
			if result.Content == "" || result.ErrorCode != "" {
				return ErrPlatformGenerationPersistenceInvalid
			}
			successes++
		case PlatformGenerationStateFailed:
			if result.Content != "" || result.Tokens != 0 || !platformGenerationStableCode(result.ErrorCode) {
				return ErrPlatformGenerationPersistenceInvalid
			}
		default:
			return ErrPlatformGenerationPersistenceInvalid
		}
	}
	if successes == 0 || (input.Mode == PlatformGenerationModeSingle && successes != 1) {
		return ErrPlatformGenerationPersistenceInvalid
	}
	if input.ConversationGUID != nil && *input.ConversationGUID <= 0 {
		return ErrPlatformGenerationPersistenceInvalid
	}
	return nil
}
```

- [ ] **Step 6: Run focused tests and confirm GREEN**

```bash
gofmt -w internal/models/models.go internal/models/models_contract_test.go internal/service/platform_generation_persistence.go internal/service/platform_generation_persistence_test.go
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/models ./internal/service -run 'TestPlatformGenerationPersistenceEnumsAreStable|TestValidatePlatformGenerationPersistenceInput' -count=1
```

Expected: PASS; empty or malformed UTF-8 user messages, malformed UTF-8 completed content/identifiers, invalid timestamps, and byte-overflowing `TEXT`/model values fail without touching Redis/MySQL. The user-message value is compared and persisted byte-for-byte without trimming; the limit uses `len([]byte(value))`, matching MySQL byte capacity rather than rune count. Advisory-lock cleanup is bounded, uses the pinned connection after caller cancellation, preserves a primary callback error, and fails closed on an unverified release. Existing `whitelabel.TestValidateRequestEnforcesChatContract` remains unchanged and green because the stricter rule belongs only to the inactive v2 durable path.

- [ ] **Step 7: Commit typed contracts**

```bash
git add internal/models/models.go internal/models/models_contract_test.go internal/service/platform_sse_v2.go internal/service/platform_sse_v2_test.go internal/service/platform_generation_store.go internal/service/platform_generation_store_test.go internal/service/platform_generation_persistence.go internal/service/platform_generation_persistence_test.go
git commit -m "feat(platform): define generation persistence contract"
```

### Task 4: Implement the owned receipt reader and integrity graph

**Files:**

- Create: `internal/service/platform_generation_receipt.go`
- Modify: `internal/service/platform_generation_persistence_test.go`

- [ ] **Step 1: Write RED receipt-reader integration tests**

Add exact test names:

```go
func TestLoadPlatformGenerationReceiptHydratesOwnedSingleResult(t *testing.T)
func TestLoadPlatformGenerationReceiptPreservesCompareModelOrder(t *testing.T)
func TestLoadPlatformGenerationReceiptRejectsCrossUserAccess(t *testing.T)
func TestLoadPlatformGenerationReceiptRejectsMalformedGraph(t *testing.T)
func TestLoadPlatformGenerationReceiptRejectsInvalidParentScalars(t *testing.T)
func TestLoadPlatformGenerationReceiptRejectsDeletedOrMismatchedMessage(t *testing.T)
func TestLoadPlatformGenerationReceiptRejectsMissingDeletedWrongRoleOrCrossConversationUserMessage(t *testing.T)
```

Each test must obtain the database only through:

```go
func openPlatformGenerationPersistenceMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	rawURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if rawURL == "" {
		t.Skip("BLOCKED_FIXTURE: requires TEST_DATABASE_URL")
	}
	if !strings.HasSuffix(strings.Trim(strings.Split(rawURL, "?")[0], "/"), "_test") {
		t.Fatal("TEST_DATABASE_URL must name an isolated *_test database")
	}
	gdb, err := db.Open(rawURL, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := migration.Up(context.Background(), gdb, persistence.NextGUID, persistence.NowMillis); err != nil {
		t.Fatal(err)
	}
	return gdb
}
```

Seed valid rows with model constructors and GORM rather than disabling foreign-key checks. Cross-user lookup must expect `ErrPlatformGenerationPersistenceNotFound`; malformed owned graphs must expect `ErrPlatformGenerationPersistenceIntegrity`. `TestLoadPlatformGenerationReceiptRejectsInvalidParentScalars` uses a fresh disposable child database per case and covers `mode=3`, `successful_model_count=0`, `daily_calls_charged != successful_model_count`, `total_tokens=-1`, `committed_at=0`, and `committed_at` above JavaScript's safe-integer maximum. For each otherwise CHECK-protected mutation, drop only its named receipt CHECK constraint, apply the one scalar mutation, prove `VerifyPlatformGenerationReceiptSchema` fails closed, and then prove the reader returns `ErrPlatformGenerationPersistenceIntegrity`; never disable foreign-key checks. For the otherwise-unrepresentable missing-user-message corruption subtest only, use a fresh disposable child database, drop the named `fk_platform_chat_generation_receipts_user_message` constraint, delete the referenced message, prove the schema verifier fails closed, then prove the reader returns the integrity error. The deleted, wrong-role, and cross-conversation subtests keep the FK installed and mutate only `is_deleted`, `role`, or `conversation_id` respectively.

- [ ] **Step 2: Run reader tests and confirm RED**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/service -run 'TestLoadPlatformGenerationReceipt' -count=1
```

Expected: FAIL to compile because `LoadPlatformGenerationReceipt` is undefined.

- [ ] **Step 3: Implement the reader with exact ownership predicates**

Create `platform_generation_receipt.go` around this complete flow:

```go
package service

import (
	"context"
	"errors"
	"math"
	"strconv"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func LoadPlatformGenerationReceipt(ctx context.Context, db *gorm.DB, userID int64, generationID string) (PlatformGenerationReceiptSnapshot, error) {
	if db == nil || validatePlatformGenerationIdentity(userID, generationID) != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceInvalid
	}
	var receipt models.PlatformChatGenerationReceipt
	err := db.WithContext(ctx).Where("user_id=? AND generation_id=? AND is_deleted=0", userID, generationID).First(&receipt).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceNotFound
	}
	if err != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable
	}
	mode := PlatformGenerationMode(receipt.Mode)
	switch mode {
	case PlatformGenerationModeSingle, PlatformGenerationModeCompare:
	default:
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}
	if receipt.SuccessfulModelCount < 1 || receipt.DailyCallsCharged != receipt.SuccessfulModelCount || receipt.TotalTokens < 0 || receipt.CommittedAt <= 0 || !platformSSEV2SafeInteger(receipt.CommittedAt) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}
	var conversation models.Conversation
	if err := db.WithContext(ctx).Where("id=? AND user_id=? AND is_deleted=0", receipt.ConversationID, userID).First(&conversation).Error; err != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}
	var userMessage models.Message
	if err := db.WithContext(ctx).Where("id=? AND conversation_id=? AND role=? AND is_deleted=0", receipt.UserMessageID, conversation.ID, models.MessageRoleUser).First(&userMessage).Error; err != nil || userMessage.Content == "" || userMessage.Model != nil || userMessage.Tokens != 0 {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}
	var rows []models.PlatformChatGenerationResult
	if err := db.WithContext(ctx).Where("receipt_id=? AND is_deleted=0", receipt.ID).Order("model_index ASC").Find(&rows).Error; err != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable
	}
	if (mode == PlatformGenerationModeSingle && len(rows) != 1) || (mode == PlatformGenerationModeCompare && (len(rows) < 2 || len(rows) > 3)) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}
	out := PlatformGenerationReceiptSnapshot{
		UserID: receipt.UserID, GenerationID: receipt.GenerationID, Mode: mode,
		ConversationGUID: conversation.Guid, UserMessage: userMessage.Content,
		SuccessfulModelCount: receipt.SuccessfulModelCount,
		DailyCallsCharged: receipt.DailyCallsCharged, TotalTokens: receipt.TotalTokens,
		CommittedAtMillis: receipt.CommittedAt, Results: make([]PlatformGenerationCommittedResult, 0, len(rows)),
	}
	successes := 0
	var total int64
	seenModels := make(map[string]struct{}, len(rows))
	seenMessages := make(map[int64]struct{}, len(rows))
	for index, row := range rows {
		if row.ModelIndex != index || !platformSSEV2ModelIdentifier(row.Model) {
			return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
		}
		if _, duplicate := seenModels[row.Model]; duplicate {
			return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
		}
		seenModels[row.Model] = struct{}{}
		item := PlatformGenerationCommittedResult{Model: row.Model, Tokens: row.Tokens}
		switch row.Status {
		case models.PlatformGenerationResultCompleted:
			if row.AssistantMessageID == nil || row.ErrorCode != nil || row.Tokens < 0 || row.Tokens > math.MaxInt32 {
				return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
			}
			if _, duplicate := seenMessages[*row.AssistantMessageID]; duplicate {
				return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
			}
			seenMessages[*row.AssistantMessageID] = struct{}{}
			var message models.Message
			if err := db.WithContext(ctx).Where("id=? AND conversation_id=? AND role=? AND is_deleted=0", *row.AssistantMessageID, conversation.ID, models.MessageRoleAssistant).First(&message).Error; err != nil || message.Model == nil || *message.Model != row.Model || int64(message.Tokens) != row.Tokens {
				return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
			}
			item.State = PlatformGenerationStateCompleted
			item.AssistantMessageGUID = strconv.FormatInt(message.Guid, 10)
			item.Content = message.Content
			successes++
			total += row.Tokens
		case models.PlatformGenerationResultFailed:
			if row.AssistantMessageID != nil || row.Tokens != 0 || row.ErrorCode == nil || !platformGenerationStableCode(*row.ErrorCode) {
				return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
			}
			item.State = PlatformGenerationStateFailed
			item.ErrorCode = *row.ErrorCode
		default:
			return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
		}
		out.Results = append(out.Results, item)
	}
	if successes != receipt.SuccessfulModelCount || receipt.DailyCallsCharged != successes || total != receipt.TotalTokens || successes == 0 {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}
	return out, nil
}
```

- [ ] **Step 4: Run the real reader tests and confirm GREEN**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/service -run 'TestLoadPlatformGenerationReceipt' -count=1
```

Expected with `TEST_DATABASE_URL`: PASS. Without it: explicit `BLOCKED_FIXTURE` skips and no claim of MySQL passage.

- [ ] **Step 5: Commit the reader**

```bash
git add internal/service/platform_generation_receipt.go internal/service/platform_generation_persistence_test.go
git commit -m "feat(platform): read generation receipts safely"
```

### Task 5: Implement atomic single finalization

**Files:**

- Modify: `internal/service/platform_generation_persistence.go`
- Modify: `internal/service/platform_generation_persistence_test.go`
- Modify: `docs/superpowers/specs/2026-09-08-platform-generation-persistence-design.md`
- Modify: `docs/superpowers/plans/2026-09-08-platform-generation-persistence.md`

- [ ] **Step 1: Write RED single/fault tests**

Add exact tests:

```go
func TestPlatformGenerationPersistenceFinalizesSingleAtomically(t *testing.T)
func TestPlatformGenerationPersistenceRejectsRedisIdentityMismatchBeforeMySQL(t *testing.T)
func TestPlatformGenerationPersistenceRejectsEmptyUserMessageBeforeDependencies(t *testing.T)
func TestPlatformGenerationPersistenceSingleWriteFailuresRollbackEveryEffect(t *testing.T)
func TestPlatformGenerationPersistenceSingleInsufficientQuotaRollsBack(t *testing.T)
```

The success test must claim a single generation, mark the model done, call `BeginCommit`, call `Finalize`, and assert one conversation, one non-empty user message, one assistant message, one usage record, one quota increment, exact total token increment, one receipt/result, `receipt.user_message_id` equal to that user-message row, and identical loaded user/assistant content plus assistant-message GUID. Every persistence result includes the final Redis `Seq`, which must match exactly but is not stored in the receipt schema. The empty-user-message and compare-mode tests use a store whose Redis address is deliberately unreachable and a transaction runner that panics if called; Task 5 `Finalize` must return `ErrPlatformGenerationPersistenceInvalid` without either dependency being touched. The fault table must inject failure at conversation create, user message, assistant message, usage, existing-conversation title/audit update, user update, receipt, and result boundaries and assert zero net rows/counter changes after each subtest. Include a receipt-insert failure caused by a bad `user_message_id` and prove it rolls back the entire transaction. Add stale Redis/user/reset/conversation timestamp cases, commit-unknown integrity/bounded-context coverage, an interpolating slow/error GORM logger capture that proves unique prompt/answer sentinels never appear, and default changed-row MySQL regressions proving whitespace-only content is byte-exact while new and already-current conversations retain the fallback title without `clientFoundRows`.

- [ ] **Step 2: Run single tests and confirm RED**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/service -run 'TestPlatformGenerationPersistence.*Single|TestPlatformGenerationPersistenceRejects(RedisIdentityMismatch|EmptyUserMessage)' -count=1
```

Expected: FAIL because `Finalize` does not exist.

- [ ] **Step 3: Implement the atomic transaction**

Add the methods below to `platform_generation_persistence.go`. Keep all calls on `tx`; do not call legacy `CheckAndConsumeCall`, `CreateConversation`, or `AddMessage` because their clocks/writes are not one BE03 boundary.

```go
func (p *PlatformGenerationPersistence) Finalize(ctx context.Context, db *gorm.DB, input PlatformGenerationPersistenceInput) (PlatformGenerationReceiptSnapshot, error) {
	if p == nil || p.generations == nil || p.runLocked == nil || p.loadReceipt == nil || ctx == nil || db == nil || input.Mode != PlatformGenerationModeSingle || validatePlatformGenerationPersistenceInput(input) != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceInvalid
	}
	redisSnapshot, err := p.generations.Get(ctx, input.UserID, input.GenerationID)
	if err != nil {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable
	}
	if !platformPersistenceMatchesRedis(input, redisSnapshot) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceConflict
	}
	if existing, err := p.loadReceipt(ctx, db, input.UserID, input.GenerationID); err == nil {
		if platformReceiptMatchesInput(existing, input) {
			return existing, nil
		}
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceConflict
	} else if !errors.Is(err, ErrPlatformGenerationPersistenceNotFound) {
		return PlatformGenerationReceiptSnapshot{}, err
	}
	err = p.runLocked(ctx, db, platformGenerationAdvisoryLockName(input.UserID, input.GenerationID), func(conn *gorm.DB) error {
		lockedSnapshot, getErr := p.generations.Get(ctx, input.UserID, input.GenerationID)
		if getErr != nil {
			return ErrPlatformGenerationPersistenceUnavailable
		}
		if !platformPersistenceMatchesRedis(input, lockedSnapshot) {
			return ErrPlatformGenerationPersistenceConflict
		}
		return conn.Transaction(func(tx *gorm.DB) error {
			return persistPlatformGeneration(tx.Session(&gorm.Session{Logger: logger.Discard}), input)
		})
	})
	if err == nil {
		return p.loadReceipt(ctx, db, input.UserID, input.GenerationID)
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), platformGenerationReceiptRecoveryTimeout)
	defer cancel()
	resolved, readErr := p.loadReceipt(recoveryCtx, db, input.UserID, input.GenerationID)
	if readErr == nil {
		if platformReceiptMatchesInput(resolved, input) {
			return resolved, nil
		}
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceConflict
	}
	if errors.Is(readErr, ErrPlatformGenerationPersistenceIntegrity) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceIntegrity
	}
	if !errors.Is(readErr, ErrPlatformGenerationPersistenceNotFound) {
		return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable
	}
	if errors.Is(err, ErrPlatformGenerationPersistenceQuota) || errors.Is(err, ErrPlatformGenerationPersistenceConflict) || errors.Is(err, ErrPlatformGenerationPersistenceInvalid) {
		return PlatformGenerationReceiptSnapshot{}, err
	}
	return PlatformGenerationReceiptSnapshot{}, ErrPlatformGenerationPersistenceUnavailable
}

func platformPersistenceMatchesRedis(input PlatformGenerationPersistenceInput, snapshot PlatformGenerationSnapshot) bool {
	if snapshot.State != PlatformGenerationStateCommitting || snapshot.GenerationID != input.GenerationID || snapshot.Mode != input.Mode || input.NowMillis < snapshot.UpdatedAtMillis || len(snapshot.Models) != len(input.Models) {
		return false
	}
	for i, model := range input.Models {
		state, ok := snapshot.ModelStates[model]
		if !ok || snapshot.Models[i] != model || state.State != input.Results[i].State || state.Seq != input.Results[i].Seq || state.AssistantMessageGUID != "" || state.ErrorCode != input.Results[i].ErrorCode {
			return false
		}
	}
	return true
}

func persistPlatformGeneration(tx *gorm.DB, input PlatformGenerationPersistenceInput) error {
	var user models.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND status=? AND is_deleted=0", input.UserID, models.UserStatusActive).First(&user).Error; err != nil {
		return ErrPlatformGenerationPersistenceConflict
	}
	if input.NowMillis < user.UpdatedAt || (user.DailyCallsResetAt != nil && input.NowMillis < *user.DailyCallsResetAt) {
		return ErrPlatformGenerationPersistenceConflict
	}
	successes := 0
	var totalTokens int64
	for _, result := range input.Results {
		if result.State == PlatformGenerationStateCompleted {
			successes++
			totalTokens += result.Tokens
		}
	}
	resetDailyAt(&user, input.NowMillis)
	if user.PlanType == models.PlanFree && user.DailyCallsUsed+successes > user.DailyCallLimit {
		return ErrPlatformGenerationPersistenceQuota
	}
	var conversation models.Conversation
	conversationCreated := false
	if input.ConversationGUID != nil {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guid=? AND user_id=? AND is_deleted=0", *input.ConversationGUID, input.UserID).First(&conversation).Error; err != nil {
			return ErrPlatformGenerationPersistenceConflict
		}
		if input.NowMillis < conversation.UpdatedAt {
			return ErrPlatformGenerationPersistenceConflict
		}
	} else {
		model := input.Models[0]
		conversationCreated = true
		conversation = models.Conversation{UserID: input.UserID, Title: truncateTitle(input.UserMessage), Model: &model, AuditFields: platformPersistenceAudit(input.UserID, input.NowMillis)}
		if err := tx.Create(&conversation).Error; err != nil {
			return err
		}
	}
	userMessage := models.Message{ConversationID: conversation.ID, Role: models.MessageRoleUser, Content: input.UserMessage, AuditFields: platformPersistenceAudit(input.UserID, input.NowMillis)}
	if err := tx.Create(&userMessage).Error; err != nil {
		return err
	}
	messageIDs := make(map[string]int64, successes)
	for _, result := range input.Results {
		if result.State != PlatformGenerationStateCompleted {
			continue
		}
		model := result.Model
		message := models.Message{ConversationID: conversation.ID, Role: models.MessageRoleAssistant, Content: result.Content, Model: &model, Tokens: int(result.Tokens), AuditFields: platformPersistenceAudit(input.UserID, input.NowMillis)}
		if err := tx.Create(&message).Error; err != nil {
			return err
		}
		messageIDs[result.Model] = message.ID
		usage := models.UsageRecord{UserID: input.UserID, RecordType: models.UsageRecordChat, Tokens: int(result.Tokens), Model: &model, AuditFields: platformPersistenceAudit(input.UserID, input.NowMillis)}
		if err := tx.Create(&usage).Error; err != nil {
			return err
		}
	}
	if !conversationCreated {
		title := conversation.Title
		if title == "新对话" {
			title = truncateTitle(input.UserMessage)
		}
		updatedByMatches := conversation.UpdatedBy != nil && *conversation.UpdatedBy == input.UserID
		if title != conversation.Title || conversation.UpdatedAt != input.NowMillis || !updatedByMatches {
			conversationUpdate := tx.Model(&models.Conversation{}).Where("id=? AND user_id=? AND is_deleted=0", conversation.ID, input.UserID).Updates(map[string]any{
				"title": title, "updated_at": input.NowMillis, "updated_by": input.UserID,
			})
			if conversationUpdate.Error != nil {
				return conversationUpdate.Error
			}
			if conversationUpdate.RowsAffected != 1 {
				return ErrPlatformGenerationPersistenceConflict
			}
		}
	}
	user.DailyCallsUsed += successes
	user.TotalTokensUsed += totalTokens
	userUpdate := tx.Model(&models.User{}).Where("id=? AND status=? AND is_deleted=0", user.ID, models.UserStatusActive).Updates(map[string]any{
		"daily_calls_used": user.DailyCallsUsed, "total_tokens_used": user.TotalTokensUsed,
		"daily_calls_reset_at": user.DailyCallsResetAt, "updated_at": input.NowMillis, "updated_by": input.UserID,
	})
	if userUpdate.Error != nil {
		return userUpdate.Error
	}
	if userUpdate.RowsAffected != 1 {
		return ErrPlatformGenerationPersistenceConflict
	}
	receipt := models.PlatformChatGenerationReceipt{
		AuditFields: platformPersistenceAudit(input.UserID, input.NowMillis), UserID: input.UserID,
		GenerationID: input.GenerationID, Mode: models.PlatformGenerationReceiptMode(input.Mode), ConversationID: conversation.ID,
		UserMessageID: userMessage.ID,
		SuccessfulModelCount: successes, DailyCallsCharged: successes, TotalTokens: totalTokens, CommittedAt: input.NowMillis,
	}
	if err := tx.Create(&receipt).Error; err != nil {
		return err
	}
	for index, result := range input.Results {
		row := models.PlatformChatGenerationResult{AuditFields: platformPersistenceAudit(input.UserID, input.NowMillis), ReceiptID: receipt.ID, ModelIndex: index, Model: result.Model, Tokens: result.Tokens}
		if result.State == PlatformGenerationStateCompleted {
			row.Status = models.PlatformGenerationResultCompleted
			messageID := messageIDs[result.Model]
			row.AssistantMessageID = &messageID
		} else {
			row.Status = models.PlatformGenerationResultFailed
			row.Tokens = 0
			code := result.ErrorCode
			row.ErrorCode = &code
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}

func platformPersistenceAudit(userID, nowMillis int64) models.AuditFields {
	return models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: nowMillis, CreatedBy: &userID, UpdatedAt: nowMillis, UpdatedBy: &userID, IsDeleted: 0}
}

func resetDailyAt(user *models.User, nowMillis int64) {
	now := time.UnixMilli(nowMillis).UTC()
	if user.DailyCallsResetAt == nil || time.UnixMilli(*user.DailyCallsResetAt).UTC().Format("2006-01-02") != now.Format("2006-01-02") {
		user.DailyCallsUsed = 0
		user.DailyCallsResetAt = &nowMillis
	}
}
```

Add the required imports `time`, `gorm.io/gorm/clause`, `gorm.io/gorm/logger`, `internal/models`, and `internal/persistence`. The finalizer and receipt-less recovery path must hold the same hashed per-generation MySQL advisory lock while resolving commit state. Run the entire transaction through a silent GORM session so error and slow-query logging cannot interpolate prompt or assistant content; preserve the original returned database error without logging content in service code. Reject a transaction timestamp older than Redis `updated_at_ms`, the locked user's `updated_at` or non-null `daily_calls_reset_at`, or a locked existing conversation's `updated_at`; equal timestamps are allowed. Replace full-model `Save` calls with explicit field-only updates and active ownership predicates. Compute a new conversation's final title before `CREATE` and skip its redundant update; skip a known no-op for an already locked active owned conversation, while attempted updates require exactly one affected row. This must work under default changed-row semantics without `clientFoundRows`. Implement `platformReceiptMatchesInput` by exact user-message bytes plus ordered mode/model/state/assistant-content/token/error comparison against the loaded receipt. `Seq` is deliberately excluded because it is a Redis precondition and has no durable receipt column. The matcher must not normalize or ignore conflicting durable fields, and it must not compare `CommittedAtMillis` with retry-local `NowMillis`. Commit-unknown receipt reads use the injected production reader under a short `WithoutCancel`-derived timeout, preserve a matching receipt as success, return mismatch as conflict and integrity as integrity, preserve typed quota/conflict/invalid only on not-found, and otherwise return unavailable.

- [ ] **Step 4: Run single/fault tests and confirm GREEN**

```bash
gofmt -w internal/service/platform_generation_persistence.go internal/service/platform_generation_persistence_test.go
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/service -run 'TestPlatformGenerationPersistence.*Single|TestPlatformGenerationPersistenceRejects(RedisIdentityMismatch|EmptyUserMessage)' -count=1
```

Expected with explicit MySQL/Redis fixtures: PASS; every injected failure leaves all counted effects unchanged.

- [ ] **Step 5: Commit atomic single finalization**

```bash
git add internal/service/platform_generation_persistence.go internal/service/platform_generation_persistence_test.go
git commit -m "feat(platform): finalize single generations atomically"
```

### Task 6: Add compare, idempotency, quota race, and commit-unknown coverage

**Files:**

- Modify: `internal/service/platform_generation_persistence.go`
- Modify: `internal/service/platform_generation_persistence_test.go`

- [ ] **Step 1: Write the complete RED behavior matrix**

Add exact tests:

```go
func TestPlatformGenerationPersistenceFinalizesCompareWithIndependentMessages(t *testing.T)
func TestPlatformGenerationPersistenceFinalizesPartialCompareWithoutChargingFailure(t *testing.T)
func TestPlatformGenerationPersistenceAllModelFailureWritesNothing(t *testing.T)
func TestPlatformGenerationPersistenceConcurrentDuplicateHasOneWinner(t *testing.T)
func TestPlatformGenerationPersistenceConflictingDuplicateIsRejected(t *testing.T)
func TestPlatformGenerationPersistenceDuplicateRejectsDifferentUserMessage(t *testing.T)
func TestPlatformGenerationPersistenceDuplicateIgnoresRetryTimestamp(t *testing.T)
func TestPlatformGenerationPersistenceQuotaRaceCannotOverrunLimit(t *testing.T)
func TestPlatformGenerationPersistenceCommitUnknownResolvesFromReceipt(t *testing.T)
```

The compare success assertions must prove two or three distinct assistant message GUIDs, no content prefixed by `__MULTI_MODEL__`, one user message, one usage row per success, original model order in the receipt, and exact aggregate counters. Partial compare must prove failed models have no message/usage/quota/tokens and retain only an allowlisted stable code.

The duplicate test must launch eight finalizers for one owner/generation and assert one receipt, one globally referenced user message, one assistant message per successful model, one quota charge set, and eight equivalent returned snapshots. The different-user-message test reuses every field except `UserMessage` and must return `ErrPlatformGenerationPersistenceConflict` without writes. The retry-timestamp test changes only `NowMillis`; it must return the existing receipt because commit time is an outcome, not immutable request identity. The quota race must use distinct generation IDs for a free user with one remaining call and assert exactly one commit. A deterministic transaction-order test must instrument `BeginTx` and the persistence query callback, proving that the locked Redis recheck completes before `BeginTx`, that persistence queries execute inside the transaction, and that both use the advisory lock's pinned physical connection. The commit-unknown test replaces `runLocked` with:

```go
persistence.runLocked = func(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
	if err := withPlatformGenerationAdvisoryLock(ctx, db, lockName, fn); err != nil {
		return err
	}
	return errors.New("simulated lost commit acknowledgement")
}
```

- [ ] **Step 2: Run the behavior matrix and confirm RED**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test -race ./internal/service -run 'TestPlatformGenerationPersistence(FinalizesCompare|FinalizesPartial|AllModel|Concurrent|Conflicting|Duplicate|QuotaRace|CommitUnknown)' -count=1 -timeout=120s
```

Expected: at least compare/idempotency behavior fails until exact duplicate matching and MySQL duplicate-key classification are complete.

- [ ] **Step 3: Complete duplicate and conflict classification**

Add these pure helpers and use them before/after the transaction:

```go
func platformReceiptMatchesInput(receipt PlatformGenerationReceiptSnapshot, input PlatformGenerationPersistenceInput) bool {
	if receipt.UserID != input.UserID || receipt.GenerationID != input.GenerationID || receipt.Mode != input.Mode || receipt.UserMessage != input.UserMessage || len(receipt.Results) != len(input.Results) {
		return false
	}
	if input.ConversationGUID != nil && receipt.ConversationGUID != *input.ConversationGUID {
		return false
	}
	for index, result := range input.Results {
		stored := receipt.Results[index]
		if stored.Model != result.Model || stored.State != result.State || stored.Content != result.Content || stored.Tokens != result.Tokens || stored.ErrorCode != result.ErrorCode {
			return false
		}
	}
	return true
}

func isPlatformGenerationDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}
```

Import `github.com/go-sql-driver/mysql`. A duplicate-key error from the receipt/result/user-message uniqueness boundary must cause a fresh `LoadPlatformGenerationReceipt`; it must never be mapped directly to success without full input comparison. Keep all model iteration ordered by `input.Models`, not by a Go map. Deliberately do not compare `receipt.CommittedAtMillis` to `input.NowMillis`: retries may use a later attempt timestamp, while `UserMessage` and all other immutable input fields must still match exactly.

- [ ] **Step 4: Run race tests and confirm GREEN**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test -race ./internal/service -run 'TestPlatformGenerationPersistence(FinalizesCompare|FinalizesPartial|AllModel|Concurrent|Conflicting|Duplicate|QuotaRace|CommitUnknown)' -count=1 -timeout=120s
```

Expected: PASS with explicit fixtures. `AllModelFailureWritesNothing` rejects validation before MySQL and confirms no receipt/counters/messages.

- [ ] **Step 5: Commit compare and idempotency**

```bash
git add internal/service/platform_generation_persistence.go internal/service/platform_generation_persistence_test.go
git commit -m "feat(platform): finalize compare generations safely"
```

### Task 7: Add receipt-aware BE02 reconciliation

**Files:**

- Modify: `internal/service/platform_generation_store.go`
- Modify: `internal/service/platform_generation_store_test.go`
- Create: `internal/service/platform_generation_reconcile.go`
- Create: `internal/service/platform_generation_reconcile_test.go`

- [ ] **Step 1: Write RED Redis CAS tests**

Add exact tests:

```go
func TestPlatformGenerationStoreReconcileCompleteIsIdempotent(t *testing.T)
func TestPlatformGenerationStoreReconcileCompleteRejectsDifferentGUIDMap(t *testing.T)
func TestPlatformGenerationStoreFailStaleCommitRequiresThirtySeconds(t *testing.T)
func TestPlatformGenerationStoreFailStaleCommitLosesToCompletedReceipt(t *testing.T)
```

Use the existing `openTestPlatformGenerationStore`, `claimTestGeneration`, and explicit Redis cleanup. Assert every new transition preserves the original TTL and never writes a content/token/quota/cost field.

- [ ] **Step 2: Run store tests and confirm RED**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache TEST_REDIS_URL="$TEST_REDIS_URL" go test -race ./internal/service -run 'TestPlatformGenerationStore(ReconcileComplete|FailStaleCommit)' -count=1
```

Expected: FAIL to compile because `ReconcileComplete` and `FailStaleCommit` are undefined.

- [ ] **Step 3: Implement narrow idempotent CAS methods**

Add:

```go
const platformGenerationConvergenceWindow = 30 * time.Second

func (s *PlatformGenerationStore) ReconcileComplete(ctx context.Context, userID int64, generationID string, assistantMessageGUIDs map[string]string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	snapshot, err := s.Get(ctx, userID, generationID)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if snapshot.State == PlatformGenerationStateCompleted {
		if platformGenerationGUIDMapMatches(snapshot, assistantMessageGUIDs) {
			return snapshot, nil
		}
		return snapshot, ErrPlatformGenerationConflict
	}
	completed, completeErr := s.Complete(ctx, userID, generationID, assistantMessageGUIDs, nowMillis)
	if errors.Is(completeErr, ErrPlatformGenerationConflict) && completed.State == PlatformGenerationStateCompleted && platformGenerationGUIDMapMatches(completed, assistantMessageGUIDs) {
		return completed, nil
	}
	return completed, completeErr
}

func (s *PlatformGenerationStore) FailStaleCommit(ctx context.Context, userID int64, generationID, code string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if !platformGenerationStableCode(code) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationInvalid
	}
	return s.mutate(ctx, userID, generationID, nowMillis, func(snapshot *PlatformGenerationSnapshot) error {
		if snapshot.State != PlatformGenerationStateCommitting || nowMillis-snapshot.UpdatedAtMillis < platformGenerationConvergenceWindow.Milliseconds() {
			return ErrPlatformGenerationConflict
		}
		snapshot.State = PlatformGenerationStateFailed
		snapshot.ErrorCode = code
		return nil
	})
}

func platformGenerationGUIDMapMatches(snapshot PlatformGenerationSnapshot, expected map[string]string) bool {
	count := 0
	for _, model := range snapshot.Models {
		state := snapshot.ModelStates[model]
		if state.State != PlatformGenerationStateCompleted {
			continue
		}
		count++
		if expected[model] != state.AssistantMessageGUID {
			return false
		}
	}
	return count == len(expected)
}
```

Confirm the existing strict decoder accepts this narrowly valid stale-commit failure shape only while assistant GUIDs remain empty and the global error code is stable; change it only if the RED test demonstrates a mismatch.

- [ ] **Step 4: Write RED cross-store recovery tests**

Add exact tests:

```go
func TestReconcilePlatformGenerationCompletesFromReceipt(t *testing.T)
func TestReconcilePlatformGenerationRejectsInvalidUserMessageReceipt(t *testing.T)
func TestReconcilePlatformGenerationFailsReceiptlessStaleCommit(t *testing.T)
func TestReconcilePlatformGenerationDoesNotFailFreshCommit(t *testing.T)
func TestReconcilePlatformGenerationReceiptWinsCASRace(t *testing.T)
func TestReconcilePlatformGenerationRedisUnavailableDoesNotReplayMySQL(t *testing.T)
func TestReconcilePlatformGenerationReturnsCompletedAuthoritativeConflict(t *testing.T)
func TestReconcilePlatformGenerationReturnsAuthoritativeStaleFailCASLoser(t *testing.T)
```

The invalid-user-message test marks the receipt's referenced user message deleted after commit, calls reconciliation, expects `ErrPlatformGenerationPersistenceIntegrity`, and proves Redis remains `committing`. No reconciliation path may copy `receipt.UserMessage` into Redis; it may derive only the successful model-to-assistant-GUID map from the validated receipt.

- [ ] **Step 5: Implement cross-store reconciliation**

Create:

```go
package service

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

func ReconcilePlatformGeneration(ctx context.Context, db *gorm.DB, store *PlatformGenerationStore, userID int64, generationID string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	if db == nil || store == nil || validatePlatformGenerationIdentity(userID, generationID) != nil || !platformSSEV2SafeInteger(nowMillis) {
		return PlatformGenerationSnapshot{}, ErrPlatformGenerationPersistenceInvalid
	}
	current, err := store.Get(ctx, userID, generationID)
	if err != nil {
		return PlatformGenerationSnapshot{}, err
	}
	if current.State != PlatformGenerationStateCommitting {
		return current, nil
	}
	var resolved PlatformGenerationSnapshot
	resolvedAuthoritativeOnError := false
	err = withPlatformGenerationAdvisoryLock(ctx, db, platformGenerationAdvisoryLockName(userID, generationID), func(conn *gorm.DB) error {
		receipt, receiptErr := LoadPlatformGenerationReceipt(ctx, conn, userID, generationID)
		if receiptErr == nil {
			guids := make(map[string]string, receipt.SuccessfulModelCount)
			for _, result := range receipt.Results {
				if result.State == PlatformGenerationStateCompleted {
					guids[result.Model] = result.AssistantMessageGUID
				}
			}
			resolved, receiptErr = store.ReconcileComplete(ctx, userID, generationID, guids, nowMillis)
			resolvedAuthoritativeOnError = receiptErr != nil && resolved.GenerationID != ""
			return receiptErr
		}
		if !errors.Is(receiptErr, ErrPlatformGenerationPersistenceNotFound) {
			return receiptErr
		}
		resolved, receiptErr = store.FailStaleCommit(ctx, userID, generationID, "internal_error", nowMillis)
		resolvedAuthoritativeOnError = receiptErr != nil && resolved.GenerationID != ""
		return receiptErr
	})
	if err != nil {
		if resolvedAuthoritativeOnError {
			return resolved, err
		}
		return current, err
	}
	return resolved, nil
}
```

- [ ] **Step 6: Run focused Redis/MySQL race tests and confirm GREEN**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test -race ./internal/service -run 'TestPlatformGenerationStore(ReconcileComplete|FailStaleCommit)|TestReconcilePlatformGeneration' -count=1 -timeout=120s
```

Expected with both explicit fixtures: PASS. A missing fixture is an explicit `BLOCKED_FIXTURE` skip, not a pass claim.

- [ ] **Step 7: Commit reconciliation**

```bash
git add internal/service/platform_generation_store.go internal/service/platform_generation_store_test.go internal/service/platform_generation_reconcile.go internal/service/platform_generation_reconcile_test.go
git commit -m "feat(platform): reconcile committed generations"
```

### Task 8: Wire the BE03 dependency without route activation

**Files:**

- Modify: `internal/app/state.go`
- Modify: `internal/app/state_test.go`
- Test: `internal/handler/platform_whitelabel_test.go`
- Test: `internal/router/router_test.go`

- [ ] **Step 1: Write RED state wiring tests**

Add exact tests:

```go
func TestNewStateWiresPlatformGenerationPersistenceWithRedis(t *testing.T)
func TestNewStateLeavesPlatformGenerationPersistenceNilWithoutRedis(t *testing.T)
func TestNewStateDoesNotRegisterGenerationRoutes(t *testing.T)
```

The first test injects the existing generation-store constructor, requires non-nil `PlatformGenerationPersistence`, and closes the store exactly once. The second requires both store and persistence fields nil. The route test must continue to receive the established v2 503 from completions/compare and 404 from generation GET/cancel.

- [ ] **Step 2: Run state/route tests and confirm RED**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/app ./internal/handler ./internal/router -run 'TestNewState.*PlatformGenerationPersistence|TestNewStateDoesNotRegisterGenerationRoutes|TestPlatform.*V2.*Unavailable' -count=1
```

Expected: FAIL because `State.PlatformGenerationPersistence` is undefined.

- [ ] **Step 3: Add the dependency field and constructor only**

Add to `app.State`:

```go
PlatformGenerationPersistence *service.PlatformGenerationPersistence
```

After `s.PlatformGenerations = generations`, add:

```go
generationPersistence, err := service.NewPlatformGenerationPersistence(generations)
if err != nil {
	return nil, err
}
s.PlatformGenerationPersistence = generationPersistence
```

Do not edit `RegisterPlatform` routes except for tests proving no activation.

- [ ] **Step 4: Run state and route tests and confirm GREEN**

```bash
gofmt -w internal/app/state.go internal/app/state_test.go
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go test ./internal/app ./internal/handler ./internal/router -run 'TestNewState.*PlatformGenerationPersistence|TestNewStateDoesNotRegisterGenerationRoutes|TestPlatform.*V2.*Unavailable' -count=1
```

Expected: PASS; v2 completions/compare remain stable 503 and generation GET/cancel remain unregistered 404.

- [ ] **Step 5: Commit dependency wiring**

```bash
git add internal/app/state.go internal/app/state_test.go internal/handler/platform_whitelabel_test.go internal/router/router_test.go
git commit -m "feat(app): wire generation persistence service"
```

### Task 9: Run isolated real-fixture and regression gates

**Files:**

- Modify only if a failing contract requires an in-scope fix: files listed in Tasks 1-8

- [ ] **Step 1: Start exact isolated fixtures**

Use loopback-only random host ports, tmpfs/no persistence, unique task labels, and a database name ending `_test`. Capture exact container IDs, image digests, labels, ports, and database name before testing. Do not read `.env`, `DATABASE_URL`, deployment `REDIS_URL`, or production credentials.

Example commands, with unique container names for the execution date:

```bash
docker run -d --rm --name porsche-chat-be03-mysql-20260908 --label codex.task=platform-generation-persistence --tmpfs /var/lib/mysql:rw,noexec,nosuid,size=1g -e MYSQL_ROOT_PASSWORD=be03-local-only -e MYSQL_DATABASE=porsche_generation_test -p 127.0.0.1::3306 mysql:8.4
docker run -d --rm --name porsche-chat-be03-redis-20260908 --label codex.task=platform-generation-persistence --tmpfs /data:rw,noexec,nosuid,size=128m -p 127.0.0.1::6379 redis:7-alpine redis-server --save '' --appendonly no
```

Resolve the assigned ports using `docker inspect`; export only `TEST_DATABASE_URL` and `TEST_REDIS_URL` in the test shell.

- [ ] **Step 2: Run focused migration/persistence/reconciliation race gates**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache TEST_DATABASE_URL="$TEST_DATABASE_URL" TEST_REDIS_URL="$TEST_REDIS_URL" go test -race ./internal/migration ./internal/models ./internal/service ./internal/app -run 'TestPlatformGeneration|TestLoadPlatformGenerationReceipt|TestReconcilePlatformGeneration|TestNewState.*PlatformGenerationPersistence' -count=1 -timeout=180s
```

Expected: PASS with no skips in the targeted fixture-backed tests.

- [ ] **Step 3: Run complete regression and static gates**

```bash
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache TEST_DATABASE_URL="$TEST_DATABASE_URL" TEST_REDIS_URL="$TEST_REDIS_URL" go test ./... -count=1 -timeout=240s
GOCACHE=/private/tmp/porsche-chat-streaming-go-cache go vet ./...
git diff --check
python3 -m json.tool feature_list.json >/dev/null
rg -n 'stream_version.*platform-chat-sse.v2|platformSSEV2Unavailable|generations/.*/cancel' internal/handler internal/router
```

Expected: all Go packages PASS, vet/diff/JSON exit zero, and the source check confirms v2 stream still enters the unavailable gate with no GET/cancel route registration.

- [ ] **Step 4: Clean up by exact identity**

Stop only the two captured fixture container IDs after verifying their task labels. Confirm their listeners, names, and task-labeled containers are absent; do not perform broad Docker cleanup and do not remove unrelated images, volumes, networks, or containers.

- [ ] **Step 5: Commit any in-scope verification fixes**

If no fix was required, make no empty commit. If a focused fix was required, stage only its exact files and commit:

```bash
git commit -m "fix(platform): close generation persistence verification gaps"
```

### Task 10: Record evidence, tracker state, and independent reviews

**Files:**

- Create: `docs/superpowers/reports/2026-09-08-platform-generation-persistence.md`
- Modify: `feature_list.json`
- Modify: `progress.md`

- [ ] **Step 1: Obtain three independent reviews**

Dispatch fresh specification, implementation-quality, and security reviewers against the exact final code diff and approved design. Require each to inspect migration conformance, transaction atomicity, idempotency, quota concurrency, commit-unknown recovery, receipt graph ownership, Redis CAS, sensitive-data exclusion, route inactivity, and fixture evidence. Address every finding with RED tests and focused commits, then rerun the full gates.

Expected final statuses: `SPEC_PASS`, `IMPLEMENTATION_PASS`, and `SECURITY_PASS`. Any unresolved finding keeps BE03 in progress and must be recorded honestly.

- [ ] **Step 2: Write the verification report after all gates pass**

Create the report with this exact structure and replace prose only with literal observed fixture IDs/ports and command outputs; do not include credentials, model content, prompts, or raw dependency errors:

```markdown
# Platform generation persistence verification

Date: 2026-09-08

Scope: BE03 migration 0011, atomic generation persistence, receipt integrity, and receipt-aware reconciliation only. No v2 route activation, frontend change, production migration, deployment, push, merge, or real upstream request.

## Isolated fixtures

The run used one loopback-only disposable MySQL 8.4 `*_test` database and one loopback-only disposable Redis 7 instance, both identified and cleaned up by exact container ID and task label. Neither `.env` nor production database/Redis configuration was read.

## RED evidence

The migration contract first failed because 0011 was absent. Typed persistence tests first failed because the models and finalizer did not exist. Receipt-reader, atomic finalization, quota/idempotency, and reconciliation tests were each observed failing before their minimal implementation.

## GREEN evidence

The focused real-fixture race command passed without fixture skips. Coverage includes 0011 schema/constraints/rerun, globally unique user-message receipt references, owned receipt hydration, exact duplicate user-message comparison, retry-timestamp independence, single/compare atomicity, per-model messages, partial failure accounting, write-boundary rollback, duplicate and quota races, commit-unknown resolution, invalid user-message graph rejection during reconciliation, stale commit reconciliation, TTL preservation, and route inactivity.

The complete `go test ./...`, `go vet ./...`, `git diff --check`, and tracker JSON checks passed.

## Review

- Independent specification review: SPEC_PASS.
- Independent implementation-quality review: IMPLEMENTATION_PASS.
- Independent security review: SECURITY_PASS.

BE03 remains below the HTTP boundary. BE04 generation GET/cancel and restart scheduling, BE05 single v2 stream, BE06 compare v2 stream, frontend/proxy/real-model acceptance, production migration, deployment, and push remain separately scoped.
```

- [ ] **Step 3: Update only the go-018 tracker entry**

Append this evidence string to `go-018.evidence`:

```json
"2026-09-08：BE03 新增 0011 generation receipt/result schema、原子 single/compare 持久化、每模型独立消息、成功项 quota/token/usage 一致性、幂等与 commit-unknown 恢复锚点；隔离 MySQL/Redis race、全量 Go、vet、diff 及独立规格/实现/安全复审通过。报告 docs/superpowers/reports/2026-09-08-platform-generation-persistence.md。"
```

Replace `go-018.notes` with:

```json
"BE01-BE03 已完成；generation GET/cancel、取消先到 tombstone、重启调度、真实 v2 single/compare stream 和联合验收仍待后续 tranche。未激活 v2 路由；未 push、merge、deploy、执行生产迁移或调用真实付费上游。"
```

Keep `go-018.status` as `in_progress`; do not alter unrelated tracker entries.

- [ ] **Step 4: Update progress without completion inflation**

Under the existing platform-chat SSE v2 section, append:

```markdown
- BE03 generation persistence 已完成：0011 仅新增 receipt/result 双表；single/compare 成功交换在一个 MySQL 事务内写入会话、每模型独立消息、usage、quota/token 计数与 durable receipt，receipt-aware Redis reconciliation 覆盖 commit-unknown 与 30 秒 stale committing。隔离 MySQL/Redis race、完整 Go、vet、diff 与独立规格/实现/安全复审通过，证据见 `docs/superpowers/reports/2026-09-08-platform-generation-persistence.md`。v2 路由仍未激活，生产迁移、部署、push 与真实上游均未执行。
```

- [ ] **Step 5: Validate and commit BE03 evidence**

```bash
python3 -m json.tool feature_list.json >/dev/null
git diff --check
git status --short
git add docs/superpowers/reports/2026-09-08-platform-generation-persistence.md feature_list.json progress.md
git commit -m "docs(platform): record generation persistence evidence"
```

Expected: JSON/diff checks PASS; the commit contains only the report and the two tracker files.

## Final completion check

Before reporting BE03 complete, run:

```bash
git status --short
git log --oneline --decorate -12
```

Expected: a clean worktree after the planned commits and every type used by later tasks defined earlier with the same name. Report BE03 only, keep `go-018` in progress, and explicitly retain BE04-BE06, frontend/proxy acceptance, production migration, deployment, push, and real upstream as incomplete/unexecuted.
