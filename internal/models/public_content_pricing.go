package models

// PublicModelConfigStatus is the persisted lifecycle for a configured public model.
type PublicModelConfigStatus int

const (
	PublicModelConfigStatusDraft    PublicModelConfigStatus = 1
	PublicModelConfigStatusActive   PublicModelConfigStatus = 2
	PublicModelConfigStatusInactive PublicModelConfigStatus = 3
)

func (status PublicModelConfigStatus) String() string {
	switch status {
	case PublicModelConfigStatusDraft:
		return "draft"
	case PublicModelConfigStatusActive:
		return "active"
	case PublicModelConfigStatusInactive:
		return "inactive"
	default:
		return "unknown"
	}
}

func ParsePublicModelConfigStatus(value string) (PublicModelConfigStatus, bool) {
	switch value {
	case "draft":
		return PublicModelConfigStatusDraft, true
	case "active":
		return PublicModelConfigStatusActive, true
	case "inactive":
		return PublicModelConfigStatusInactive, true
	default:
		return 0, false
	}
}

type PublicPriceSnapshotReason int

const (
	PublicPriceSnapshotReasonRootPublish    PublicPriceSnapshotReason = 1
	PublicPriceSnapshotReasonUpstreamSafety PublicPriceSnapshotReason = 2
	PublicPriceSnapshotReasonRestore        PublicPriceSnapshotReason = 3
)

type PublicContentDocumentKind int

const (
	PublicContentDocumentSite    PublicContentDocumentKind = 1
	PublicContentDocumentHome    PublicContentDocumentKind = 2
	PublicContentDocumentAbout   PublicContentDocumentKind = 3
	PublicContentDocumentTerms   PublicContentDocumentKind = 4
	PublicContentDocumentPrivacy PublicContentDocumentKind = 5
)

type PublicContentReviewState int

const (
	PublicContentReviewPending  PublicContentReviewState = 1
	PublicContentReviewApproved PublicContentReviewState = 2
)

type PublicPriceVisibility int

const (
	PublicPriceVisibilityVisible           PublicPriceVisibility = 1
	PublicPriceVisibilityAuthenticatedOnly PublicPriceVisibility = 2
)

type RootAlertType int

const (
	RootAlertTypePublishedPriceBelowUpstream RootAlertType = 1
	RootAlertTypeUpstreamMissing             RootAlertType = 2
	RootAlertTypeAutomaticInactivation       RootAlertType = 3
	RootAlertTypeUpstreamReappearance        RootAlertType = 4
	RootAlertTypeCatalogSyncFailure          RootAlertType = 5
	RootAlertTypePriceNotComparable          RootAlertType = 6
	RootAlertTypeRendererFailure             RootAlertType = 7
)

type RootAlertState int

const (
	RootAlertStateActive   RootAlertState = 1
	RootAlertStateResolved RootAlertState = 2
)

type PublicRenderJobState int

const (
	PublicRenderJobQueued    PublicRenderJobState = 1
	PublicRenderJobLeased    PublicRenderJobState = 2
	PublicRenderJobSucceeded PublicRenderJobState = 3
	PublicRenderJobFailed    PublicRenderJobState = 4
)

// PublicModelConfig preserves permanently reserved public and upstream identities.
type PublicModelConfig struct {
	ID                             int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields                    `gorm:"embedded" json:"-"`
	ModelKey                       string                  `gorm:"column:model_key;type:varchar(128);not null;<-:create" json:"model_key"`
	UpstreamModelID                string                  `gorm:"column:upstream_model_id;type:varchar(255);not null;<-:create" json:"upstream_model_id"`
	DisplayName                    string                  `gorm:"column:display_name;type:varchar(128);not null" json:"display_name"`
	Provider                       string                  `gorm:"column:provider;type:varchar(128);not null" json:"provider"`
	Capabilities                   JSONMap                 `gorm:"column:capabilities;type:json;not null" json:"capabilities"`
	ContextWindow                  int64                   `gorm:"column:context_window;type:bigint;not null" json:"context_window"`
	InputPriceUSDPerMillionTokens  *string                 `gorm:"column:input_price_usd_per_million_tokens;type:decimal(20,8)" json:"-"`
	OutputPriceUSDPerMillionTokens *string                 `gorm:"column:output_price_usd_per_million_tokens;type:decimal(20,8)" json:"-"`
	Status                         PublicModelConfigStatus `gorm:"column:status;type:int;not null" json:"-"`
	InactiveReason                 *string                 `gorm:"column:inactive_reason;type:varchar(128)" json:"-"`
	LastUpstreamObservedAt         *int64                  `gorm:"column:last_upstream_observed_at;type:bigint" json:"-"`
	LastUpstreamCheckAt            *int64                  `gorm:"column:last_upstream_check_at;type:bigint" json:"-"`
	ConsecutiveAbsences            int                     `gorm:"column:consecutive_absences;type:int;not null" json:"-"`
	Revision                       int64                   `gorm:"column:revision;type:bigint;not null" json:"-"`
	EverPublished                  int                     `gorm:"column:ever_published;type:int;not null" json:"-"`
}

