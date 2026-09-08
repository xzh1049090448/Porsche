package migration

import (
	"errors"

	"gorm.io/gorm"
)

var errAdminResponseIntegrityMigration = errors.New("admin response integrity migration is partial or invalid")

func applyAdminResponseIntegrityMigration(db *gorm.DB, raw []byte) error {
	if db == nil || len(raw) == 0 {
		return errAdminResponseIntegrityMigration
	}
	statements := splitStatements(string(raw))
	if len(statements) != 4 {
		return errAdminResponseIntegrityMigration
	}
	stage, err := adminResponseIntegrityStage(db)
	if err != nil {
		return errAdminResponseIntegrityMigration
	}
	if stage == 0 {
		if err := db.Exec(statements[0]).Error; err != nil {
			return err
		}
		if stage, err = adminResponseIntegrityStage(db); err != nil || stage != 1 {
			return errAdminResponseIntegrityMigration
		}
	}
	if stage == 1 {
		if err := db.Exec(statements[1]).Error; err != nil {
			return err
		}
		if stage, err = adminResponseIntegrityStage(db); err != nil || stage != 2 {
			return errAdminResponseIntegrityMigration
		}
	}
	// The backfill is deliberately idempotent. MySQL exposes no durable marker
	// between its committed UPDATE and the following CHECK DDL, so every resume
	// with the outcome column present repeats the bounded NULL-only update.
	if err := db.Exec(statements[2]).Error; err != nil {
		return err
	}
	if stage == 2 {
		if err := db.Exec(statements[3]).Error; err != nil {
			return err
		}
		if stage, err = adminResponseIntegrityStage(db); err != nil || stage != 3 {
			return errAdminResponseIntegrityMigration
		}
	}
	if stage != 3 {
		return errAdminResponseIntegrityMigration
	}
	return nil
}

// adminResponseIntegrityStage recognizes only prefixes that the four-statement
// migration can commit. Any other mixture is schema drift and remains closed.
func adminResponseIntegrityStage(db *gorm.DB) (int, error) {
	if db == nil {
		return -1, errAdminResponseIntegrityMigration
	}
	var responseColumns, immutableCheck, lifecycleCheck, outboxColumn, outcomeCheck int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND column_name IN ('lifecycle_state','integrity_version','response_hmac')").Row().Scan(&responseColumns); err != nil {
		return -1, err
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND constraint_type='CHECK' AND constraint_name='chk_admin_operation_responses_immutable'").Row().Scan(&immutableCheck); err != nil {
		return -1, err
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND constraint_type='CHECK' AND constraint_name='chk_admin_operation_responses_lifecycle'").Row().Scan(&lifecycleCheck); err != nil {
		return -1, err
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='admin_action_outbox' AND column_name='failure_code'").Row().Scan(&outboxColumn); err != nil {
		return -1, err
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.table_constraints WHERE table_schema=DATABASE() AND table_name='admin_action_outbox' AND constraint_type='CHECK' AND constraint_name='chk_admin_action_outbox_outcome'").Row().Scan(&outcomeCheck); err != nil {
		return -1, err
	}
	switch {
	case responseColumns == 0 && immutableCheck == 1 && lifecycleCheck == 0 && outboxColumn == 0 && outcomeCheck == 0:
		return 0, nil
	case responseColumns == 3 && immutableCheck == 0 && lifecycleCheck == 1 && outboxColumn == 0 && outcomeCheck == 0:
		return 1, nil
	case responseColumns == 3 && immutableCheck == 0 && lifecycleCheck == 1 && outboxColumn == 1 && outcomeCheck == 0:
		return 2, nil
	case responseColumns == 3 && immutableCheck == 0 && lifecycleCheck == 1 && outboxColumn == 1 && outcomeCheck == 1:
		return 3, nil
	default:
		return -1, errAdminResponseIntegrityMigration
	}
}
