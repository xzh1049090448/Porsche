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
	complete, partial := adminResponseIntegrityColumns(db)
	if partial {
		return errAdminResponseIntegrityMigration
	}
	if !complete {
		for _, statement := range statements {
			if err := db.Exec(statement).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func adminResponseIntegrityColumns(db *gorm.DB) (complete, partial bool) {
	var responseColumns, outboxColumns int64
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='admin_operation_responses' AND column_name IN ('lifecycle_state','integrity_version','response_hmac')").Row().Scan(&responseColumns); err != nil {
		return false, true
	}
	if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='admin_action_outbox' AND column_name='failure_code'").Row().Scan(&outboxColumns); err != nil {
		return false, true
	}
	complete = responseColumns == 3 && outboxColumns == 1
	partial = !complete && (responseColumns != 0 || outboxColumns != 0)
	return complete, partial
}
