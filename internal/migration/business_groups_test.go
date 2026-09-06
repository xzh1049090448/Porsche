package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestBusinessGroupMigrationLatest(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 7 || migrations[6].Version != "0007" {
		t.Fatalf("All() = %#v, want exactly seven migrations ending at 0007", migrations)
	}

	wantPublished := []string{
		"2da41ffd07c44d45cb05a705f867db2f2b8f01defb519000197dedce9998aedd",
		"58712428ca668fb1fea0943d71a2209b2e7faf26de043d870195a033ac0f413c",
		"31c49d9bb1f171d9ea6caab49714d9de05552b8f6e9cb73f2989760efd0a015c",
		"44b5caba0473162c239e6b3035e6d9067e3af0b35e998a79c827a1424621494e",
		"4fc34da357e155c4a04548838153796803f618f0c7199adda17098ef5190cd68",
		"c0bc9f68370985315db711c0028ee644dafe4d9667c595a49193ed38e2d6f6b6",
	}
	for i, want := range wantPublished {
		if got := fmt.Sprintf("%x", sha256.Sum256(migrations[i].UpSQL)); got != want {
			t.Fatalf("published migration %s checksum changed: got %s want %s", migrations[i].Version, got, want)
		}
	}

	const marker = "-- porsche:seed-default-business-group"
	up := strings.ToLower(string(migrations[6].UpSQL))
	if strings.Count(up, marker) != 1 {
		t.Fatalf("0007 marker count = %d, want exactly one", strings.Count(up, marker))
	}
	for _, fragment := range []string{
		"create table if not exists business_groups",
		"id bigint not null auto_increment primary key",
		"guid bigint not null",
		"group_key varchar(64) not null",
		"display_name varchar(64) not null",
		"status int not null",
		"created_at bigint not null",
		"created_by bigint null",
		"updated_at bigint not null",
		"updated_by bigint null",
		"is_deleted int not null default 0",
		"unique key uk_business_groups_guid (guid)",
		"unique key uk_business_groups_key_deleted (group_key, is_deleted)",
		"key idx_business_groups_active (status, is_deleted, group_key)",
		"constraint chk_business_groups_status check (status in (1, 2))",
		"constraint chk_business_groups_audit check (created_at >= 0 and updated_at >= 0 and is_deleted in (0, 1))",
		"engine=innodb default charset=utf8mb4 collate=utf8mb4_unicode_ci",
		"alter table users add column group_id bigint null",
		"update users set group_id = ? where group_id is null",
		"alter table users modify column group_id bigint not null",
		"create index idx_users_group_id on users (group_id)",
		"constraint fk_users_business_group foreign key (group_id) references business_groups(id) on delete restrict on update restrict",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0007 missing %q", fragment)
		}
	}
	for _, forbidden := range []string{"timestamp", "datetime", " enum(", "automigrate", "insert into business_groups", "delete from users"} {
		if strings.Contains(up, forbidden) {
			t.Errorf("0007 contains forbidden %q", forbidden)
		}
	}

	down := strings.ToLower(string(migrations[6].DownSQL))
	for _, fragment := range []string{
		"alter table users drop foreign key fk_users_business_group",
		"alter table users drop index idx_users_group_id",
		"alter table users drop column group_id",
		"drop table business_groups",
	} {
		if !strings.Contains(down, fragment) {
			t.Errorf("0007 down missing %q", fragment)
		}
	}
}

