package models

type AdminOperationResponseLifecycle int

const (
	OperationResponseActive   AdminOperationResponseLifecycle = 1
	OperationResponseRedacted AdminOperationResponseLifecycle = 2
)

const (
	OperationResponseIntegrityLegacySealed = 0
	OperationResponseIntegrityHMACV1       = 1
)

// AdminOperationResponse is the immutable HTTP success snapshot attached to a
// terminal operation. ResponseBody is already public, canonical JSON and must
// never contain credentials, request headers, or internal database IDs.
type AdminOperationResponse struct {
	ID               int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields      `gorm:"embedded" json:"-"`
	OperationID      int64                           `gorm:"column:operation_id;type:bigint;not null" json:"-"`
	LifecycleState   AdminOperationResponseLifecycle `gorm:"column:lifecycle_state;type:int;not null;default:1" json:"-"`
	IntegrityVersion int                             `gorm:"column:integrity_version;type:int;not null;default:0" json:"-"`
	ResponseHMAC     *string                         `gorm:"column:response_hmac;type:char(64)" json:"-"`
	HTTPStatus       int                             `gorm:"column:http_status;type:int;not null" json:"-"`
	MediaType        string                          `gorm:"column:media_type;type:varchar(64);not null" json:"-"`
	ResponseBody     []byte                          `gorm:"column:response_body;type:varbinary(4096);not null" json:"-"`
	BodySHA256       string                          `gorm:"column:body_sha256;type:char(64);not null" json:"-"`
}

func (AdminOperationResponse) TableName() string { return "admin_operation_responses" }
