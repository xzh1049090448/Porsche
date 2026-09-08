package migration

import (
	"context"
	"errors"
	"gorm.io/gorm"
	"strings"
)

var ErrPermissionSchema = errors.New("permission schema mismatch or unavailable")

type permissionColumn struct {
	Name     string `gorm:"column:column_name"`
	Type     string `gorm:"column:data_type"`
	FullType string `gorm:"column:column_type"`
	Nullable string `gorm:"column:is_nullable"`
	Default  string `gorm:"column:column_default"`
	Extra    string `gorm:"column:extra"`
}
type permissionIndex struct {
	Column    string `gorm:"column:column_name"`
	NonUnique int    `gorm:"column:non_unique"`
}

func VerifyPermissionSchema(ctx context.Context, db *gorm.DB) error {
	if db == nil {
		return ErrPermissionSchema
	}
	for _, table := range []string{"user_permission_heads", "user_permission_overrides"} {
		var engine string
		if err := db.WithContext(ctx).Raw("SELECT engine FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", table).Row().Scan(&engine); err != nil || engine != "InnoDB" {
			return ErrPermissionSchema
		}
		expected := map[string]string{"id": "bigint", "guid": "bigint", "user_id": "bigint", "created_at": "bigint", "created_by": "bigint", "updated_at": "bigint", "updated_by": "bigint", "is_deleted": "int", "policy_version": "bigint"}
		if table == "user_permission_heads" {
			expected["catalog_version"] = "int"
			expected["rule_count"] = "int"
		} else {
			expected["capability"] = "int"
			expected["effect"] = "int"
		}
		var cols []permissionColumn
		if err := db.WithContext(ctx).Raw("SELECT column_name AS column_name,data_type AS data_type,column_type AS column_type,is_nullable AS is_nullable,COALESCE(column_default, '') AS column_default,extra AS extra FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=?", table).Scan(&cols).Error; err != nil || len(cols) != len(expected) {
			return ErrPermissionSchema
		}
		for _, c := range cols {
			want, ok := expected[c.Name]
			nullable := "NO"
			if c.Name == "created_by" || c.Name == "updated_by" {
				nullable = "YES"
			}
			if !ok || c.Type != want || c.Nullable != nullable || strings.Contains(strings.ToLower(c.FullType), "unsigned") || (c.Name == "id" && !strings.Contains(c.Extra, "auto_increment")) || (c.Name == "is_deleted" && c.Default != "0") {
				return ErrPermissionSchema
			}
		}
		indexes := map[string]struct {
			columns string
			unique  bool
		}{"PRIMARY": {"id", true}}
		fk := "fk_permission_heads_user"
		if table == "user_permission_heads" {
			indexes["uk_permission_heads_guid"] = struct {
				columns string
				unique  bool
			}{"guid", true}
			indexes["uk_permission_heads_user"] = struct {
				columns string
				unique  bool
			}{"user_id", true}
			indexes["idx_permission_heads_active"] = struct {
				columns string
				unique  bool
			}{"is_deleted,updated_at", false}
		} else {
			fk = "fk_permission_overrides_user"
			indexes["uk_permission_overrides_guid"] = struct {
				columns string
				unique  bool
			}{"guid", true}
			indexes["uk_permission_overrides_version_cap"] = struct {
				columns string
				unique  bool
			}{"user_id,policy_version,capability", true}
			indexes["idx_permission_overrides_active"] = struct {
				columns string
				unique  bool
			}{"user_id,is_deleted,policy_version", false}
		}
		for name, want := range indexes {
			var rows []permissionIndex
			if err := db.WithContext(ctx).Raw("SELECT column_name AS column_name,non_unique AS non_unique FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND index_name=? ORDER BY seq_in_index", table, name).Scan(&rows).Error; err != nil || len(rows) == 0 {
				return ErrPermissionSchema
			}
			names := make([]string, 0, len(rows))
			for _, r := range rows {
				if (r.NonUnique == 0) != want.unique {
					return ErrPermissionSchema
				}
				names = append(names, r.Column)
			}
			if strings.Join(names, ",") != want.columns {
				return ErrPermissionSchema
			}
		}
		var foreign []struct {
			Column     string `gorm:"column:column_name"`
			Schema     string `gorm:"column:referenced_table_schema"`
			Table      string `gorm:"column:referenced_table_name"`
			Ref        string `gorm:"column:referenced_column_name"`
			DeleteRule string `gorm:"column:delete_rule"`
			UpdateRule string `gorm:"column:update_rule"`
		}
		q := `SELECT k.column_name AS column_name,k.referenced_table_schema AS referenced_table_schema,k.referenced_table_name AS referenced_table_name,k.referenced_column_name AS referenced_column_name,r.delete_rule AS delete_rule,r.update_rule AS update_rule FROM information_schema.key_column_usage k JOIN information_schema.referential_constraints r ON r.constraint_schema=k.constraint_schema AND r.table_name=k.table_name AND r.constraint_name=k.constraint_name WHERE k.table_schema=DATABASE() AND k.table_name=? AND k.constraint_name=?`
		if err := db.WithContext(ctx).Raw(q, table, fk).Scan(&foreign).Error; err != nil || len(foreign) != 1 || foreign[0].Column != "user_id" || foreign[0].Schema == "" || foreign[0].Table != "users" || foreign[0].Ref != "id" || (foreign[0].DeleteRule != "RESTRICT" && foreign[0].DeleteRule != "NO ACTION") || (foreign[0].UpdateRule != "RESTRICT" && foreign[0].UpdateRule != "NO ACTION") {
			return ErrPermissionSchema
		}
		var currentSchema string
		if err := db.WithContext(ctx).Raw("SELECT DATABASE()").Row().Scan(&currentSchema); err != nil || foreign[0].Schema != currentSchema {
			return ErrPermissionSchema
		}
	}
	return nil
}
