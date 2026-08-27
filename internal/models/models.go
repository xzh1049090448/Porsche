// Package models contains the persisted MySQL domain entities and their integer-backed enum mappings.
package models

type UserStatus int

const (
	UserStatusActive   UserStatus = 1
	UserStatusDisabled UserStatus = 2
)

func (s UserStatus) IsActive() bool { return s == UserStatusActive }
func (s UserStatus) String() string {
	if s == UserStatusActive {
		return "active"
	}
	if s == UserStatusDisabled {
		return "disabled"
	}
	return "unknown"
}
func ParseUserStatus(value string) (UserStatus, bool) {
	switch value {
	case "active":
		return UserStatusActive, true
	case "disabled":
		return UserStatusDisabled, true
	}
	return 0, false
}

type GatewayTokenStatus int

const (
	GatewayTokenActive   GatewayTokenStatus = 1
	GatewayTokenDisabled GatewayTokenStatus = 2
	GatewayTokenRevoked  GatewayTokenStatus = 3
)

func (s GatewayTokenStatus) String() string {
	switch s {
	case GatewayTokenActive:
		return "active"
	case GatewayTokenDisabled:
		return "disabled"
	case GatewayTokenRevoked:
		return "revoked"
	}
	return "unknown"
}

type PlanType int

const (
	PlanFree         PlanType = 1
	PlanProfessional PlanType = 2
	PlanEnterprise   PlanType = 3
)

func (p PlanType) String() string {
	switch p {
	case PlanFree:
		return "free"
	case PlanProfessional:
		return "professional"
	case PlanEnterprise:
		return "enterprise"
	}
	return "unknown"
}
func ParsePlanType(value string) (PlanType, bool) {
	switch value {
	case "free":
		return PlanFree, true
	case "professional":
		return PlanProfessional, true
	case "enterprise":
		return PlanEnterprise, true
	}
	return 0, false
}

type OrderStatus int

const (
	OrderPending   OrderStatus = 1
	OrderPaid      OrderStatus = 2
	OrderCancelled OrderStatus = 3
	OrderRefunded  OrderStatus = 4
)

func (s OrderStatus) String() string {
	switch s {
	case OrderPending:
		return "pending"
	case OrderPaid:
		return "paid"
	case OrderCancelled:
		return "cancelled"
	case OrderRefunded:
		return "refunded"
	}
	return "unknown"
}

type MessageRole int

const (
	MessageRoleSystem    MessageRole = 1
	MessageRoleUser      MessageRole = 2
	MessageRoleAssistant MessageRole = 3
)

func (r MessageRole) String() string {
	switch r {
	case MessageRoleSystem:
		return "system"
	case MessageRoleUser:
		return "user"
	case MessageRoleAssistant:
		return "assistant"
	}
	return "unknown"
}
func ParseMessageRole(value string) (MessageRole, bool) {
	switch value {
	case "system":
		return MessageRoleSystem, true
	case "user":
		return MessageRoleUser, true
	case "assistant":
		return MessageRoleAssistant, true
	}
	return 0, false
}

type UsageRecordType int

const (
	UsageRecordChat UsageRecordType = 1
)

// UserRole is the stable, integer-backed minimum role used by auth middleware.
type UserRole int

const (
	UserRoleUser  UserRole = 1
	UserRoleAdmin UserRole = 10
	UserRoleRoot  UserRole = 100
)

func (r UserRole) String() string {
	switch r {
	case UserRoleUser:
		return "user"
	case UserRoleAdmin:
		return "admin"
	case UserRoleRoot:
		return "root"
	default:
		return "unknown"
	}
}

// ParseUserRole converts the API-safe role name to its stable stored value.
func ParseUserRole(value string) (UserRole, bool) {
	switch value {
	case "user":
		return UserRoleUser, true
	case "admin":
		return UserRoleAdmin, true
	case "root":
		return UserRoleRoot, true
	default:
		return 0, false
	}
}

// LoginMethod identifies the credential flow that created a server session.
type LoginMethod int

const (
	LoginMethodPassword LoginMethod = 1
)

func (m LoginMethod) String() string {
	switch m {
	case LoginMethodPassword:
		return "password"
	default:
		return "unknown"
	}
}

// ParseLoginMethod converts an API-safe login method name to its stored value.
func ParseLoginMethod(value string) (LoginMethod, bool) {
	switch value {
	case "password":
		return LoginMethodPassword, true
	default:
		return 0, false
	}
}

// AuthAuditEventType records an authentication security event without storing
// any credential, token, cookie, or authorization value.
type AuthAuditEventType int