func TestBusinessGroupModelUsesStableEnumsAndInternalAssociation(t *testing.T) {
	if models.BusinessGroupStatusActive != 1 || models.BusinessGroupStatusInactive != 2 {
		t.Fatalf("business group statuses = %d/%d, want stable 1/2", models.BusinessGroupStatusActive, models.BusinessGroupStatusInactive)
	}
	for _, tc := range []struct {
		name string
		want models.BusinessGroupStatus
	}{
		{"active", models.BusinessGroupStatusActive},
		{"inactive", models.BusinessGroupStatusInactive},
	} {
		got, ok := models.ParseBusinessGroupStatus(tc.name)
		if !ok || got != tc.want || got.String() != tc.name {
			t.Errorf("status %q round trip = %d/%v/%q", tc.name, got, ok, got.String())
		}
	}
	if got, ok := models.ParseBusinessGroupStatus("unknown"); ok || got != 0 || models.BusinessGroupStatus(99).String() != "unknown" {
		t.Fatal("unknown business group status accepted")
	}

	parsed, err := schema.Parse(&models.BusinessGroup{}, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("parse BusinessGroup schema: %v", err)
	}
	if parsed.Table != "business_groups" {
		t.Fatalf("BusinessGroup table = %q", parsed.Table)
	}
	for _, expected := range []struct {
		name      string
		dbName    string
		dataType  string
		notNull   bool
		creatable bool
		updatable bool
	}{
		{"ID", "id", "bigint", true, true, true},
		{"Guid", "guid", "bigint", true, true, true},
		{"Key", "group_key", "varchar(64)", true, true, false},
		{"DisplayName", "display_name", "varchar(64)", true, true, true},
		{"Status", "status", "int", true, true, true},
	} {
		field := parsed.LookUpField(expected.name)
		if field == nil {
			t.Errorf("BusinessGroup missing %s", expected.name)
			continue
		}
		if field.DBName != expected.dbName || string(field.DataType) != expected.dataType || field.NotNull != expected.notNull || field.Creatable != expected.creatable || field.Updatable != expected.updatable {
			t.Errorf("BusinessGroup.%s metadata = db:%q type:%q notnull:%v create:%v update:%v", expected.name, field.DBName, field.DataType, field.NotNull, field.Creatable, field.Updatable)
		}
	}

	groupID, ok := reflect.TypeOf(models.User{}).FieldByName("GroupID")
	if !ok || groupID.Type != reflect.TypeOf(int64(0)) || groupID.Tag.Get("gorm") != "column:group_id;type:bigint;not null;index:idx_users_group_id" || groupID.Tag.Get("json") != "-" {
		t.Fatalf("User.GroupID must be an internal non-null BIGINT association with the fixed index: %#v", groupID)
	}
}

func TestBusinessGroupSchemaContractRejectsMetadataDrift(t *testing.T) {
	want := businessGroupTableContractDefinition()
	valid := businessGroupMetadataFromContract(want)
	if !matchesBusinessGroupTableContract(want, valid) {
		t.Fatal("exact business_groups metadata rejected")
	}
	mutations := []struct {
		name string
		edit func(*businessGroupTableMetadata)
	}{
		{"engine", func(g *businessGroupTableMetadata) { g.engine = "MyISAM" }},
		{"charset", func(g *businessGroupTableMetadata) { g.characterSet = "latin1" }},
		{"collation", func(g *businessGroupTableMetadata) { g.collation = "utf8mb4_bin" }},
		{"column_name", func(g *businessGroupTableMetadata) { g.columns[1].name = "uuid" }},
		{"column_order", func(g *businessGroupTableMetadata) { g.columns[1], g.columns[2] = g.columns[2], g.columns[1] }},
		{"column_type", func(g *businessGroupTableMetadata) { g.columns[2].columnType = "varchar(63)" }},
		{"column_unsigned", func(g *businessGroupTableMetadata) { g.columns[0].columnType = "bigint unsigned" }},
		{"column_nullable", func(g *businessGroupTableMetadata) { g.columns[3].nullable = "YES" }},
		{"column_default", func(g *businessGroupTableMetadata) {
			g.columns[9].defaultVal = sql.NullString{String: "1", Valid: true}
		}},
		{"column_extra", func(g *businessGroupTableMetadata) { g.columns[0].extra = "" }},
		{"column_charset", func(g *businessGroupTableMetadata) { g.columns[2].characterSet = "latin1" }},
		{"column_collation", func(g *businessGroupTableMetadata) { g.columns[2].collation = "utf8mb4_bin" }},
		{"missing_unique", func(g *businessGroupTableMetadata) { g.indexes = append(g.indexes[:1], g.indexes[2:]...) }},
		{"missing_index", func(g *businessGroupTableMetadata) { g.indexes = g.indexes[:len(g.indexes)-1] }},
		{"extra_index", func(g *businessGroupTableMetadata) {
			g.indexes = append(g.indexes, businessGroupIndexMetadata{name: "extra", column: "id", sequence: 1, nonUnique: 1})
		}},
		{"index_order", func(g *businessGroupTableMetadata) { g.indexes[2].sequence = 2 }},
		{"index_unique", func(g *businessGroupTableMetadata) { g.indexes[1].nonUnique = 1 }},
		{"missing_check", func(g *businessGroupTableMetadata) { g.checks = g.checks[1:] }},
		{"extra_check", func(g *businessGroupTableMetadata) {
			g.checks = append(g.checks, businessGroupCheckMetadata{name: "extra", clause: "status = 1", enforced: "YES"})
		}},
		{"disabled_check", func(g *businessGroupTableMetadata) { g.checks[0].enforced = "NO" }},
		{"changed_check", func(g *businessGroupTableMetadata) { g.checks[0].clause = "status IN (1, 3)" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			got := cloneBusinessGroupTableMetadata(valid)
			tc.edit(&got)
			if matchesBusinessGroupTableContract(want, got) {
				t.Fatal("business_groups schema drift accepted")
			}
		})
	}
}

