package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestAdminOperationResponseMigrationIsLatestAndChecksumProtected(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 10 || migrations[8].Version != "0009" || migrations[9].Version != "0010" {
		t.Fatalf("All() = %#v, want exactly ten migrations ending at 0010", migrations)
	}
	up := strings.ToLower(string(migrations[8].UpSQL))
	for _, fragment := range []string{
		"lifecycle_state int not null default 1",
		"integrity_version int not null default 0",
		"response_hmac char(64)",
		"failure_code int null",
		"chk_admin_operation_responses_lifecycle",
		"chk_admin_action_outbox_outcome",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0009 missing %q", fragment)
		}
	}
	if !strings.Contains(up, "alter table admin_operation_responses") || !strings.Contains(up, "alter table admin_action_outbox") || strings.Contains(up, "create trigger") || strings.Contains(up, "timestamp") || strings.Contains(up, "datetime") {
		t.Fatalf("0009 must migrate response integrity and outbox outcome fields: %s", up)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(migrations[8].UpSQL)); got == strings.Repeat("0", 64) || len(got) != 64 {
		t.Fatalf("0009 checksum = %q", got)
	}
}

func TestAdminOperationResponseModelIsInternalAndImmutable(t *testing.T) {
	parsed, err := schema.Parse(&models.AdminOperationResponse{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Table != "admin_operation_responses" {
		t.Fatalf("table = %q", parsed.Table)
	}
	for _, name := range []string{"ID", "Guid", "OperationID", "TargetGUID", "LifecycleState", "IntegrityVersion", "ResponseHMAC", "HTTPStatus", "MediaType", "ResponseBody", "BodySHA256", "CreatedAt", "CreatedBy", "UpdatedAt", "UpdatedBy", "IsDeleted"} {
		field := parsed.LookUpField(name)
		if field == nil {
			t.Errorf("missing model field %s", name)
		}
	}
	if field := parsed.LookUpField("ResponseBody"); field == nil || field.DBName != "response_body" || string(field.DataType) != "varbinary(4096)" || !field.NotNull {
		t.Fatalf("response body metadata = %#v", field)
	}
}

func TestAdminOperationResponseCheckCanonicalizationAcceptsMySQLStringEscaping(t *testing.T) {
	want, wantOK := canonicalizeAdminOperationResponseCheck("http_status = 201 AND media_type = 'application/json'")
	got, gotOK := canonicalizeAdminOperationResponseCheck("((`HTTP_STATUS` = 0201) AnD (`MEDIA_TYPE` = _ASCII\\'application/json\\'))")
	if !wantOK || !gotOK || got != want {
		t.Fatalf("canonical MySQL clause = %q/%v, want %q/%v", got, gotOK, want, wantOK)
	}
}

func TestAdminOperationResponseCheckCanonicalizationPreservesActorBooleanGrouping(t *testing.T) {
	want, wantOK := canonicalizeAdminOperationResponseCheck("created_at >= 0 AND updated_at = created_at AND is_deleted = 0 AND ((created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by))")
	drifted, driftedOK := canonicalizeAdminOperationResponseCheck("(created_at >= 0 AND updated_at = created_at AND is_deleted = 0 AND created_by IS NULL AND updated_by IS NULL) OR (created_by IS NOT NULL AND updated_by = created_by)")
	if !wantOK || !driftedOK || drifted == want {
		t.Fatalf("actor OR moved outside immutable conjunction was accepted: %q", drifted)
	}
	notGroup, notGroupOK := canonicalizeAdminOperationResponseCheck("NOT (created_by IS NULL OR updated_by IS NULL)")
	notDrift, notDriftOK := canonicalizeAdminOperationResponseCheck("(NOT created_by IS NULL) OR updated_by IS NULL")
	if !notGroupOK || !notDriftOK || notGroup == notDrift {
		t.Fatalf("NOT grouping drift was accepted: %q", notDrift)
	}
}

func TestAdminOperationResponseCheckCanonicalizationAcceptsFunctionFormatting(t *testing.T) {
	want, wantOK := canonicalizeAdminOperationResponseCheck("OCTET_LENGTH(response_body) BETWEEN 2 AND 4096 AND OCTET_LENGTH(body_sha256) = 64")
	got, gotOK := canonicalizeAdminOperationResponseCheck("(( LENGTH ( `RESPONSE_BODY` ) BETWEEN (0002) AND 04096) aNd (length(`BODY_SHA256`) = 0064))")
	if !wantOK || !gotOK || got != want {
		t.Fatalf("canonical function clause = %q/%v, want %q/%v", got, gotOK, want, wantOK)
	}
}

func TestAdminOperationResponseCheckCanonicalizationAcceptsBinaryHexFormatting(t *testing.T) {
	want, wantOK := canonicalizeAdminOperationResponseCheck("response_body = X'7B7D'")
	got, gotOK := canonicalizeAdminOperationResponseCheck("(`response_body` = 0x7b7d)")
	if !wantOK || !gotOK || got != want {
		t.Fatalf("canonical binary literal = %q/%v, want %q/%v", got, gotOK, want, wantOK)
	}
}

func TestAdminOperationResponseContractRejectsLifecycleBooleanDrift(t *testing.T) {
	want := adminOperationResponseTableContract()
	got := matchingAdminOperationResponseMetadata(want, "fixture_test")
	if !matchesAdminOperationResponseContract(want, got, "fixture_test") {
		t.Fatal("equivalent MySQL formatting was rejected")
	}
	for index := range got.checks {
		if got.checks[index].name == "chk_admin_operation_responses_lifecycle" {
			got.checks[index].clause = "lifecycle_state = 1 OR is_deleted = 0"
		}
	}
	if matchesAdminOperationResponseContract(want, got, "fixture_test") {
		t.Fatal("verifier accepted a weakened response lifecycle check")
	}
}

func matchingAdminOperationResponseMetadata(contract businessGroupTableContract, schemaName string) businessGroupTableMetadata {
	metadata := businessGroupTableMetadata{engine: "InnoDB", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"}
	for _, column := range contract.columns {
		metadata.columns = append(metadata.columns, businessGroupColumnMetadata{
			name: column.name, columnType: column.columnType, nullable: column.nullable, defaultVal: column.defaultVal,
			extra: column.extra, characterSet: column.characterSet, collation: column.collation,
		})
	}
	for _, index := range contract.indexes {
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
	metadata.foreignKeys = []businessGroupForeignKeyMetadata{{
		name: "fk_admin_operation_responses_operation", column: "operation_id", ordinal: 1, targetSchema: schemaName,
		targetTable: "admin_operations", targetColumn: "id", deleteRule: "RESTRICT", updateRule: "RESTRICT",
	}, {
		name: "fk_admin_operation_responses_target", column: "target_guid", ordinal: 1, targetSchema: schemaName,
		targetTable: "users", targetColumn: "guid", deleteRule: "RESTRICT", updateRule: "RESTRICT",
	}}
	for _, check := range contract.checks {
		metadata.checks = append(metadata.checks, businessGroupCheckMetadata{name: check.name, clause: check.clause, enforced: "YES"})
	}
	return metadata
}

func TestAdminOperationResponseMigrationOnIsolatedMySQLIsRerunnable(t *testing.T) {
	gdb := permissionSchemaDB(t)
	next := sequentialGUID(29_000)
	apply := func() {
		t.Helper()
		if err := Up(context.Background(), gdb, next, func() int64 { return 1_900_000_000_000 }); err != nil {
			t.Fatal(err)
		}
		if err := VerifyAdminOperationResponseSchema(context.Background(), gdb); err != nil {
			t.Fatal(err)
		}
	}
	apply()
	// Simulate a process crash after MySQL atomically created the table but
	// before the runner recorded the version. The next run must verify the
	// existing table and recreate exactly one ledger entry.
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version = '0010'").Error; err != nil {
		t.Fatal(err)
	}
	apply()
	apply()
	var ledgerCount int64
	if err := gdb.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version = '0010' AND is_deleted = 0").Row().Scan(&ledgerCount); err != nil || ledgerCount != 1 {
		t.Fatalf("0010 ledger count = %d (%v)", ledgerCount, err)
	}
}

func TestAdminOperationResponseTargetMigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 10 || migrations[9].Version != "0010" {
		t.Fatalf("missing 0010 response target migration: %#v", migrations)
	}
	up := strings.ToLower(string(migrations[9].UpSQL))
	for _, fragment := range []string{"add column target_guid bigint null", "idx_admin_operation_responses_target", "fk_admin_operation_responses_target", "target_guid is not null", "response_body=x'7b7d'"} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0010 missing %q", fragment)
		}
	}
	if strings.Contains(up, "create trigger") || strings.Contains(up, "log_bin_trust_function_creators") {
		t.Fatal("0010 must not depend on triggers or server policy")
	}
}