func (PublicModelConfig) TableName() string { return "public_model_configs" }

type PublicPriceSnapshot struct {
	ID                     int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields            `gorm:"embedded" json:"-"`
	Version                int64                     `gorm:"column:version;type:bigint;not null" json:"-"`
	Reason                 PublicPriceSnapshotReason `gorm:"column:reason;type:int;not null" json:"-"`
	SourceRevision         int64                     `gorm:"column:source_revision;type:bigint;not null" json:"-"`
	ContentHash            string                    `gorm:"column:content_hash;type:char(64);not null" json:"-"`
	RestoredFromSnapshotID *int64                    `gorm:"column:restored_from_snapshot_id;type:bigint" json:"-"`
	PublishedAt            int64                     `gorm:"column:published_at;type:bigint;not null" json:"-"`
}

func (PublicPriceSnapshot) TableName() string { return "public_price_snapshots" }

type PublicPriceSnapshotItem struct {
	ID                             int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields                    `gorm:"embedded" json:"-"`
	SnapshotID                     int64   `gorm:"column:snapshot_id;type:bigint;not null" json:"-"`
	ModelConfigID                  int64   `gorm:"column:model_config_id;type:bigint;not null" json:"-"`
	ModelKey                       string  `gorm:"column:model_key;type:varchar(128);not null" json:"model_key"`
	UpstreamModelID                string  `gorm:"column:upstream_model_id;type:varchar(255);not null" json:"-"`
	DisplayName                    string  `gorm:"column:display_name;type:varchar(128);not null" json:"display_name"`
	Provider                       string  `gorm:"column:provider;type:varchar(128);not null" json:"provider"`
	Capabilities                   JSONMap `gorm:"column:capabilities;type:json;not null" json:"capabilities"`
	ContextWindow                  int64   `gorm:"column:context_window;type:bigint;not null" json:"context_window"`
	InputPriceUSDPerMillionTokens  string  `gorm:"column:input_price_usd_per_million_tokens;type:decimal(20,8);not null" json:"-"`
	OutputPriceUSDPerMillionTokens string  `gorm:"column:output_price_usd_per_million_tokens;type:decimal(20,8);not null" json:"-"`
	UpstreamCheckedAt              *int64  `gorm:"column:upstream_checked_at;type:bigint" json:"-"`
}

func (PublicPriceSnapshotItem) TableName() string { return "public_price_snapshot_items" }

type PublicPublicationState struct {
	ID               int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields      `gorm:"embedded" json:"-"`
	StateKey         string                `gorm:"column:state_key;type:varchar(64);not null" json:"-"`
	PriceSnapshotID  *int64                `gorm:"column:price_snapshot_id;type:bigint" json:"-"`
	ContentReleaseID *int64                `gorm:"column:content_release_id;type:bigint" json:"-"`
	PriceVisibility  PublicPriceVisibility `gorm:"column:price_visibility;type:int;not null" json:"-"`
	Revision         int64                 `gorm:"column:revision;type:bigint;not null" json:"-"`
}

func (PublicPublicationState) TableName() string { return "public_publication_state" }

type PublicContentDraft struct {
	ID           int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields  `gorm:"embedded" json:"-"`
	DocumentKind PublicContentDocumentKind `gorm:"column:document_kind;type:int;not null" json:"-"`
	Payload      JSONMap                   `gorm:"column:payload;type:json;not null" json:"-"`
	Revision     int64                     `gorm:"column:revision;type:bigint;not null" json:"-"`
	ReviewState  PublicContentReviewState  `gorm:"column:review_state;type:int;not null" json:"-"`
}

func (PublicContentDraft) TableName() string { return "public_content_drafts" }