func TestBusinessGroupUserRelationContractRejectsMetadataDrift(t *testing.T) {
	want := businessGroupUserRelationContractDefinition()
	valid := businessGroupUserRelationMetadataFromContract(want, "fixture")
	if !matchesBusinessGroupUserRelationContract(want, valid, "fixture", false) {
		t.Fatal("exact users.group_id metadata rejected")
	}
	mutations := []struct {
		name string
		edit func(*businessGroupUserRelationMetadata)
	}{
		{"missing_column", func(g *businessGroupUserRelationMetadata) { g.columns = nil }},
		{"wrong_column", func(g *businessGroupUserRelationMetadata) { g.columns[0].name = "group_guid" }},
		{"column_type", func(g *businessGroupUserRelationMetadata) { g.columns[0].columnType = "int" }},
		{"column_unsigned", func(g *businessGroupUserRelationMetadata) { g.columns[0].columnType = "bigint unsigned" }},
		{"column_nullable", func(g *businessGroupUserRelationMetadata) { g.columns[0].nullable = "YES" }},
		{"column_default", func(g *businessGroupUserRelationMetadata) {
			g.columns[0].defaultVal = sql.NullString{String: "0", Valid: true}
		}},
		{"column_extra", func(g *businessGroupUserRelationMetadata) { g.columns[0].extra = "auto_increment" }},
		{"missing_index", func(g *businessGroupUserRelationMetadata) { g.indexes = nil }},
		{"extra_index", func(g *businessGroupUserRelationMetadata) {
			g.indexes = append(g.indexes, businessGroupIndexMetadata{name: "extra", column: "group_id", sequence: 1, nonUnique: 1})
		}},
		{"index_name", func(g *businessGroupUserRelationMetadata) { g.indexes[0].name = "idx_users_group" }},
		{"index_unique", func(g *businessGroupUserRelationMetadata) { g.indexes[0].nonUnique = 0 }},
		{"missing_fk", func(g *businessGroupUserRelationMetadata) { g.foreignKeys = nil }},
		{"extra_fk", func(g *businessGroupUserRelationMetadata) { g.foreignKeys = append(g.foreignKeys, g.foreignKeys[0]) }},
		{"fk_name", func(g *businessGroupUserRelationMetadata) { g.foreignKeys[0].name = "fk_users_group" }},
		{"fk_schema", func(g *businessGroupUserRelationMetadata) { g.foreignKeys[0].targetSchema = "other" }},
		{"fk_table", func(g *businessGroupUserRelationMetadata) { g.foreignKeys[0].targetTable = "other_groups" }},
		{"fk_column", func(g *businessGroupUserRelationMetadata) { g.foreignKeys[0].targetColumn = "guid" }},
		{"fk_source_column", func(g *businessGroupUserRelationMetadata) { g.foreignKeys[0].column = "guid" }},
		{"fk_composite", func(g *businessGroupUserRelationMetadata) { g.foreignKeys[0].ordinal = 2 }},
		{"fk_delete_cascade", func(g *businessGroupUserRelationMetadata) { g.foreignKeys[0].deleteRule = "CASCADE" }},
		{"fk_update_cascade", func(g *businessGroupUserRelationMetadata) { g.foreignKeys[0].updateRule = "CASCADE" }},
	}
	for _, tc := range mutations {
		t.Run(tc.name, func(t *testing.T) {
			got := cloneBusinessGroupUserRelationMetadata(valid)
			tc.edit(&got)
			if matchesBusinessGroupUserRelationContract(want, got, "fixture", false) {
				t.Fatal("users.group_id schema drift accepted")
			}
		})
	}
	if !matchesBusinessGroupUserRelationContract(want, valid, "fixture", true) {
		t.Fatal("pre-seed relation phase must accept the exact nullable group_id column")
	}
}

