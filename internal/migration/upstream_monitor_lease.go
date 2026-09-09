package migration

import (
	"context"
	"errors"
	"gorm.io/gorm"
)

var ErrUpstreamMonitorLeaseSchema = errors.New("upstream monitor lease schema mismatch or unavailable")

func VerifyUpstreamMonitorLeaseSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrUpstreamMonitorLeaseSchema
	}
	var schema string
	if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Scan(&schema).Error; err != nil || schema == "" {
		return ErrUpstreamMonitorLeaseSchema
	}
	contract := publicContentPricingTable("upstream_monitor_leases", publicContentPricingColumns(
		publicColumn("lease_key", "varchar(64)", "NO", "", "ascii", "ascii_bin"),
		publicColumn("owner_token", "char(64)", "YES", "", "ascii", "ascii_bin"),
		publicColumn("lease_expires_at", "bigint", "NO", "0", "", ""),
		publicColumn("revision", "bigint", "NO", "1", "", ""),
	), []businessGroupIndexContract{publicPrimary(), publicIndex("uk_upstream_monitor_leases_guid", true, "guid"), publicIndex("uk_upstream_monitor_leases_key", true, "lease_key"), publicIndex("idx_upstream_monitor_leases_expiry", false, "lease_expires_at", "is_deleted")})
	contract.checks = []businessGroupCheckContract{{name: "chk_upstream_monitor_leases_values", clause: "lease_expires_at >= 0 AND revision > 0 AND is_deleted IN (0, 1)", enforced: "YES"}}
	metadata, ok := loadBusinessGroupTableMetadata(ctx, db, contract.name)
	if !ok || !matchesBusinessGroupTableContract(contract, metadata) {
		return ErrUpstreamMonitorLeaseSchema
	}
	var rows int64
	if err := db.WithContext(ctx).Raw("SELECT COUNT(*) FROM upstream_monitor_leases WHERE BINARY lease_key=BINARY 'catalog' AND is_deleted=0").Scan(&rows).Error; err != nil || rows != 1 {
		return ErrUpstreamMonitorLeaseSchema
	}
	return nil
}
