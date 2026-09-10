package migration

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var ErrPublicPricingCatalogMetadataMigration = errors.New("public pricing catalog metadata migration partial or unavailable")

func applyPublicPricingCatalogMetadataMigration(db *gorm.DB, sql []byte) error {
	if db == nil {
		return ErrPublicPricingCatalogMetadataMigration
	}
	statements := splitStatements(string(sql))
	steps := []struct {
		table   string
		columns []string
	}{
		{"public_model_configs", []string{"public_display_group", "endpoint_types", "public_restrictions", "price_source", "price_reviewer", "price_effective_at"}},
		{"public_price_snapshot_items", []string{"pricing_type", "public_display_group", "endpoint_types", "public_restrictions", "price_source", "price_reviewer", "effective_at"}},
	}
	if len(statements) != len(steps) {
		return fmt.Errorf("invalid statement count")
	}
	for i, step := range steps {
		var count int64
		if err := db.Raw("SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name IN ?", step.table, step.columns).Scan(&count).Error; err != nil {
			return err
		}
		switch count {
		case 0:
			if err := db.Exec(statements[i]).Error; err != nil {
				return err
			}
		case int64(len(step.columns)):
			// A prior unledgered attempt completed this atomic ALTER. The final
			// exact verifier below decides whether its shape is trustworthy.
		default:
			return ErrPublicPricingCatalogMetadataMigration
		}
	}
	return VerifyPublicPricingCatalogMetadataSchema(context.Background(), db)
}

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
