package models

// AdminActionDeliveryState is the stable integer persisted for outbox delivery.
// Published values must only be extended, never reordered or reused.
type AdminActionDeliveryState int

const (
	DeliveryPending   AdminActionDeliveryState = 1
	DeliveryDelivered AdminActionDeliveryState = 2
	DeliveryDead      AdminActionDeliveryState = 3
)

func (state AdminActionDeliveryState) String() string {
	switch state {
	case DeliveryPending:
		return "pending"
	case DeliveryDelivered:
		return "delivered"
	case DeliveryDead:
		return "dead"
	default:
		return "unknown"
	}
}

func ParseAdminActionDeliveryState(value string) (AdminActionDeliveryState, bool) {
	switch value {
	case "pending":
		return DeliveryPending, true
	case "delivered":
		return DeliveryDelivered, true
	case "dead":
		return DeliveryDead, true
	default:
		return 0, false
	}
}

type AdminActionOutbox struct {
	ID            int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields   `gorm:"embedded" json:"-"`
	OperationID   int64                    `gorm:"column:operation_id;type:bigint;not null" json:"-"`
	PublicRef     string                   `gorm:"column:public_ref;type:char(46);not null" json:"-"`
	Action        int                      `gorm:"column:action;type:int;not null" json:"-"`
	TargetKind    int                      `gorm:"column:target_kind;type:int;not null" json:"-"`
	TargetGUID    *int64                   `gorm:"column:target_guid;type:bigint" json:"-"`
	State         AdminOperationState      `gorm:"column:state;type:int;not null" json:"-"`
	FailureCode   *AdminOperationFailure   `gorm:"column:failure_code;type:int" json:"-"`
	ResultKind    *AdminResultKind         `gorm:"column:result_kind;type:int" json:"-"`
	ResultGUID    *int64                   `gorm:"column:result_guid;type:bigint" json:"-"`
	DeliveryState AdminActionDeliveryState `gorm:"column:delivery_state;type:int;not null;default:1" json:"-"`
	AvailableAt   int64                    `gorm:"column:available_at;type:bigint;not null" json:"-"`
	DeliveredAt   *int64                   `gorm:"column:delivered_at;type:bigint" json:"-"`
	AttemptCount  int                      `gorm:"column:attempt_count;type:int;not null;default:0" json:"-"`
}

func (AdminActionOutbox) TableName() string { return "admin_action_outbox" }