func TestAdminOperationResponseTargetMigrationResumesEveryCommittedPrefix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix int
	}{
		{name: "target_column", prefix: 1},
		{name: "durable_backfill", prefix: 2},
		{name: "fail_safe_redaction", prefix: 3},
		{name: "constraint_and_indexes", prefix: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gdb := permissionSchemaDB(t)
			statements := prepareAdminResponseTargetPrefix(t, gdb)
			for index := 0; index < tc.prefix; index++ {
				if err := gdb.Exec(statements[index]).Error; err != nil {
					t.Fatalf("apply 0010 committed prefix statement %d: %v", index+1, err)
				}
			}
			calls := 0
			if err := Up(context.Background(), gdb, func() int64 { calls++; return 69_000 + int64(calls) }, func() int64 { return 1_900_000_000_004 }); err != nil {
				t.Fatalf("resume 0010 after %s: %v", tc.name, err)
			}
			if calls != 1 {
				t.Fatalf("resume 0010 after %s allocated %d ledger GUIDs, want 1", tc.name, calls)
			}
			if err := Verify(context.Background(), gdb); err != nil {
				t.Fatalf("verify resumed 0010 %s: %v", tc.name, err)
			}
			var target sql.NullInt64
			var lifecycle, deleted int
			var body []byte
			if err := gdb.Raw("SELECT target_guid,lifecycle_state,is_deleted,response_body FROM admin_operation_responses WHERE guid=68011").Row().Scan(&target, &lifecycle, &deleted, &body); err != nil || !target.Valid || target.Int64 != 68002 || lifecycle != 1 || deleted != 0 || !strings.Contains(string(body), "legacy-personal-data") {
				t.Fatalf("resolved snapshot after %s = target=%#v lifecycle=%d deleted=%d body=%q err=%v", tc.name, target, lifecycle, deleted, body, err)
			}
			clear(body)
			if err := gdb.Raw("SELECT target_guid,lifecycle_state,is_deleted,response_body FROM admin_operation_responses WHERE guid=68031").Row().Scan(&target, &lifecycle, &deleted, &body); err != nil || !target.Valid || target.Int64 != 68002 || lifecycle != 1 || deleted != 0 || !strings.Contains(string(body), "expired-outbox-private") {
				t.Fatalf("expired outbox snapshot after %s = target=%#v lifecycle=%d deleted=%d body=%q err=%v", tc.name, target, lifecycle, deleted, body, err)
			}
			clear(body)
			if err := gdb.Raw("SELECT target_guid,lifecycle_state,is_deleted,response_body FROM admin_operation_responses WHERE guid=68021").Row().Scan(&target, &lifecycle, &deleted, &body); err != nil || target.Valid || lifecycle != 2 || deleted != 1 || string(body) != "{}" {
				t.Fatalf("unresolved snapshot after %s = target=%#v lifecycle=%d deleted=%d body=%q err=%v", tc.name, target, lifecycle, deleted, body, err)
			}
			clear(body)
			if err := gdb.Raw("SELECT target_guid,lifecycle_state,is_deleted,response_body FROM admin_operation_responses WHERE guid=68041").Row().Scan(&target, &lifecycle, &deleted, &body); err != nil || target.Valid || lifecycle != 2 || deleted != 1 || string(body) != "{}" {
				t.Fatalf("legacy redacted snapshot after %s = target=%#v lifecycle=%d deleted=%d body=%q err=%v", tc.name, target, lifecycle, deleted, body, err)
			}
			clear(body)
			calls = 0
			if err := Up(context.Background(), gdb, func() int64 { calls++; return 79_000 }, func() int64 { return 1_900_000_000_005 }); err != nil || calls != 0 {
				t.Fatalf("rerun completed 0010 %s err=%v GUID calls=%d", tc.name, err, calls)
			}
		})
	}
}