type PublicContentRelease struct {
	ID                    int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields           `gorm:"embedded" json:"-"`
	DocumentKind          PublicContentDocumentKind `gorm:"column:document_kind;type:int;not null" json:"-"`
	Version               int64                     `gorm:"column:version;type:bigint;not null" json:"-"`
	SourceRevision        int64                     `gorm:"column:source_revision;type:bigint;not null" json:"-"`
	Payload               JSONMap                   `gorm:"column:payload;type:json;not null" json:"-"`
	ContentHash           string                    `gorm:"column:content_hash;type:char(64);not null" json:"-"`
	RestoredFromReleaseID *int64                    `gorm:"column:restored_from_release_id;type:bigint" json:"-"`
	PublishedAt           int64                     `gorm:"column:published_at;type:bigint;not null" json:"-"`
}

func (PublicContentRelease) TableName() string { return "public_content_releases" }

type UpstreamModelObservation struct {
	ID                             int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields                    `gorm:"embedded" json:"-"`
	UpstreamModelID                string  `gorm:"column:upstream_model_id;type:varchar(255);not null" json:"-"`
	Provider                       string  `gorm:"column:provider;type:varchar(128);not null" json:"-"`
	InputPriceUSDPerMillionTokens  *string `gorm:"column:input_price_usd_per_million_tokens;type:decimal(20,8)" json:"-"`
	OutputPriceUSDPerMillionTokens *string `gorm:"column:output_price_usd_per_million_tokens;type:decimal(20,8)" json:"-"`
	CatalogComplete                int     `gorm:"column:catalog_complete;type:int;not null" json:"-"`
	CatalogFresh                   int     `gorm:"column:catalog_fresh;type:int;not null" json:"-"`
	ObservedAt                     int64   `gorm:"column:observed_at;type:bigint;not null" json:"-"`
	ResponseSummaryHash            string  `gorm:"column:response_summary_hash;type:char(64);not null" json:"-"`
}

func (UpstreamModelObservation) TableName() string { return "upstream_model_observations" }

type RootAlert struct {
	ID              int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields     `gorm:"embedded" json:"-"`
	ModelConfigID   *int64         `gorm:"column:model_config_id;type:bigint" json:"-"`
	ModelKey        *string        `gorm:"column:model_key;type:varchar(128)" json:"-"`
	AlertType       RootAlertType  `gorm:"column:alert_type;type:int;not null" json:"-"`
	State           RootAlertState `gorm:"column:state;type:int;not null" json:"-"`
	Fingerprint     string         `gorm:"column:fingerprint;type:char(64);not null" json:"-"`
	Payload         JSONMap        `gorm:"column:payload;type:json;not null" json:"-"`
	OccurrenceCount int            `gorm:"column:occurrence_count;type:int;not null" json:"-"`
	FirstObservedAt int64          `gorm:"column:first_observed_at;type:bigint;not null" json:"-"`
	LastObservedAt  int64          `gorm:"column:last_observed_at;type:bigint;not null" json:"-"`
	ResolvedAt      *int64         `gorm:"column:resolved_at;type:bigint" json:"-"`
}

func (RootAlert) TableName() string { return "root_alerts" }

type RootAlertReceipt struct {
	ID             int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields    `gorm:"embedded" json:"-"`
	AlertID        int64  `gorm:"column:alert_id;type:bigint;not null" json:"-"`
	RootUserID     int64  `gorm:"column:root_user_id;type:bigint;not null" json:"-"`
	ReadAt         *int64 `gorm:"column:read_at;type:bigint" json:"-"`
	AcknowledgedAt *int64 `gorm:"column:acknowledged_at;type:bigint" json:"-"`
}

func (RootAlertReceipt) TableName() string { return "root_alert_receipts" }

type PublicRenderJob struct {
	ID               int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields      `gorm:"embedded" json:"-"`
	PriceSnapshotID  int64                `gorm:"column:price_snapshot_id;type:bigint;not null" json:"-"`
	ContentReleaseID int64                `gorm:"column:content_release_id;type:bigint;not null" json:"-"`
	State            PublicRenderJobState `gorm:"column:state;type:int;not null" json:"-"`
	LeaseOwnerHMAC   *string              `gorm:"column:lease_owner_hmac;type:char(64)" json:"-"`
	LeaseExpiresAt   *int64               `gorm:"column:lease_expires_at;type:bigint" json:"-"`
	AttemptCount     int                  `gorm:"column:attempt_count;type:int;not null" json:"-"`
	LastFailure      *string              `gorm:"column:last_failure;type:varchar(1024)" json:"-"`
	CompletedAt      *int64               `gorm:"column:completed_at;type:bigint" json:"-"`
}

func (PublicRenderJob) TableName() string { return "public_render_jobs" }