func TestBusinessGroupVerifierFailsClosedAndRejectsInvalidDataCounts(t *testing.T) {
	if err := VerifyBusinessGroupsSchema(context.Background(), nil); !errors.Is(err, ErrBusinessGroupsSchema) {
		t.Fatalf("nil database error = %v", err)
	}
	if ErrBusinessGroupsSchema.Error() != "business groups schema mismatch or unavailable" {
		t.Fatalf("verifier error exposes diagnostics: %q", ErrBusinessGroupsSchema)
	}
	for _, tc := range []struct {
		defaults int64
		nulls    int64
		orphans  int64
		want     bool
	}{
		{1, 0, 0, true},
		{0, 0, 0, false},
		{2, 0, 0, false},
		{1, 1, 0, false},
		{1, 0, 1, false},
	} {
		if got := validBusinessGroupDataCounts(tc.defaults, tc.nulls, tc.orphans); got != tc.want {
			t.Errorf("data counts (%d,%d,%d) valid=%v want %v", tc.defaults, tc.nulls, tc.orphans, got, tc.want)
		}
	}
}

func TestBusinessGroupMarkerMustSplitExactlyOnce(t *testing.T) {
	valid := []byte("SELECT 1;\n-- porsche:seed-default-business-group\nSELECT 2;")
	pre, post, err := splitBusinessGroupsMigration(valid)
	if err != nil || len(pre) != 1 || len(post) != 1 {
		t.Fatalf("valid marker split = %#v/%#v/%v", pre, post, err)
	}
	for _, malformed := range [][]byte{
		[]byte("SELECT 1;"),
		[]byte("-- porsche:seed-default-business-group\n-- porsche:seed-default-business-group"),
	} {
		if _, _, err := splitBusinessGroupsMigration(malformed); err == nil {
			t.Fatalf("malformed marker accepted: %q", malformed)
		}
	}
}

