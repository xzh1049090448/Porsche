package migration

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestAdminOperationRolePermissionResults0013MigrationContract(t *testing.T) {
	migrations, err := All()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) != 13 || migrations[12].Version != "0013" {
		t.Fatalf("migration 0013 is missing or out of order: %#v", migrations)
	}
	up := strings.ToLower(strings.TrimSpace(string(migrations[12].UpSQL)))
	for _, fragment := range []string{
		"alter table admin_operations",
		"add column result_permissions_version bigint null after result_auth_version",
		"add column result_role int null after result_permissions_version",
		"drop check chk_admin_operations_result_auth_version",
		"add constraint chk_admin_operations_result_role_permission check",
	} {
		if !strings.Contains(up, fragment) {
			t.Errorf("0013 up missing %q: %s", fragment, up)
		}
	}
	for _, forbidden := range []string{"create index", "add index", "add key", "timestamp", "datetime", "update ", "delete "} {
		if strings.Contains(up, forbidden) {
			t.Errorf("0013 up contains forbidden %q: %s", forbidden, up)
		}
	}
	down := strings.ToLower(strings.TrimSpace(string(migrations[12].DownSQL)))
	for _, fragment := range []string{
		"drop check chk_admin_operations_result_role_permission",
		"update admin_operations\nset state = 4,\n    finished_at = null,\n    error_code = null,\n    result_kind = null,\n    result_guid = null,\n    result_auth_version = null,\n    result_http_status = null\nwhere state = 2 and action in (3, 4, 5)",
		"drop column result_role,\n  drop column result_permissions_version",
		"add constraint chk_admin_operations_result_auth_version check",
	} {
		if !strings.Contains(down, fragment) {
			t.Errorf("0013 down missing %q: %s", fragment, down)
		}
	}

	wantPublished := map[string]string{
		"0001": "2da41ffd07c44d45cb05a705f867db2f2b8f01defb519000197dedce9998aedd",
		"0002": "58712428ca668fb1fea0943d71a2209b2e7faf26de043d870195a033ac0f413c",
		"0003": "31c49d9bb1f171d9ea6caab49714d9de05552b8f6e9cb73f2989760efd0a015c",
		"0004": "44b5caba0473162c239e6b3035e6d9067e3af0b35e998a79c827a1424621494e",
		"0005": "4fc34da357e155c4a04548838153796803f618f0c7199adda17098ef5190cd68",
		"0006": "c0bc9f68370985315db711c0028ee644dafe4d9667c595a49193ed38e2d6f6b6",
		"0007": "b3c3351771fce2dbf5466d300cb92ffd5dbd183cffaff15671e46a8bc87e143e",
		"0008": "21289da334e7ef4425f697c659e6f45227e88c4d86ac4c099867dd6895f666f2",
		"0009": "4dc818d93180bb6777d2ec6d8318e728fe76add4c736a178f19b808ca2afedf7",
		"0010": "b6ddd5b7088f1617b9831186e08da622f7d06cbe985707ef9f5524ffe6057780",
		"0011": "be6ea3beb18a64cf2a2e03b2730df5abbcc457900e9da6292eefee0dd15a2792",
		"0012": "d2f1f841f7176684cd6b953bc69f0f1143e64eca0d758ae7b9e3d1104c97de01",
	}
	for _, migration := range migrations[:12] {
		got := fmt.Sprintf("%x", sha256.Sum256(migration.UpSQL))
		if got != wantPublished[migration.Version] {
			t.Errorf("published migration %s checksum changed: %s", migration.Version, got)
		}
	}
}

