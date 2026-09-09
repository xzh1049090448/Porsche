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
	var currentSchema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Scan(&currentSchema).Error; err != nil || currentSchema == "" {
		return ErrPublicPriceDraftStateSchema
	}
	contract := publicContentPricingTableContract{table: publicContentPricingTable("public_price_draft_state", publicContentPricingColumns(
		publicColumn("state_key", "varchar(64)", "NO", "", "ascii", "ascii_bin"),
		publicColumn("revision", "bigint", "NO", "1", "", ""),
	), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_public_price_draft_state_guid", true, "guid"), publicIndex("uk_public_price_draft_state_key", true, "state_key"), publicIndex("idx_public_price_draft_state_revision", false, "revision", "is_deleted")})}
	metadata, ok := loadBusinessGroupTableMetadata(ctx, db, "public_price_draft_state")
	if !ok || !matchesPublicContentPricingTableContract(contract, metadata, currentSchema) {
		return ErrPublicPriceDraftStateSchema
	}
	var pricing int64
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM public_price_draft_state WHERE BINARY state_key=BINARY 'pricing' AND revision>0 AND is_deleted=0").Scan(&pricing).Error; err != nil || pricing != 1 {
		return ErrPublicPriceDraftStateSchema
	}
	return nil
}
