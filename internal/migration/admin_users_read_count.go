package migration

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// VerifyAdminUsersReadCountIndex fails closed if the immutable 0004 index
// contract is missing, reordered, unique, or contains extra columns.
func VerifyAdminUsersReadCountIndex(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("verify admin users read count index requires database")
	}
	result, err := db.WithContext(ctx).Raw(`SELECT column_name, seq_in_index, non_unique
FROM information_schema.statistics
WHERE table_schema = DATABASE() AND table_name = 'users' AND index_name = 'idx_users_admin_read_count'
	ORDER BY seq_in_index`).Rows()
	if err != nil {
		return fmt.Errorf("verify admin users read count index: %w", err)
	}
	defer result.Close()
	type indexColumn struct {
		name      string
		sequence  int
		nonUnique int
	}
	var rows []indexColumn
	for result.Next() {
		var row indexColumn
		if err := result.Scan(&row.name, &row.sequence, &row.nonUnique); err != nil {
			return fmt.Errorf("verify admin users read count index: %w", err)
		}
		rows = append(rows, row)
	}
	if err := result.Err(); err != nil {
		return fmt.Errorf("verify admin users read count index: %w", err)
	}
	want := []string{"is_deleted", "role", "status"}
	if len(rows) != len(want) {
		return fmt.Errorf("admin users read count index has invalid column count")
	}
	for i, row := range rows {
		if row.sequence != i+1 || row.name != want[i] || row.nonUnique != 1 {
			return fmt.Errorf("admin users read count index contract mismatch")
		}
	}
	return nil
}
