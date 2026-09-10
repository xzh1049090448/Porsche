package migration

import (
	"context"
	"errors"
	"strings"
	"unicode"

	"gorm.io/gorm"
)

var ErrPublicRenderJobTerminalMigration = errors.New("public render job terminal migration partial or unavailable")

const publicRenderTerminalCheck = "((last_terminal_owner_hmac is null and last_terminal_fence is null and last_terminal_operation is null and last_terminal_state is null) or (last_terminal_owner_hmac is not null and last_terminal_fence > 0 and last_terminal_operation in (1,2) and last_terminal_state in (1,3,4)))"

func VerifyPublicRenderJobTerminalSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPublicRenderJobTerminalMigration
	}
	var count int64
	err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='public_render_jobs' AND column_default IS NULL AND ((column_name='last_terminal_owner_hmac' AND column_type='char(64)' AND data_type='char' AND is_nullable='YES' AND character_set_name='ascii' AND collation_name='ascii_bin') OR (column_name IN ('last_terminal_fence','last_terminal_operation','last_terminal_state') AND column_type='int' AND data_type='int' AND is_nullable='YES' AND character_set_name IS NULL AND collation_name IS NULL))`).Scan(&count).Error
	if err != nil || count != 4 {
		return ErrPublicRenderJobTerminalMigration
	}
	var clauses []string
	err = db.WithContext(ctx).Raw(`SELECT cc.check_clause FROM information_schema.check_constraints cc JOIN information_schema.table_constraints tc ON tc.constraint_schema=cc.constraint_schema AND tc.constraint_name=cc.constraint_name AND tc.table_name='public_render_jobs' WHERE cc.constraint_schema=DATABASE() AND cc.constraint_name='chk_public_render_jobs_terminal' AND tc.constraint_type='CHECK'`).Scan(&clauses).Error
	if err != nil || len(clauses) != 1 || normalizePublicRenderTerminalCheck(clauses[0]) != normalizePublicRenderTerminalCheck(publicRenderTerminalCheck) {
		return ErrPublicRenderJobTerminalMigration
	}
	return nil
}

func normalizePublicRenderTerminalCheck(value string) string {
	value = strings.ToLower(strings.ReplaceAll(value, "`", ""))
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
}