const (
	AuthAuditEventRegistered       AuthAuditEventType = 1
	AuthAuditEventLoginSucceeded   AuthAuditEventType = 2
	AuthAuditEventRefreshSucceeded AuthAuditEventType = 3
	AuthAuditEventReplayRevoked    AuthAuditEventType = 4
	AuthAuditEventLoggedOut        AuthAuditEventType = 5
	AuthAuditEventSessionRevoked   AuthAuditEventType = 6
	AuthAuditEventUserDisabled     AuthAuditEventType = 7
	AuthAuditEventUserDeleted      AuthAuditEventType = 8
)

func (e AuthAuditEventType) String() string {
	switch e {
	case AuthAuditEventRegistered:
		return "registered"
	case AuthAuditEventLoginSucceeded:
		return "login_succeeded"
	case AuthAuditEventRefreshSucceeded:
		return "refresh_succeeded"
	case AuthAuditEventReplayRevoked:
		return "refresh_replay_revoked"
	case AuthAuditEventLoggedOut:
		return "logged_out"
	case AuthAuditEventSessionRevoked:
		return "session_revoked"
	case AuthAuditEventUserDisabled:
		return "user_disabled"
	case AuthAuditEventUserDeleted:
		return "user_deleted"
	default:
		return "unknown"
	}
}

// ParseAuthAuditEventType converts a security event name to its stable stored
// integer without accepting arbitrary user-provided database values.
func ParseAuthAuditEventType(value string) (AuthAuditEventType, bool) {
	switch value {
	case "registered":
		return AuthAuditEventRegistered, true
	case "login_succeeded":
		return AuthAuditEventLoginSucceeded, true
	case "refresh_succeeded":
		return AuthAuditEventRefreshSucceeded, true
	case "refresh_replay_revoked":
		return AuthAuditEventReplayRevoked, true
	case "logged_out":
		return AuthAuditEventLoggedOut, true
	case "session_revoked":
		return AuthAuditEventSessionRevoked, true
	case "user_disabled":
		return AuthAuditEventUserDisabled, true
	case "user_deleted":
		return AuthAuditEventUserDeleted, true
	default:
		return 0, false
	}
}

// AuditFields is embedded in every persisted table. Timestamps are UTC Unix milliseconds; nil actor IDs denote system work.
type AuditFields struct {
	Guid      int64  `gorm:"type:bigint;not null;uniqueIndex" json:"guid"`
	CreatedAt int64  `gorm:"type:bigint;not null" json:"created_at"`
	CreatedBy *int64 `gorm:"type:bigint" json:"created_by,omitempty"`
	UpdatedAt int64  `gorm:"type:bigint;not null" json:"updated_at"`
	UpdatedBy *int64 `gorm:"type:bigint" json:"updated_by,omitempty"`
	IsDeleted int    `gorm:"type:int;not null;default:0;index" json:"-"`
}

type User struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	Phone             *string    `gorm:"size:20;uniqueIndex" json:"-"`
	Username          *string    `gorm:"size:20;uniqueIndex" json:"username,omitempty"`
	PasswordHash      *string    `gorm:"size:255" json:"-"`
	Nickname          *string    `gorm:"size:64" json:"nickname"`
	RealName          *string    `gorm:"size:64" json:"real_name,omitempty"`
	IDCardHash        *string    `gorm:"size:128" json:"-"`
	IsVerified        bool       `gorm:"not null;default:false" json:"is_verified"`
	PlanType          PlanType   `gorm:"type:int;not null;default:1" json:"-"`
	Status            UserStatus `gorm:"type:int;not null;default:1" json:"-"`
	Role              UserRole   `gorm:"type:int;not null;default:1" json:"-"`
	AuthVersion       int        `gorm:"type:int;not null;default:1" json:"-"`
	LastLoginAt       *int64     `gorm:"type:bigint" json:"-"`
	AllowedModels     JSONSlice  `gorm:"type:json;not null" json:"allowed_models,omitempty"`
	DailyCallLimit    int        `gorm:"not null;default:100" json:"daily_call_limit"`
	DailyCallsUsed    int        `gorm:"not null;default:0" json:"daily_calls_used"`
	DailyCallsResetAt *int64     `gorm:"type:bigint" json:"-"`
	TotalTokensUsed   int64      `gorm:"not null;default:0" json:"total_tokens_used"`
}

// Session is a server-side, revocable authentication session. SID is a secret
// selector only; Guid remains the public business identifier.
type Session struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	SID                      string      `gorm:"size:36;not null;uniqueIndex" json:"-"`
	UserID                   int64       `gorm:"type:bigint;not null;index" json:"-"`
	LoginMethod              LoginMethod `gorm:"type:int;not null" json:"-"`
	IP                       *string     `gorm:"size:64" json:"-"`
	UserAgent                *string     `gorm:"size:512" json:"-"`
	SessionVersion           int         `gorm:"type:int;not null;default:1" json:"-"`
	RefreshHMAC              string      `gorm:"size:64;not null" json:"-"`
	PreviousRefreshHMAC      *string     `gorm:"size:64" json:"-"`
	PreviousRefreshExpiresAt *int64      `gorm:"type:bigint" json:"-"`
	LastActiveAt             int64       `gorm:"type:bigint;not null" json:"-"`
	ExpiresAt                int64       `gorm:"type:bigint;not null;index" json:"-"`
	RevokedAt                *int64      `gorm:"type:bigint" json:"-"`
}