func prepareAdminResponseTargetPrefix(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	permissionUp(t, gdb)
	migrations, err := All()
	if err != nil || len(migrations) != 10 || migrations[9].Version != "0010" {
		t.Fatalf("load 0010 = %d/%v", len(migrations), err)
	}
	migration := migrations[9]
	if err := executeAdminOperationSafetyFixtureSQL(gdb, migration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version='0010'").Error; err != nil {
		t.Fatal(err)
	}
	insertMigrationUserWithDefaultGroup(t, gdb, 68_001, "target-prefix-actor")
	insertMigrationUserWithDefaultGroup(t, gdb, 68_002, "target-prefix-user")
	var actorID int64
	if err := gdb.Raw("SELECT id FROM users WHERE guid=68001").Row().Scan(&actorID); err != nil || actorID <= 0 {
		t.Fatalf("read 0010 actor id=%d err=%v", actorID, err)
	}
	if err := gdb.Exec(`INSERT INTO user_sessions (guid,sid,user_id,login_method,session_version,refresh_hmac,last_active_at,expires_at,created_at,updated_at,is_deleted) VALUES (68003,'00000000-0000-4000-8000-000000068003',?,1,1,?,1,3,1,1,0)`, actorID, strings.Repeat("a", 64)).Error; err != nil {
		t.Fatal(err)
	}
	var sessionID int64
	if err := gdb.Raw("SELECT id FROM user_sessions WHERE guid=68003").Row().Scan(&sessionID); err != nil || sessionID <= 0 {
		t.Fatalf("read 0010 session id=%d err=%v", sessionID, err)
	}
	activeRef := "op_" + strings.Repeat("t", 43)
	if err := gdb.Exec(`INSERT INTO admin_operations (guid,actor_user_id,actor_auth_version,session_id,action,idempotency_key_hmac,request_hmac,state,public_ref,finished_at,query_expires_at,result_kind,result_guid,result_http_status,created_at,created_by,updated_at,updated_by,is_deleted) VALUES (68004,?,1,?,9,?,?,2,?,2,3,2,68002,201,1,?,2,?,0)`, actorID, sessionID, strings.Repeat("b", 64), strings.Repeat("c", 64), activeRef, actorID, actorID).Error; err != nil {
		t.Fatal(err)
	}
	var activeOperationID int64
	if err := gdb.Raw("SELECT id FROM admin_operations WHERE guid=68004").Row().Scan(&activeOperationID); err != nil || activeOperationID <= 0 {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO admin_action_outbox (guid,created_at,created_by,updated_at,updated_by,is_deleted,operation_id,public_ref,action,target_kind,state,failure_code,result_guid,delivery_state,available_at,attempt_count) VALUES (68005,2,?,2,?,0,?,?,9,1,2,NULL,68002,1,2,0)`, actorID, actorID, activeOperationID, activeRef).Error; err != nil {
		t.Fatal(err)
	}
	activeBody := []byte(`{"operation_ref":"` + activeRef + `","legacy":"legacy-personal-data"}`)
	activeDigest := fmt.Sprintf("%x", sha256.Sum256(activeBody))
	if err := gdb.Exec(`INSERT INTO admin_operation_responses (guid,created_at,created_by,updated_at,updated_by,is_deleted,operation_id,lifecycle_state,integrity_version,response_hmac,http_status,media_type,response_body,body_sha256) VALUES (68011,2,?,2,?,0,?,1,1,?,201,'application/json',?,?)`, actorID, actorID, activeOperationID, strings.Repeat("d", 64), activeBody, activeDigest).Error; err != nil {
		t.Fatal(err)
	}
	expiredRef := "op_" + strings.Repeat("u", 43)
	if err := gdb.Exec(`INSERT INTO admin_operations (guid,actor_user_id,actor_auth_version,session_id,action,idempotency_key_hmac,request_hmac,state,public_ref,finished_at,query_expires_at,created_at,created_by,updated_at,updated_by,is_deleted) VALUES (68014,?,1,?,9,?,?,5,?,2,3,1,?,2,?,1)`, actorID, sessionID, strings.Repeat("e", 64), strings.Repeat("f", 64), expiredRef, actorID, actorID).Error; err != nil {
		t.Fatal(err)
	}
	var expiredOperationID int64
	if err := gdb.Raw("SELECT id FROM admin_operations WHERE guid=68014").Row().Scan(&expiredOperationID); err != nil || expiredOperationID <= 0 {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO admin_action_outbox (guid,created_at,created_by,updated_at,updated_by,is_deleted,operation_id,public_ref,action,target_kind,state,failure_code,result_guid,delivery_state,available_at,attempt_count) VALUES (68015,2,?,2,?,0,?,?,9,1,2,NULL,68002,1,2,0)`, actorID, actorID, expiredOperationID, expiredRef).Error; err != nil {
		t.Fatal(err)
	}
	expiredBody := []byte(`{"legacy":"expired-outbox-private"}`)
	expiredDigest := fmt.Sprintf("%x", sha256.Sum256(expiredBody))
	if err := gdb.Exec(`INSERT INTO admin_operation_responses (guid,created_at,created_by,updated_at,updated_by,is_deleted,operation_id,lifecycle_state,integrity_version,response_hmac,http_status,media_type,response_body,body_sha256) VALUES (68031,2,?,2,?,0,?,1,1,?,201,'application/json',?,?)`, actorID, actorID, expiredOperationID, strings.Repeat("4", 64), expiredBody, expiredDigest).Error; err != nil {
		t.Fatal(err)
	}
	unresolvedRef := "op_" + strings.Repeat("v", 43)
	if err := gdb.Exec(`INSERT INTO admin_operations (guid,actor_user_id,actor_auth_version,session_id,action,idempotency_key_hmac,request_hmac,state,public_ref,finished_at,query_expires_at,created_at,created_by,updated_at,updated_by,is_deleted) VALUES (68024,?,1,?,9,?,?,5,?,2,3,1,?,2,?,1)`, actorID, sessionID, strings.Repeat("6", 64), strings.Repeat("7", 64), unresolvedRef, actorID, actorID).Error; err != nil {
		t.Fatal(err)
	}
	var unresolvedOperationID int64
	if err := gdb.Raw("SELECT id FROM admin_operations WHERE guid=68024").Row().Scan(&unresolvedOperationID); err != nil || unresolvedOperationID <= 0 {
		t.Fatal(err)
	}
	unresolvedBody := []byte(`{"legacy":"must-be-erased"}`)
	unresolvedDigest := fmt.Sprintf("%x", sha256.Sum256(unresolvedBody))
	if err := gdb.Exec(`INSERT INTO admin_operation_responses (guid,created_at,created_by,updated_at,updated_by,is_deleted,operation_id,lifecycle_state,integrity_version,response_hmac,http_status,media_type,response_body,body_sha256) VALUES (68021,2,?,2,?,0,?,1,1,?,201,'application/json',?,?)`, actorID, actorID, unresolvedOperationID, strings.Repeat("1", 64), unresolvedBody, unresolvedDigest).Error; err != nil {
		t.Fatal(err)
	}
	legacyRedactedRef := "op_" + strings.Repeat("w", 43)
	if err := gdb.Exec(`INSERT INTO admin_operations (guid,actor_user_id,actor_auth_version,session_id,action,idempotency_key_hmac,request_hmac,state,public_ref,finished_at,query_expires_at,created_at,created_by,updated_at,updated_by,is_deleted) VALUES (68034,?,1,?,9,?,?,5,?,2,3,1,?,2,?,1)`, actorID, sessionID, strings.Repeat("8", 64), strings.Repeat("9", 64), legacyRedactedRef, actorID, actorID).Error; err != nil {
		t.Fatal(err)
	}
	var legacyRedactedOperationID int64
	if err := gdb.Raw("SELECT id FROM admin_operations WHERE guid=68034").Row().Scan(&legacyRedactedOperationID); err != nil || legacyRedactedOperationID <= 0 {
		t.Fatal(err)
	}
	legacyRedactedBody := []byte("PI")
	legacyRedactedDigest := fmt.Sprintf("%x", sha256.Sum256(legacyRedactedBody))
	if err := gdb.Exec(`INSERT INTO admin_operation_responses (guid,created_at,created_by,updated_at,updated_by,is_deleted,operation_id,lifecycle_state,integrity_version,response_hmac,http_status,media_type,response_body,body_sha256) VALUES (68041,2,?,3,?,1,?,2,1,?,201,'application/json',?,?)`, actorID, actorID, legacyRedactedOperationID, strings.Repeat("5", 64), legacyRedactedBody, legacyRedactedDigest).Error; err != nil {
		t.Fatal(err)
	}
	statements := splitStatements(string(migration.UpSQL))
	if len(statements) != 4 {
		t.Fatalf("0010 statements=%d want=4", len(statements))
	}
	return statements
}