func TestBusinessGroupMigrationOnIsolatedMySQL(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("TEST_DATABASE_URL is not set; isolated MySQL business group migration test skipped")
	}
	t.Run("backfills_active_and_tombstoned_users", func(t *testing.T) {
		gdb := permissionSchemaDB(t)
		applyThroughAdminActionOutbox(t, gdb)
		insertMigrationUser(t, gdb, 7101, "business-group-active", 0)
		insertMigrationUser(t, gdb, 7102, "business-group-tombstone", 1)

		allocated := []int64{7201, 7202}
		calls := 0
		nextGUID := func() int64 {
			value := allocated[calls]
			calls++
			return value
		}
		if err := Up(context.Background(), gdb, nextGUID, func() int64 { return 1_700_000_000_007 }); err != nil {
			t.Fatalf("apply 0007: %v", err)
		}
		if calls != 2 {
			t.Fatalf("GUID calls = %d, want default group plus ledger", calls)
		}
		assertBusinessGroupBackfill(t, gdb, 2, 7201)
		if err := VerifyBusinessGroupsSchema(context.Background(), gdb); err != nil {
			t.Fatalf("verify 0007: %v", err)
		}
	})

	t.Run("resumes_after_seed_and_partial_backfill", func(t *testing.T) {
		gdb := permissionSchemaDB(t)
		applyThroughAdminActionOutbox(t, gdb)
		insertMigrationUser(t, gdb, 7301, "business-group-crash-active", 0)
		insertMigrationUser(t, gdb, 7302, "business-group-crash-tombstone", 1)
		migrations, err := All()
		if err != nil {
			t.Fatal(err)
		}
		pre, _, err := splitBusinessGroupsMigration(migrations[6].UpSQL)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range pre {
			if err := gdb.Exec(statement).Error; err != nil {
				t.Fatalf("simulate pre-seed phase: %v", err)
			}
		}
		if err := gdb.Exec(`INSERT INTO business_groups (guid,group_key,display_name,status,created_at,created_by,updated_at,updated_by,is_deleted) VALUES (7401,'default','Default',1,1700000000007,NULL,1700000000007,NULL,0)`).Error; err != nil {
			t.Fatal(err)
		}
		if err := gdb.Exec("UPDATE users SET group_id=(SELECT id FROM business_groups WHERE group_key='default') WHERE username='business-group-crash-active'").Error; err != nil {
			t.Fatal(err)
		}

		calls := 0
		if err := Up(context.Background(), gdb, func() int64 { calls++; return 7500 + int64(calls) }, func() int64 { return 1_700_000_000_008 }); err != nil {
			t.Fatalf("resume 0007: %v", err)
		}
		if calls != 1 {
			t.Fatalf("resume allocated %d GUIDs, want ledger only", calls)
		}
		assertBusinessGroupBackfill(t, gdb, 2, 7401)

		rerunCalls := 0
		if err := Up(context.Background(), gdb, func() int64 { rerunCalls++; return 7600 }, func() int64 { return 1_700_000_000_009 }); err != nil {
			t.Fatalf("rerun completed 0007: %v", err)
		}
		if rerunCalls != 0 {
			t.Fatalf("completed rerun allocated %d GUIDs", rerunCalls)
		}
	})

	t.Run("rejects_malformed_partial_schema_without_ledger", func(t *testing.T) {
		gdb := permissionSchemaDB(t)
		applyThroughAdminActionOutbox(t, gdb)
		if err := gdb.Exec(`CREATE TABLE business_groups (id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY, guid BIGINT NOT NULL) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`).Error; err != nil {
			t.Fatal(err)
		}
		if err := Up(context.Background(), gdb, func() int64 { return 7701 }, func() int64 { return 1 }); err == nil {
			t.Fatal("malformed partial business_groups schema accepted")
		}
		var count int64
		if err := gdb.Raw("SELECT COUNT(*) FROM schema_migrations WHERE version='0007'").Scan(&count).Error; err != nil || count != 0 {
			t.Fatalf("malformed schema entered ledger: count=%d err=%v", count, err)
		}
	})
}