// TableName matches the explicit MySQL migration rather than GORM's default.
func (Session) TableName() string { return "user_sessions" }

// AuthAuditEvent records security-relevant authentication state changes. Its
// fields intentionally exclude password hashes, refresh material, and tokens.
type AuthAuditEvent struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	UserID      *int64             `gorm:"type:bigint;index" json:"-"`
	SessionGuid *int64             `gorm:"type:bigint;index" json:"-"`
	EventType   AuthAuditEventType `gorm:"type:int;not null;index" json:"-"`
	LoginMethod *LoginMethod       `gorm:"type:int" json:"-"`
	IP          *string            `gorm:"size:64" json:"-"`
	UserAgent   *string            `gorm:"size:512" json:"-"`
}
type Conversation struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	UserID   int64     `gorm:"type:bigint;not null;index" json:"-"`
	Title    string    `gorm:"size:256;not null;default:新对话" json:"title"`
	Model    *string   `gorm:"size:128" json:"model"`
	Messages []Message `gorm:"foreignKey:ConversationID" json:"messages,omitempty"`
}
type Message struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	ConversationID int64       `gorm:"type:bigint;not null;index" json:"-"`
	Role           MessageRole `gorm:"type:int;not null" json:"-"`
	Content        string      `gorm:"type:text;not null" json:"content"`
	Model          *string     `gorm:"size:128" json:"model"`
	Tokens         int         `gorm:"not null;default:0" json:"tokens"`
}
type UsageRecord struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	UserID     int64           `gorm:"type:bigint;not null;index" json:"-"`
	RecordType UsageRecordType `gorm:"type:int;not null" json:"-"`
	Tokens     int             `gorm:"not null;default:0" json:"tokens"`
	Model      *string         `gorm:"size:128" json:"model"`
}
type Order struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	OrderNo          string      `gorm:"size:64;not null;uniqueIndex" json:"order_no"`
	UserID           int64       `gorm:"type:bigint;not null;index" json:"-"`
	PlanType         PlanType    `gorm:"type:int;not null" json:"-"`
	Amount           float64     `gorm:"not null;default:0" json:"amount"`
	Status           OrderStatus `gorm:"type:int;not null;default:1" json:"-"`
	InvoiceRequested bool        `gorm:"not null;default:false" json:"invoice_requested"`
	PaidAt           *int64      `gorm:"type:bigint" json:"-"`
}
type AuditLog struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	UserID   *int64  `gorm:"type:bigint;index" json:"-"`
	Action   string  `gorm:"size:64;not null;index" json:"action"`
	Resource *string `gorm:"size:128" json:"resource"`
	Detail   JSONMap `gorm:"type:json;not null" json:"detail"`
	IP       *string `gorm:"size:64" json:"ip"`
}
type ModelHealth struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	ModelName     string  `gorm:"size:128;not null;uniqueIndex" json:"model_name"`
	Provider      string  `gorm:"size:64;not null" json:"provider"`
	IsAvailable   bool    `gorm:"not null;default:true" json:"is_available"`
	AvgLatencyMs  float64 `gorm:"not null;default:0" json:"avg_latency_ms"`
	ErrorRate     float64 `gorm:"not null;default:0" json:"error_rate"`
	LastCheckedAt *int64  `gorm:"type:bigint" json:"-"`
}
type GatewayAPIToken struct {
	ID int64 `gorm:"primaryKey;type:bigint" json:"-"`
	AuditFields
	UserID        int64              `gorm:"type:bigint;not null;index" json:"-"`
	Name          string             `gorm:"size:128;not null" json:"name"`
	TokenHash     string             `gorm:"size:64;not null;uniqueIndex" json:"-"`
	TokenPrefix   string             `gorm:"size:24;not null;index" json:"token_prefix"`
	Status        GatewayTokenStatus `gorm:"type:int;not null;default:1;index" json:"-"`
	AllowedModels JSONSlice          `gorm:"type:json;not null" json:"allowed_models"`
	IPAllowlist   JSONSlice          `gorm:"type:json;not null" json:"ip_allowlist"`
	ExpiresAt     *int64             `gorm:"type:bigint;index" json:"-"`
	LastUsedAt    *int64             `gorm:"type:bigint" json:"-"`
}

// TableName matches the singular table created by the explicit MySQL migration.
func (ModelHealth) TableName() string { return "model_health" }

func (GatewayAPIToken) TableName() string { return "gateway_api_tokens" }
