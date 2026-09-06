package models

// BusinessGroupStatus is the stable integer-backed lifecycle state stored in
// business_groups.status.
type BusinessGroupStatus int

const (
	BusinessGroupStatusActive   BusinessGroupStatus = 1
	BusinessGroupStatusInactive BusinessGroupStatus = 2
)

func (status BusinessGroupStatus) String() string {
	switch status {
	case BusinessGroupStatusActive:
		return "active"
	case BusinessGroupStatusInactive:
		return "inactive"
	default:
		return "unknown"
	}
}

// ParseBusinessGroupStatus converts the API-safe name to its stable stored
// integer. Unknown values are rejected rather than persisted implicitly.
func ParseBusinessGroupStatus(value string) (BusinessGroupStatus, bool) {
	switch value {
	case "active":
		return BusinessGroupStatusActive, true
	case "inactive":
		return BusinessGroupStatusInactive, true
	default:
		return 0, false
	}
}

// BusinessGroup is the persisted grouping entity. ID is restricted to
// internal relations; Guid and Key are stable business identifiers.
type BusinessGroup struct {
	ID          int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields `gorm:"embedded" json:"-"`
	Key         string              `gorm:"column:group_key;type:varchar(64);not null;<-:create" json:"group_key"`
	DisplayName string              `gorm:"column:display_name;type:varchar(64);not null" json:"display_name"`
	Status      BusinessGroupStatus `gorm:"column:status;type:int;not null" json:"status"`
}

func (BusinessGroup) TableName() string { return "business_groups" }