func TestBusinessGroupVerifierRejectsRealSchemaAndDataDrift(t *testing.T) {
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" {
		t.Skip("TEST_DATABASE_URL is not set; isolated MySQL business group verifier drift test skipped")
	}
	for _, tc := range []struct {
		name string
		ddl  []string
	}{
		{"business_group_default", []string{"ALTER TABLE business_groups MODIFY is_deleted INT NOT NULL DEFAULT 1"}},
		{"nullable_user_group", []string{"ALTER TABLE users DROP FOREIGN KEY fk_users_business_group", "ALTER TABLE users MODIFY group_id BIGINT NULL"}},
		{"cascade_foreign_key", []string{"ALTER TABLE users DROP FOREIGN KEY fk_users_business_group", "ALTER TABLE users ADD CONSTRAINT fk_users_business_group FOREIGN KEY(group_id) REFERENCES business_groups(id) ON DELETE CASCADE ON UPDATE CASCADE"}},
		{"no_active_default", []string{"UPDATE business_groups SET status=2 WHERE group_key='default' AND is_deleted=0"}},
		{"multiple_active_defaults", []string{"ALTER TABLE business_groups DROP INDEX uk_business_groups_key_deleted", "INSERT INTO business_groups (guid,group_key,display_name,status,created_at,updated_at,is_deleted) VALUES (7998,'default','Duplicate',1,1,1,0)"}},
		{"orphaned_user_group", []string{"ALTER TABLE users DROP FOREIGN KEY fk_users_business_group", "UPDATE users SET group_id=group_id+999999"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gdb := permissionSchemaDB(t)
			if err := Up(context.Background(), gdb, sequentialGUID(7800), func() int64 { return 1_700_000_000_010 }); err != nil {
				t.Fatal(err)
			}
			inserted := false
			if tc.name == "orphaned_user_group" {
				insertMigrationUserWithDefaultGroup(t, gdb, 7999, "business-group-orphan")
				inserted = true
			}
			for _, statement := range tc.ddl {
				if err := gdb.Exec(statement).Error; err != nil {
					t.Fatalf("apply drift %q: %v", statement, err)
				}
			}
			if err := VerifyBusinessGroupsSchema(context.Background(), gdb); err == nil {
				t.Fatalf("accepted real drift %s (inserted=%v)", tc.name, inserted)
			}
		})
	}
}

