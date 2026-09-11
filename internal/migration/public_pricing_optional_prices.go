package migration

import (
	"context"
	"errors"
	"gorm.io/gorm"
)

var ErrPublicPricingOptionalPricesMigration = errors.New("public pricing optional prices migration partial or unavailable")

func VerifyPublicPricingOptionalPricesSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPublicPricingOptionalPricesMigration
	}
	var count int64
	err := db.WithContext(ctx).Raw(`SELECT COUNT(*) FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='public_price_snapshot_items' AND column_name IN ('input_price_usd_per_million_tokens','output_price_usd_per_million_tokens') AND column_type='decimal(20,8)' AND is_nullable='YES' AND column_default IS NULL AND character_set_name IS NULL AND collation_name IS NULL`).Scan(&count).Error
	if err != nil || count != 2 {
		return ErrPublicPricingOptionalPricesMigration
	}
	return nil
}