func TestAdminOperationRolePermissionResults0013RealMySQLDownPreservesCompatibleResults(t *testing.T) {
	gdb := permissionSchemaDB(t)
	nextGUID := int64(9_120_000_000_000_000)
	if err := Up(context.Background(), gdb, func() int64 { nextGUID++; return nextGUID }, func() int64 { return 1_900_000_000_000 }); err != nil {
		t.Fatalf("apply migrations through 0013: %v", err)
	}
	if err := VerifyAdminOperationRolePermissionResultsSchema(context.Background(), gdb); err != nil {
		t.Fatal(err)
	}
	insertMigrationUserWithDefaultGroup(t, gdb, 9_120_001, "role-result-actor")
	var actorID int64
	if err := gdb.Raw("SELECT id FROM users WHERE guid = ?", 9_120_001).Row().Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec(`INSERT INTO user_sessions (guid,sid,user_id,login_method,session_version,refresh_hmac,last_active_at,expires_at,created_at,updated_at,is_deleted) VALUES (9120002,'00000000-0000-4000-8000-000009120002',?,1,1,?,1,3,1,1,0)`, actorID, strings.Repeat("a", 64)).Error; err != nil {
		t.Fatal(err)
	}
	var sessionID int64
	if err := gdb.Raw("SELECT id FROM user_sessions WHERE guid = ?", 9_120_002).Row().Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	insert := func(guid int64, action int, auth any, permissions any, role any) error {
		return gdb.Exec(`INSERT INTO admin_operations (guid,actor_user_id,actor_auth_version,session_id,action,idempotency_key_hmac,request_hmac,state,public_ref,finished_at,query_expires_at,result_kind,result_guid,result_auth_version,result_permissions_version,result_role,result_http_status,created_at,updated_at,is_deleted) VALUES (?,?,1,?,?,?, ?,2,?,2,3,2,9120099,?,?,?,200,1,2,0)`,
			guid, actorID, sessionID, action, fmt.Sprintf("%064d", guid), fmt.Sprintf("%064d", guid+1), "op_"+fmt.Sprintf("%043d", guid), auth, permissions, role).Error
	}
	if err := insert(9_120_010, 2, 8, nil, nil); err != nil {
		t.Fatalf("insert legacy reset result: %v", err)
	}
	if err := insert(9_120_011, 3, 9, 4, 10); err != nil {
		t.Fatalf("insert A08 promote result: %v", err)
	}
	for _, invalid := range []struct {
		guid             int64
		action           int
		auth, perm, role any
	}{
		{9_120_012, 3, 9, 4, nil},
		{9_120_013, 4, 9, 4, 10},
		{9_120_014, 6, 9, 4, 10},
	} {
		if err := insert(invalid.guid, invalid.action, invalid.auth, invalid.perm, invalid.role); err == nil {
			t.Fatalf("invalid action result inserted: %#v", invalid)
		}
	}

	migrations, _ := All()
	if err := executeAdminOperationSafetyFixtureSQL(gdb, migrations[12].DownSQL); err != nil {
		t.Fatalf("0013 down with A08 history: %v", err)
	}
	var resetState, resetAuth, resetKind, resetStatus int
	var resetResultGUID int64
	if err := gdb.Raw("SELECT state,result_kind,result_guid,result_auth_version,result_http_status FROM admin_operations WHERE guid = ?", 9_120_010).Row().Scan(&resetState, &resetKind, &resetResultGUID, &resetAuth, &resetStatus); err != nil || resetState != 2 || resetKind != 2 || resetResultGUID != 9_120_099 || resetAuth != 8 || resetStatus != 200 {
		t.Fatalf("down changed reset result: state/kind/guid/auth/status=%d/%d/%d/%d/%d err=%v", resetState, resetKind, resetResultGUID, resetAuth, resetStatus, err)
	}
	var a08State, a08Action int
	var a08Finished, a08Failure, a08Kind, a08GUID, a08Auth, a08Status any
	var a08PublicRef string
	if err := gdb.Raw("SELECT state,action,public_ref,finished_at,error_code,result_kind,result_guid,result_auth_version,result_http_status FROM admin_operations WHERE guid = ?", 9_120_011).Row().Scan(&a08State, &a08Action, &a08PublicRef, &a08Finished, &a08Failure, &a08Kind, &a08GUID, &a08Auth, &a08Status); err != nil ||
		a08State != 4 || a08Action != 3 || a08PublicRef != "op_0000000000000000000000000000000000009120011" || a08Finished != nil || a08Failure != nil || a08Kind != nil || a08GUID != nil || a08Auth != nil || a08Status != nil {
		t.Fatalf("down A08 conservative result: state/action/ref=%d/%d/%q terminal=%#v/%#v/%#v/%#v/%#v/%#v err=%v", a08State, a08Action, a08PublicRef, a08Finished, a08Failure, a08Kind, a08GUID, a08Auth, a08Status, err)
	}
	if err := VerifyAdminOperationSafetySchema(context.Background(), gdb); err != nil {
		t.Fatalf("0012 verifier after down: %v", err)
	}
	if err := executeAdminOperationSafetyFixtureSQL(gdb, migrations[12].UpSQL); err != nil {
		t.Fatalf("0013 reapply: %v", err)
	}
	if err := VerifyAdminOperationRolePermissionResultsSchema(context.Background(), gdb); err != nil {
		t.Fatalf("0013 verifier after reapply: %v", err)
	}
	var reappliedState int
	var reappliedAuth, reappliedPermissions, reappliedRole any
	if err := gdb.Raw("SELECT state,result_auth_version,result_permissions_version,result_role FROM admin_operations WHERE guid = ?", 9_120_011).Row().Scan(&reappliedState, &reappliedAuth, &reappliedPermissions, &reappliedRole); err != nil || reappliedState != 4 || reappliedAuth != nil || reappliedPermissions != nil || reappliedRole != nil {
		t.Fatalf("reapplied A08 row forged result: state=%d tuple=%#v/%#v/%#v err=%v", reappliedState, reappliedAuth, reappliedPermissions, reappliedRole, err)
	}
}

func TestAdminOperationRolePermissionResultsSchemaContractIsExact(t *testing.T) {
	contract := adminOperationRolePermissionResultsContract()
	if contract.name != "admin_operations" {
		t.Fatalf("contract table = %q", contract.name)
	}
	var names []string
	for _, column := range contract.columns {
		names = append(names, column.name)
	}
	wantOrder := "result_guid,result_auth_version,result_permissions_version,result_role,result_http_status"
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, wantOrder) {
		t.Fatalf("stable result column order = %s, want contiguous %s", joined, wantOrder)
	}
	for _, want := range []struct{ name, typ string }{{"result_permissions_version", "bigint"}, {"result_role", "int"}} {
		found := false
		for _, column := range contract.columns {
			if column.name == want.name {
				found = column.columnType == want.typ && column.nullable == "YES" && !column.defaultVal.Valid && column.extra == ""
			}
		}
		if !found {
			t.Errorf("column %s is not nullable %s without default/extra", want.name, want.typ)
		}
	}
	if len(contract.indexes) != 9 {
		t.Fatalf("0013 added or removed indexes: %#v", contract.indexes)
	}
	for _, check := range contract.checks {
		if canonical, ok := canonicalizeCheckClause(check.clause); !ok || canonical == "" {
			t.Errorf("0013 check %s is not supported by strict verifier grammar", check.name)
		}
		if check.name == "chk_admin_operations_result_role_permission" && strings.Count(strings.ToLower(check.clause), "result_role is not null") != 3 {
			t.Errorf("0013 role CHECK must reject NULL roles in every A08 success branch: %s", check.clause)
		}
	}
	if err := VerifyAdminOperationRolePermissionResultsSchema(nil, nil); err != ErrAdminOperationRolePermissionResultsSchema {
		t.Fatalf("nil verifier error = %v", err)
	}
}