func TestAdminResponseIntegrityMigrationResumesEveryCommittedPrefix(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix int
	}{
		{name: "response_alter_only", prefix: 1},
		{name: "outbox_column", prefix: 2},
		{name: "backfill", prefix: 3},
		{name: "outcome_check", prefix: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gdb := permissionSchemaDB(t)
			statements := prepareAdminResponseIntegrityPrefix(t, gdb)
			for index := 0; index < tc.prefix; index++ {
				if err := gdb.Exec(statements[index]).Error; err != nil {
					t.Fatalf("apply committed prefix statement %d: %v", index+1, err)
				}
			}
			calls := 0
			if err := Up(context.Background(), gdb, func() int64 {
				calls++
				return 39_000 + int64(calls)
			}, func() int64 { return 1_900_000_000_001 }); err != nil {
				t.Fatalf("resume after %s: %v", tc.name, err)
			}
			if calls != 2 {
				t.Fatalf("resume after %s allocated %d ledger GUIDs, want 2 for 0009 and 0010", tc.name, calls)
			}
			if err := Verify(context.Background(), gdb); err != nil {
				t.Fatalf("verify resumed %s: %v", tc.name, err)
			}
			var failureCode sql.NullInt64
			if err := gdb.Raw("SELECT failure_code FROM admin_action_outbox WHERE guid=38004").Row().Scan(&failureCode); err != nil || !failureCode.Valid || failureCode.Int64 != 2 {
				t.Fatalf("resumed %s failure_code = %#v (%v), want 2", tc.name, failureCode, err)
			}
			calls = 0
			if err := Up(context.Background(), gdb, func() int64 { calls++; return 49_000 }, func() int64 { return 1_900_000_000_002 }); err != nil || calls != 0 {
				t.Fatalf("rerun completed %s err=%v GUID calls=%d", tc.name, err, calls)
			}
		})
	}
}

