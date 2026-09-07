package migration

import (
	"errors"

	"gorm.io/gorm"
)

var ErrAdminOperationResponseTargetMigration = errors.New("admin operation response target migration partial or unavailable")

type adminOperationResponseTargetStage int

const (
	adminOperationResponseTargetBefore adminOperationResponseTargetStage = iota
	adminOperationResponseTargetColumn
	adminOperationResponseTargetComplete
)

// applyAdminOperationResponseTargetsMigration resumes only after a committed
// statement boundary. The data backfill and fail-safe redaction are
// intentionally rerunnable because MySQL DDL implicitly commits.
func applyAdminOperationResponseTargetsMigration(db *gorm.DB, sqlBytes []byte) error {
	if db == nil {
		return ErrAdminOperationResponseTargetMigration
	}
	statements := splitStatements(string(sqlBytes))
	if len(statements) != 4 {
		return ErrAdminOperationResponseTargetMigration
	}
	stage, err := probeAdminOperationResponseTargetStage(db)
	if err != nil {
		return err
	}
	if stage == adminOperationResponseTargetBefore {
		if err := db.Exec(statements[0]).Error; err != nil {
			return err
		}
		stage, err = probeAdminOperationResponseTargetStage(db)
		if err != nil || stage != adminOperationResponseTargetColumn {
			return ErrAdminOperationResponseTargetMigration
		}
	}
	if stage == adminOperationResponseTargetColumn {
		for _, statement := range statements[1:] {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
		stage, err = probeAdminOperationResponseTargetStage(db)
		if err != nil || stage != adminOperationResponseTargetComplete {
			return ErrAdminOperationResponseTargetMigration
		}
	}
	if stage != adminOperationResponseTargetComplete {
		return ErrAdminOperationResponseTargetMigration
	}
	return nil
}

func probeAdminOperationResponseTargetStage(db *gorm.DB) (adminOperationResponseTargetStage, error) {
	var targetColumn, targetIndex, targetForeignKey int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND column_name='target_guid'").Row().Scan(&targetColumn); err != nil {
		return 0, ErrAdminOperationResponseTargetMigration
	}
	if err := db.Raw("SELECT COUNT(DISTINCT index_name) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND index_name='idx_admin_operation_responses_target'").Row().Scan(&targetIndex); err != nil {
		return 0, ErrAdminOperationResponseTargetMigration
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND constraint_type='FOREIGN KEY' AND constraint_name='fk_admin_operation_responses_target'").Row().Scan(&targetForeignKey); err != nil {
		return 0, ErrAdminOperationResponseTargetMigration
	}
	switch {
	case targetColumn == 0 && targetIndex == 0 && targetForeignKey == 0:
		return adminOperationResponseTargetBefore, nil
	case targetColumn == 1 && targetIndex == 0 && targetForeignKey == 0:
		return adminOperationResponseTargetColumn, nil
	case targetColumn == 1 && targetIndex == 1 && targetForeignKey == 1:
		return adminOperationResponseTargetComplete, nil
	default:
		return 0, ErrAdminOperationResponseTargetMigration
	}
}
