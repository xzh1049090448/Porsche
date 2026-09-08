package models

// AdminOperationState is the stable integer persisted for an administrative
// operation. Published values must only be extended, never reordered or reused.
type AdminOperationState int

const (
	OperationProcessing      AdminOperationState = 1
	OperationSucceeded       AdminOperationState = 2
	OperationFailed          AdminOperationState = 3
	OperationPendingRecovery AdminOperationState = 4
	OperationExpired         AdminOperationState = 5
)

func (state AdminOperationState) String() string {
	switch state {
	case OperationProcessing:
		return "processing"
	case OperationSucceeded:
		return "succeeded"
	case OperationFailed:
		return "failed"
	case OperationPendingRecovery:
		return "pending_recovery"
	case OperationExpired:
		return "expired"
	default:
		return "unknown"
	}
}

func ParseAdminOperationState(value string) (AdminOperationState, bool) {
	switch value {
	case "processing":
		return OperationProcessing, true
	case "succeeded":
		return OperationSucceeded, true
	case "failed":
		return OperationFailed, true
	case "pending_recovery":
		return OperationPendingRecovery, true
	case "expired":
		return OperationExpired, true
	default:
		return 0, false
	}
}

// CanTransitionTo reports whether the design permits a one-way persisted state
// transition. Time, lease, and authorization preconditions belong to services.
func (state AdminOperationState) CanTransitionTo(next AdminOperationState) bool {
	switch state {
	case OperationProcessing:
		return next == OperationSucceeded || next == OperationFailed || next == OperationPendingRecovery
	case OperationSucceeded, OperationFailed, OperationPendingRecovery:
		return next == OperationExpired
	default:
		return false
	}
}

// AdminOperationFailure is the stable, foundational failure classification.
// Action-specific values begin at 1000 and are added with their consumers.
type AdminOperationFailure int

const (
	FailureActionRejected        AdminOperationFailure = 1
	FailureTargetVersionConflict AdminOperationFailure = 2
	FailurePolicyVersionConflict AdminOperationFailure = 3
	FailureTargetStateConflict   AdminOperationFailure = 4
	FailureConsumerValidation    AdminOperationFailure = 5
)

func (failure AdminOperationFailure) String() string {
	switch failure {
	case FailureActionRejected:
		return "action_rejected"
	case FailureTargetVersionConflict:
		return "target_version_conflict"
	case FailurePolicyVersionConflict:
		return "policy_version_conflict"
	case FailureTargetStateConflict:
		return "target_state_conflict"
	case FailureConsumerValidation:
		return "consumer_validation_failed"
	default:
		return "unknown"
	}
}

func ParseAdminOperationFailure(value string) (AdminOperationFailure, bool) {
	switch value {
	case "action_rejected":
		return FailureActionRejected, true
	case "target_version_conflict":
		return FailureTargetVersionConflict, true
	case "policy_version_conflict":
		return FailurePolicyVersionConflict, true
	case "target_state_conflict":
		return FailureTargetStateConflict, true
	case "consumer_validation_failed":
		return FailureConsumerValidation, true
	default:
		return 0, false
	}
}

// AdminResultKind identifies the optional business result without coupling the
// persistence model to an action-specific DTO.
type AdminResultKind int

const (
	ResultNone          AdminResultKind = 1
	ResultUser          AdminResultKind = 2
	ResultPublicContent AdminResultKind = 3
)

func (kind AdminResultKind) String() string {
	switch kind {
	case ResultNone:
		return "none"
	case ResultUser:
		return "user"
	case ResultPublicContent:
		return "public_content"
	default:
		return "unknown"
	}
}

func ParseAdminResultKind(value string) (AdminResultKind, bool) {
	switch value {
	case "none":
		return ResultNone, true
	case "user":
		return ResultUser, true
	case "public_content":
		return ResultPublicContent, true
	default:
		return 0, false
	}
}

// AdminActionVerificationStatus is derived from stored verification fields and
// is not persisted as a separate column.
type AdminActionVerificationStatus string

const (
	VerificationActive   AdminActionVerificationStatus = "active"
	VerificationConsumed AdminActionVerificationStatus = "consumed"
	VerificationExpired  AdminActionVerificationStatus = "expired"
)

type AdminActionVerification struct {
	ID               int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields      `gorm:"embedded" json:"-"`
	ActorUserID      int64  `gorm:"column:actor_user_id;type:bigint;not null" json:"-"`
	ActorAuthVersion int    `gorm:"column:actor_auth_version;type:int;not null" json:"-"`
	SessionID        int64  `gorm:"column:session_id;type:bigint;not null" json:"-"`
	Action           int    `gorm:"column:action;type:int;not null" json:"-"`
	TargetKind       int    `gorm:"column:target_kind;type:int;not null" json:"-"`
	TargetGUID       *int64 `gorm:"column:target_guid;type:bigint" json:"-"`
	IntentHMAC       string `gorm:"column:intent_hmac;type:char(64);not null" json:"-"`
	TicketHMAC       string `gorm:"column:ticket_hmac;type:char(64);not null" json:"-"`
	ExpiresAt        int64  `gorm:"column:expires_at;type:bigint;not null" json:"-"`
	ConsumedAt       *int64 `gorm:"column:consumed_at;type:bigint" json:"-"`
}

func (AdminActionVerification) TableName() string { return "admin_action_verifications" }

// StatusAt derives the verification status. Consumption takes precedence over
// expiry because consumed records are also logical tombstones.
func (verification AdminActionVerification) StatusAt(now int64) AdminActionVerificationStatus {
	if verification.ConsumedAt != nil {
		return VerificationConsumed
	}
	if verification.ExpiresAt <= now || verification.IsDeleted == 1 {
		return VerificationExpired
	}
	return VerificationActive
}

type AdminOperation struct {
	ID                 int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields        `gorm:"embedded" json:"-"`
	ActorUserID        int64                  `gorm:"column:actor_user_id;type:bigint;not null" json:"-"`
	ActorAuthVersion   int                    `gorm:"column:actor_auth_version;type:int;not null" json:"-"`
	SessionID          int64                  `gorm:"column:session_id;type:bigint;not null" json:"-"`
	Action             int                    `gorm:"column:action;type:int;not null" json:"-"`
	IdempotencyKeyHMAC string                 `gorm:"column:idempotency_key_hmac;type:char(64);not null" json:"-"`
	RequestHMAC        string                 `gorm:"column:request_hmac;type:char(64);not null" json:"-"`
	VerificationID     *int64                 `gorm:"column:verification_id;type:bigint" json:"-"`
	State              AdminOperationState    `gorm:"column:state;type:int;not null" json:"-"`
	PublicRef          string                 `gorm:"column:public_ref;type:char(46);not null" json:"-"`
	LeaseOwnerHMAC     *string                `gorm:"column:lease_owner_hmac;type:char(64)" json:"-"`
	LeaseExpiresAt     *int64                 `gorm:"column:lease_expires_at;type:bigint" json:"-"`
	FinishedAt         *int64                 `gorm:"column:finished_at;type:bigint" json:"-"`
	QueryExpiresAt     int64                  `gorm:"column:query_expires_at;type:bigint;not null" json:"-"`
	ErrorCode          *AdminOperationFailure `gorm:"column:error_code;type:int" json:"-"`
	ResultKind         *AdminResultKind       `gorm:"column:result_kind;type:int" json:"-"`
	ResultGUID         *int64                 `gorm:"column:result_guid;type:bigint" json:"-"`
	ResultHTTPStatus   *int                   `gorm:"column:result_http_status;type:int" json:"-"`
}

func (AdminOperation) TableName() string { return "admin_operations" }
