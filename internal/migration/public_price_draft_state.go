package migration

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var ErrPublicPriceDraftStateSchema = errors.New("public price draft state schema verification failed")

func VerifyPublicPriceDraftStateSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPublicPriceDraftStateSchema
	}
	var tables, columns, pricing int64
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name='public_price_draft_state' AND engine='InnoDB' AND table_collation='utf8mb4_unicode_ci'").Scan(&tables).Error; err != nil {
		return ErrPublicPriceDraftStateSchema
	}
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='public_price_draft_state'").Scan(&columns).Error; err != nil {
		return ErrPublicPriceDraftStateSchema
	}
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM public_price_draft_state WHERE BINARY state_key=BINARY 'pricing' AND revision>0 AND is_deleted=0").Scan(&pricing).Error; err != nil {
		return ErrPublicPriceDraftStateSchema
	}
	var guidKey, keyKey, revisionIndex, checkConstraint int64
	qIndex := "SELECT COUNT(DISTINCT index_name) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='public_price_draft_state' AND index_name=?"
	for name, dest := range map[string]*int64{"uk_public_price_draft_state_guid": &guidKey, "uk_public_price_draft_state_key": &keyKey, "idx_public_price_draft_state_revision": &revisionIndex} {
		if err := db.WithContext(ctx).Raw(qIndex, name).Scan(dest).Error; err != nil {
			return ErrPublicPriceDraftStateSchema
		}
	}
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM information_schema.table_constraints WHERE constraint_schema=DATABASE() AND table_name='public_price_draft_state' AND constraint_name='chk_public_price_draft_state_values' AND constraint_type='CHECK'").Scan(&checkConstraint).Error; err != nil {
		return ErrPublicPriceDraftStateSchema
	}
	if tables != 1 || columns != 9 || pricing != 1 || guidKey != 1 || keyKey != 1 || revisionIndex != 1 || checkConstraint != 1 {
		return ErrPublicPriceDraftStateSchema
	}
	return nil
}
