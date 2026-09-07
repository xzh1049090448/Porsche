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
	adminOperationResponseTargetColumns
	adminOperationResponseTargetOutboxComplete
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
	if len(statements) != 9 {
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
		if err := db.Exec(statements[1]).Error; err != nil {
			return err
		}
		stage, err = probeAdminOperationResponseTargetStage(db)
		if err != nil || stage != adminOperationResponseTargetColumns {
			return ErrAdminOperationResponseTargetMigration
		}
	}
	if stage == adminOperationResponseTargetColumns {
		for _, statement := range statements[2:8] {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
		stage, err = probeAdminOperationResponseTargetStage(db)
		if err != nil || stage != adminOperationResponseTargetOutboxComplete {
			return ErrAdminOperationResponseTargetMigration
		}
	}
	if stage == adminOperationResponseTargetOutboxComplete {
		if err := db.Exec(statements[8]).Error; err != nil {
			return err
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
	var targetColumn, resultKindColumn, targetIndex, targetForeignKey, resultKindOutcomeCheck int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND column_name='target_guid'").Row().Scan(&targetColumn); err != nil {
		return 0, ErrAdminOperationResponseTargetMigration
	}
	if err := db.Raw("SELECT COUNT(DISTINCT index_name) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND index_name='idx_admin_operation_responses_target'").Row().Scan(&targetIndex); err != nil {
		return 0, ErrAdminOperationResponseTargetMigration
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND constraint_type='FOREIGN KEY' AND constraint_name='fk_admin_operation_responses_target'").Row().Scan(&targetForeignKey); err != nil {
		return 0, ErrAdminOperationResponseTargetMigration
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='admin_action_outbox' AND column_name='result_kind'").Row().Scan(&resultKindColumn); err != nil {
		return 0, ErrAdminOperationResponseTargetMigration
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.table_constraints tc JOIN information_schema.check_constraints cc ON cc.constraint_schema=tc.constraint_schema AND cc.constraint_name=tc.constraint_name WHERE tc.table_schema=DATABASE() AND tc.table_name='admin_action_outbox' AND tc.constraint_type='CHECK' AND tc.constraint_name='chk_admin_action_outbox_outcome' AND LOWER(cc.check_clause) LIKE '%result_kind%'").Row().Scan(&resultKindOutcomeCheck); err != nil {
		return 0, ErrAdminOperationResponseTargetMigration
	}
	switch {
	case targetColumn == 0 && resultKindColumn == 0 && targetIndex == 0 && targetForeignKey == 0 && resultKindOutcomeCheck == 0:
		return adminOperationResponseTargetBefore, nil
	case targetColumn == 1 && resultKindColumn == 0 && targetIndex == 0 && targetForeignKey == 0 && resultKindOutcomeCheck == 0:
		return adminOperationResponseTargetColumn, nil
	case targetColumn == 1 && resultKindColumn == 1 && targetIndex == 0 && targetForeignKey == 0 && resultKindOutcomeCheck == 0:
		return adminOperationResponseTargetColumns, nil
	case targetColumn == 1 && resultKindColumn == 1 && targetIndex == 0 && targetForeignKey == 0 && resultKindOutcomeCheck == 1:
		return adminOperationResponseTargetOutboxComplete, nil
	case targetColumn == 1 && resultKindColumn == 1 && targetIndex == 1 && targetForeignKey == 1 && resultKindOutcomeCheck == 1:
		return adminOperationResponseTargetComplete, nil
	default:
		return 0, ErrAdminOperationResponseTargetMigration
	}
}