func applyThroughAdminActionOutbox(t *testing.T, gdb *gorm.DB) {
	t.Helper()
	if err := gdb.Exec(createLedgerSQL).Error; err != nil {
		t.Fatal(err)
	}
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	for i, migration := range migrations[:6] {
		for _, statement := range splitStatements(string(migration.UpSQL)) {
			if err := gdb.Exec(statement).Error; err != nil {
				t.Fatalf("apply %s: %v", migration.Version, err)
			}
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(migration.UpSQL))
		if err := gdb.Exec("INSERT INTO schema_migrations (guid,version,checksum,created_at,updated_at,is_deleted) VALUES (?,?,?,?,?,0)", 7000+i, migration.Version, checksum, 1, 1).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func insertMigrationUser(t *testing.T, gdb *gorm.DB, guid int64, username string, deleted int) {
	t.Helper()
	if err := gdb.Exec("INSERT INTO users (guid,username,password_hash,allowed_models,created_at,updated_at,is_deleted) VALUES (?,?,?,?,?,?,?)", guid, username, "hash", "[]", 1, 1, deleted).Error; err != nil {
		t.Fatal(err)
	}
}

func insertMigrationUserWithDefaultGroup(t *testing.T, gdb *gorm.DB, guid int64, username string) {
	t.Helper()
	if err := gdb.Exec("INSERT INTO users (guid,username,password_hash,allowed_models,created_at,updated_at,is_deleted,group_id) SELECT ?,?,'hash','[]',1,1,0,id FROM business_groups WHERE group_key='default' AND status=1 AND is_deleted=0", guid, username).Error; err != nil {
		t.Fatal(err)
	}
}

func assertBusinessGroupBackfill(t *testing.T, gdb *gorm.DB, wantUsers int64, wantGUID int64) {
	t.Helper()
	var group struct {
		ID        int64
		GUID      int64
		Status    int
		Deleted   int
		CreatedBy sql.NullInt64
		UpdatedBy sql.NullInt64
	}
	if err := gdb.Raw("SELECT id,guid,status,is_deleted,created_by,updated_by FROM business_groups WHERE group_key='default'").Row().Scan(&group.ID, &group.GUID, &group.Status, &group.Deleted, &group.CreatedBy, &group.UpdatedBy); err != nil {
		t.Fatal(err)
	}
	if group.GUID != wantGUID || group.Status != 1 || group.Deleted != 0 || group.CreatedBy.Valid || group.UpdatedBy.Valid {
		t.Fatalf("default group = %#v", group)
	}
	var users int64
	if err := gdb.Raw("SELECT COUNT(*) FROM users WHERE group_id=?", group.ID).Scan(&users).Error; err != nil || users != wantUsers {
		t.Fatalf("backfilled users = %d err=%v, want %d", users, err, wantUsers)
	}
	var groups int64
	if err := gdb.Raw("SELECT COUNT(*) FROM business_groups WHERE group_key='default' AND status=1 AND is_deleted=0").Scan(&groups).Error; err != nil || groups != 1 {
		t.Fatalf("active defaults = %d err=%v", groups, err)
	}
}

func sequentialGUID(start int64) func() int64 {
	return func() int64 {
		start++
		return start
	}
}

func businessGroupMetadataFromContract(contract businessGroupTableContract) businessGroupTableMetadata {
	metadata := businessGroupTableMetadata{engine: "InnoDB", characterSet: "utf8mb4", collation: "utf8mb4_unicode_ci"}
	for _, column := range contract.columns {
		metadata.columns = append(metadata.columns, businessGroupColumnMetadata{
			name: column.name, columnType: column.columnType, nullable: column.nullable,
			defaultVal: column.defaultVal, extra: column.extra, characterSet: column.characterSet, collation: column.collation,
		})
	}
	for _, index := range contract.indexes {
		for i, column := range index.columns {
			nonUnique := 1
			if index.unique {
				nonUnique = 0
			}
			metadata.indexes = append(metadata.indexes, businessGroupIndexMetadata{name: index.name, column: column, sequence: i + 1, nonUnique: nonUnique})
		}
	}
	for _, check := range contract.checks {
		metadata.checks = append(metadata.checks, businessGroupCheckMetadata{name: check.name, clause: check.clause, enforced: "YES"})
	}
	return metadata
}

func cloneBusinessGroupTableMetadata(value businessGroupTableMetadata) businessGroupTableMetadata {
	value.columns = append([]businessGroupColumnMetadata(nil), value.columns...)
	value.indexes = append([]businessGroupIndexMetadata(nil), value.indexes...)
	value.checks = append([]businessGroupCheckMetadata(nil), value.checks...)
	return value
}

func businessGroupUserRelationMetadataFromContract(contract businessGroupUserRelationContract, targetSchema string) businessGroupUserRelationMetadata {
	return businessGroupUserRelationMetadata{
		columns: []businessGroupColumnMetadata{contract.column},
		indexes: []businessGroupIndexMetadata{{name: contract.index.name, column: "group_id", sequence: 1, nonUnique: 1}},
		foreignKeys: []businessGroupForeignKeyMetadata{{
			name: contract.foreignKey.name, column: "group_id", ordinal: 1, targetSchema: targetSchema,
			targetTable: "business_groups", targetColumn: "id", deleteRule: "RESTRICT", updateRule: "NO ACTION",
		}},
	}
}

func cloneBusinessGroupUserRelationMetadata(value businessGroupUserRelationMetadata) businessGroupUserRelationMetadata {
	value.columns = append([]businessGroupColumnMetadata(nil), value.columns...)
	value.indexes = append([]businessGroupIndexMetadata(nil), value.indexes...)
	value.foreignKeys = append([]businessGroupForeignKeyMetadata(nil), value.foreignKeys...)
	return value
}
