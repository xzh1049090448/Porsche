package migration

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var ErrAdminOperationRolePermissionResultsSchema = errors.New("admin operation role permission result schema mismatch or unavailable")

const adminOperationResultAuthVersion0012Check = "(state = 2 AND action = 2 AND result_kind = 2 AND result_guid IS NOT NULL AND result_auth_version IS NOT NULL AND result_auth_version > 0) OR (state = 2 AND action IN (3, 4, 5) AND result_kind = 2 AND result_guid IS NOT NULL AND result_auth_version IS NOT NULL AND result_auth_version > 0) OR ((state IN (1, 3, 4, 5) OR (state = 2 AND (action < 2 OR action > 5))) AND result_auth_version IS NULL)"

const adminOperationResultRolePermission0012Check = "(state = 2 AND action = 3 AND result_kind = 2 AND result_guid IS NOT NULL AND result_permissions_version IS NOT NULL AND result_permissions_version > 0 AND result_role IS NOT NULL AND result_role = 10) OR (state = 2 AND action = 4 AND result_kind = 2 AND result_guid IS NOT NULL AND result_permissions_version IS NOT NULL AND result_permissions_version > 0 AND result_role IS NOT NULL AND result_role = 1) OR (state = 2 AND action = 5 AND result_kind = 2 AND result_guid IS NOT NULL AND result_permissions_version IS NOT NULL AND result_permissions_version > 0 AND result_role IS NOT NULL AND result_role = 10) OR ((state IN (1, 3, 4, 5) OR (state = 2 AND (action < 3 OR action > 5))) AND result_permissions_version IS NULL AND result_role IS NULL)"

func adminOperationRolePermissionResultsContracts() []adminOperationTableContract {
	contracts := adminOperationSafetyContracts()
	for i := range contracts {
		if contracts[i].name != "admin_operations" {
			continue
		}
		columns := make([]adminOperationColumnContract, 0, len(contracts[i].columns)+2)
		for _, column := range contracts[i].columns {
			columns = append(columns, column)
			if column.name == "result_auth_version" {
				columns = append(columns,
					nullableColumn("result_permissions_version", "bigint"),
					nullableColumn("result_role", "int"),
				)
			}
		}
		contracts[i].columns = columns
		for checkIndex := range contracts[i].checks {
			if contracts[i].checks[checkIndex].name == "chk_admin_operations_result_auth_version" {
				contracts[i].checks[checkIndex].clause = adminOperationResultAuthVersion0012Check
			}
		}
		contracts[i].checks = append(contracts[i].checks, adminOperationCheckContract{
			name: "chk_admin_operations_result_role_permission", clause: adminOperationResultRolePermission0012Check, enforced: "YES",
		})
	}
	return contracts
}

func adminOperationRolePermissionResultsContract() adminOperationTableContract {
	for _, contract := range adminOperationRolePermissionResultsContracts() {
		if contract.name == "admin_operations" {
			return contract
		}
	}
	return adminOperationTableContract{}
}

// VerifyAdminOperationRolePermissionResultsSchema checks the complete operation
// safety schema after migration 0012, including exact column order and the
// unchanged index, foreign-key, and CHECK sets.
func VerifyAdminOperationRolePermissionResultsSchema(ctx context.Context, db *gorm.DB) error {
	if ctx == nil || db == nil {
		return ErrAdminOperationRolePermissionResultsSchema
	}
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || currentSchema == "" {
		return ErrAdminOperationRolePermissionResultsSchema
	}
	for _, contract := range adminOperationRolePermissionResultsContracts() {
		if !verifyAdminOperationTable(ctx, db, currentSchema, contract) {
			return ErrAdminOperationRolePermissionResultsSchema
		}
	}
	return nil
}
