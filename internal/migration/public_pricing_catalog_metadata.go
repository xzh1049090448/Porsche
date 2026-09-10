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
	query := `SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND (
(table_name='public_model_configs' AND (
(column_name='public_display_group' AND column_type='varchar(128)' AND is_nullable='NO' AND column_default='' AND character_set_name='utf8mb4' AND collation_name='utf8mb4_unicode_ci') OR
(column_name IN ('endpoint_types','public_restrictions') AND column_type='json' AND is_nullable='NO' AND column_default='json_array()' AND character_set_name IS NULL AND collation_name IS NULL) OR
(column_name='price_source' AND column_type='varchar(255)' AND is_nullable='NO' AND column_default='' AND character_set_name='utf8mb4' AND collation_name='utf8mb4_unicode_ci') OR
(column_name='price_reviewer' AND column_type='varchar(128)' AND is_nullable='NO' AND column_default='' AND character_set_name='utf8mb4' AND collation_name='utf8mb4_unicode_ci') OR
(column_name='price_effective_at' AND column_type='bigint' AND is_nullable='YES' AND column_default IS NULL AND character_set_name IS NULL AND collation_name IS NULL))) OR
(table_name='public_price_snapshot_items' AND (
(column_name='pricing_type' AND column_type='varchar(32)' AND is_nullable='NO' AND column_default='token' AND character_set_name='utf8mb4' AND collation_name='utf8mb4_unicode_ci') OR
(column_name='public_display_group' AND column_type='varchar(128)' AND is_nullable='NO' AND column_default='' AND character_set_name='utf8mb4' AND collation_name='utf8mb4_unicode_ci') OR
(column_name IN ('endpoint_types','public_restrictions') AND column_type='json' AND is_nullable='NO' AND column_default='json_array()' AND character_set_name IS NULL AND collation_name IS NULL) OR
(column_name='price_source' AND column_type='varchar(255)' AND is_nullable='NO' AND column_default='' AND character_set_name='utf8mb4' AND collation_name='utf8mb4_unicode_ci') OR
(column_name='price_reviewer' AND column_type='varchar(128)' AND is_nullable='NO' AND column_default='' AND character_set_name='utf8mb4' AND collation_name='utf8mb4_unicode_ci') OR
(column_name='effective_at' AND column_type='bigint' AND is_nullable='YES' AND column_default IS NULL AND character_set_name IS NULL AND collation_name IS NULL))))`
	if err := db.WithContext(ctx).Raw(query).Scan(&count).Error; err != nil || count != 13 {
		return ErrPublicPricingCatalogMetadataMigration
	}
	return nil
}
