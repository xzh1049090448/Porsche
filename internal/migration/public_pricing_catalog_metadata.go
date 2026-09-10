package migration

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

var ErrPublicPricingCatalogMetadataMigration = errors.New("public pricing catalog metadata migration partial or unavailable")

// VerifyPublicPricingCatalogMetadataSchema fails closed when the frozen public
// catalog fields are absent from either mutable configuration or immutable snapshots.
func VerifyPublicPricingCatalogMetadataSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPublicPricingCatalogMetadataMigration
	}
	var count int64
	err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND ((table_name='public_model_configs' AND column_name IN ('public_display_group','endpoint_types','public_restrictions','price_source','price_reviewer','price_effective_at')) OR (table_name='public_price_snapshot_items' AND column_name IN ('pricing_type','public_display_group','endpoint_types','public_restrictions','price_source','price_reviewer','effective_at')))`).Scan(&count).Error
	if err != nil || count != 13 {
		return ErrPublicPricingCatalogMetadataMigration
	}
	return nil
}
