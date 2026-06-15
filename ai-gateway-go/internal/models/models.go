package models

import (
	"time"
)

type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
)

type PlanType string

const (
	PlanFree         PlanType = "free"
	PlanProfessional PlanType = "professional"
	PlanEnterprise   PlanType = "enterprise"
)

type DatasetCategory string

const (
	CategoryProductKnowledge DatasetCategory = "product_knowledge"
	CategoryCustomerService  DatasetCategory = "customer_service"
	CategoryPlatformRules    DatasetCategory = "platform_rules"
	CategoryReviewSentiment  DatasetCategory = "review_sentiment"
)

type DatasetStatus string

const (
	DatasetDraft      DatasetStatus = "draft"
	DatasetProcessing DatasetStatus = "processing"
	DatasetActive     DatasetStatus = "active"
	DatasetOffline    DatasetStatus = "offline"
)

type VectorStatus string

const (
	VectorPending    VectorStatus = "pending"
	VectorProcessing VectorStatus = "processing"
	VectorReady      VectorStatus = "ready"
	VectorFailed     VectorStatus = "failed"
)

type OrderStatus string

const (
	OrderPending   OrderStatus = "pending"
	OrderPaid      OrderStatus = "paid"
	OrderCancelled OrderStatus = "cancelled"
	OrderRefunded  OrderStatus = "refunded"
)

type User struct {
	ID                 uint       `gorm:"primaryKey" json:"id"`
	Phone              string     `gorm:"size:20;uniqueIndex" json:"phone"`
	PasswordHash       *string    `gorm:"size:255" json:"-"`
	Nickname           *string    `gorm:"size:64" json:"nickname"`
	RealName           *string    `gorm:"size:64" json:"real_name,omitempty"`
	IDCardHash         *string    `gorm:"size:128" json:"-"`
	IsVerified         bool       `gorm:"default:false" json:"is_verified"`
	PlanType           PlanType   `gorm:"size:32;default:free" json:"plan_type"`
	Status             UserStatus `gorm:"size:32;default:active" json:"status"`
	AllowedModels      JSONSlice  `gorm:"type:json" json:"allowed_models,omitempty"`
	AllowedDatasets    JSONIntSlice `gorm:"type:json" json:"allowed_datasets,omitempty"`
	DailyCallLimit     int        `gorm:"default:100" json:"daily_call_limit"`
	DailyCallsUsed     int        `gorm:"default:0" json:"daily_calls_used"`
	DailyCallsResetAt  *time.Time `json:"daily_calls_reset_at,omitempty"`
	TotalTokensUsed    int        `gorm:"default:0" json:"total_tokens_used"`
	DatasetCalls       int        `gorm:"default:0" json:"dataset_calls"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type Conversation struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	UserID         uint      `gorm:"index" json:"user_id"`
	Title          string    `gorm:"size:256;default:新对话" json:"title"`
	Model          *string   `gorm:"size:128" json:"model"`
	DatasetEnabled bool      `gorm:"default:false" json:"dataset_enabled"`
	DatasetIDs     JSONIntSlice `gorm:"type:json" json:"dataset_ids"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Messages       []Message `gorm:"foreignKey:ConversationID" json:"messages,omitempty"`
}

type Message struct {
	ID                  uint      `gorm:"primaryKey" json:"id"`
	ConversationID      uint      `gorm:"index" json:"conversation_id"`
	Role                string    `gorm:"size:32" json:"role"`
	Content             string    `gorm:"type:text" json:"content"`
	Model               *string   `gorm:"size:128" json:"model"`
	DatasetUsed         bool      `gorm:"default:false" json:"dataset_used"`
	DatasetAttribution  *string   `gorm:"size:512" json:"dataset_attribution"`
	Tokens              int       `gorm:"default:0" json:"tokens"`
	CreatedAt           time.Time `json:"created_at"`
}

type Dataset struct {
	ID                uint            `gorm:"primaryKey" json:"id"`
	Name              string          `gorm:"size:128" json:"name"`
	Slug              string          `gorm:"size:64;uniqueIndex" json:"slug"`
	Category          DatasetCategory `gorm:"size:64" json:"category"`
	Description       *string         `gorm:"type:text" json:"description"`
	Status            DatasetStatus   `gorm:"size:32;default:draft" json:"status"`
	CurrentVersion    string          `gorm:"size:32;default:1.0.0" json:"current_version"`
	TokenCount        int             `gorm:"default:0" json:"token_count"`
	VectorStatus      VectorStatus    `gorm:"size:32;default:pending" json:"vector_status"`
	AccessPlans       JSONSlice       `gorm:"type:json" json:"access_plans"`
	ComplianceReport  JSONMap         `gorm:"type:json" json:"compliance_report,omitempty"`
	AssetID           *string         `gorm:"size:128" json:"asset_id,omitempty"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	Versions          []DatasetVersion `gorm:"foreignKey:DatasetID" json:"versions,omitempty"`
}

type DatasetVersion struct {
	ID                uint      `gorm:"primaryKey" json:"id"`
	DatasetID         uint      `gorm:"index;uniqueIndex:idx_dataset_version" json:"dataset_id"`
	Version           string    `gorm:"size:32;uniqueIndex:idx_dataset_version" json:"version"`
	FilePath          *string   `gorm:"size:512" json:"file_path,omitempty"`
	TokenCount        int       `gorm:"default:0" json:"token_count"`
	RecordCount       int       `gorm:"default:0" json:"record_count"`
	ComplianceReport  JSONMap   `gorm:"type:json" json:"compliance_report,omitempty"`
	IsActive          bool      `gorm:"default:false" json:"is_active"`
	CreatedAt         time.Time `json:"created_at"`
}

type UsageRecord struct {
	ID         uint      `gorm:"primaryKey" json:"id"`
	UserID     uint      `gorm:"index" json:"user_id"`
	RecordType string    `gorm:"size:32" json:"record_type"`
	Tokens     int       `gorm:"default:0" json:"tokens"`
	Model      *string   `gorm:"size:128" json:"model"`
	DatasetID  *uint     `json:"dataset_id"`
	CreatedAt  time.Time `gorm:"index" json:"created_at"`
}

type Order struct {
	ID               uint        `gorm:"primaryKey" json:"id"`
	OrderNo          string      `gorm:"size:64;uniqueIndex" json:"order_no"`
	UserID           uint        `gorm:"index" json:"user_id"`
	PlanType         PlanType    `gorm:"size:32" json:"plan_type"`
	Amount           float64     `gorm:"default:0" json:"amount"`
	Status           OrderStatus `gorm:"size:32;default:pending" json:"status"`
	InvoiceRequested bool        `gorm:"default:false" json:"invoice_requested"`
	CreatedAt        time.Time   `json:"created_at"`
	PaidAt           *time.Time  `json:"paid_at"`
}

type AuditLog struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	UserID    *uint     `gorm:"index" json:"user_id"`
	Action    string    `gorm:"size:64;index" json:"action"`
	Resource  *string   `gorm:"size:128" json:"resource"`
	Detail    JSONMap   `gorm:"type:json" json:"detail"`
	IP        *string   `gorm:"size:64" json:"ip"`
	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

type ModelHealth struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	ModelName      string     `gorm:"size:128;uniqueIndex" json:"model_name"`
	Provider       string     `gorm:"size:64" json:"provider"`
	IsAvailable    bool       `gorm:"default:true" json:"is_available"`
	AvgLatencyMs   float64    `gorm:"default:0" json:"avg_latency_ms"`
	ErrorRate      float64    `gorm:"default:0" json:"error_rate"`
	LastCheckedAt  *time.Time `json:"last_checked_at"`
}
