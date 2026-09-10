package migration

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var ErrPublicRenderJobTerminalMigration = errors.New("public render job terminal migration partial or unavailable")

func VerifyPublicRenderJobTerminalSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPublicRenderJobTerminalMigration
	}
	var count int64
	err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='public_render_jobs' AND ((column_name='last_terminal_owner_hmac' AND column_type='char(64)' AND is_nullable='YES' AND character_set_name='ascii' AND collation_name='ascii_bin') OR (column_name IN ('last_terminal_fence','last_terminal_operation','last_terminal_state') AND column_type='int' AND is_nullable='YES'))`).Scan(&count).Error
	if err != nil || count != 4 {
		return ErrPublicRenderJobTerminalMigration
	}
	if err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND table_name='public_render_jobs' AND constraint_name='chk_public_render_jobs_terminal' AND constraint_type='CHECK'`).Scan(&count).Error; err != nil || count != 1 {
		return ErrPublicRenderJobTerminalMigration
	}
	return nil
}