func TestAdminResponseIntegrityMigrationRejectsNonPrefixPartialDDL(t *testing.T) {
	gdb := permissionSchemaDB(t)
	permissionUp(t, gdb)
	targetMigration := adminResponseTargetMigration(t)
	if err := executeAdminOperationSafetyFixtureSQL(gdb, targetMigration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version='0010'").Error; err != nil {
		t.Fatal(err)
	}
	migration := adminResponseIntegrityMigration(t)
	if err := executeAdminOperationSafetyFixtureSQL(gdb, migration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version='0009'").Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("ALTER TABLE admin_operation_responses ADD COLUMN lifecycle_state INT NOT NULL DEFAULT 1 AFTER operation_id").Error; err != nil {
		t.Fatal(err)
	}
	if err := Up(context.Background(), gdb, sequentialGUID(59_000), func() int64 { return 1_900_000_000_003 }); err == nil {
		t.Fatal("non-prefix partial 0009 response DDL was accepted")
	}
	assertMigrationLedgerCount(t, gdb, "0009", 0)
}

func prepareAdminResponseIntegrityPrefix(t *testing.T, gdb *gorm.DB) []string {
	t.Helper()
	permissionUp(t, gdb)
	targetMigration := adminResponseTargetMigration(t)
	if err := executeAdminOperationSafetyFixtureSQL(gdb, targetMigration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version='0010'").Error; err != nil {
		t.Fatal(err)
	}
	migration := adminResponseIntegrityMigration(t)
	if err := executeAdminOperationSafetyFixtureSQL(gdb, migration.DownSQL); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("DELETE FROM schema_migrations WHERE version='0009'").Error; err != nil {
		t.Fatal(err)
	}
	insertMigrationUserWithDefaultGroup(t, gdb, 38_001, "integrity-prefix")
	var userID int64
	if err := gdb.Raw("SELECT id FROM users WHERE guid=38001").Row().Scan(&userID); err != nil || userID <= 0 {
		t.Fatalf("read prefix user id=%d err=%v", userID, err)
	}
	if err := gdb.Exec(`INSERT INTO user_sessions (guid,sid,user_id,login_method,session_version,refresh_hmac,last_active_at,expires_at,created_at,updated_at,is_deleted) VALUES (38002,'00000000-0000-4000-8000-000000038002',?,1,1,?,1,2,1,1,0)`, userID, strings.Repeat("a", 64)).Error; err != nil {
		t.Fatal(err)
	}
	var sessionID int64
	if err := gdb.Raw("SELECT id FROM user_sessions WHERE guid=38002").Row().Scan(&sessionID); err != nil || sessionID <= 0 {
		t.Fatalf("read prefix session id=%d err=%v", sessionID, err)
	}
	publicRef := "op_" + strings.Repeat("r", 43)
	if err := gdb.Exec(`INSERT INTO admin_operations (guid,actor_user_id,actor_auth_version,session_id,action,idempotency_key_hmac,request_hmac,state,public_ref,finished_at,query_expires_at,error_code,result_http_status,created_at,updated_at,is_deleted) VALUES (38003,?,1,?,4,?,?,3,?,2,3,2,409,1,2,0)`, userID, sessionID, strings.Repeat("b", 64), strings.Repeat("c", 64), publicRef).Error; err != nil {
		t.Fatal(err)
	}
	var operationID int64
	if err := gdb.Raw("SELECT id FROM admin_operations WHERE guid=38003").Row().Scan(&operationID); err != nil || operationID <= 0 {
		t.Fatalf("read prefix operation id=%d err=%v", operationID, err)
	}
	if err := gdb.Exec(`INSERT INTO admin_action_outbox (guid,created_at,updated_at,is_deleted,operation_id,public_ref,action,target_kind,state,delivery_state,available_at,attempt_count) VALUES (38004,1,2,0,?,?,4,1,3,1,2,0)`, operationID, publicRef).Error; err != nil {
		t.Fatal(err)
	}
	statements := splitStatements(string(migration.UpSQL))
	if len(statements) != 4 {
		t.Fatalf("0009 statements=%d want=4", len(statements))
	}
	return statements
}

func adminResponseIntegrityMigration(t *testing.T) Migration {
	t.Helper()
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 10 || migrations[8].Version != "0009" {
		t.Fatalf("unexpected migration count/tail: %d/%q", len(migrations), migrations[8].Version)
	}
	return migrations[8]
}

func adminResponseTargetMigration(t *testing.T) Migration {
	t.Helper()
	migrations, err := All()
	if err != nil || len(migrations) != 10 || migrations[9].Version != "0010" {
		t.Fatalf("unexpected 0010 migration count/tail: %d/%v", len(migrations), err)
	}
	return migrations[9]
}
