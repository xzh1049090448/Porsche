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

func (reason PublicPriceSnapshotReason) String() string {
	switch reason {
	case PublicPriceSnapshotReasonRootPublish:
		return "root_publish"
	case PublicPriceSnapshotReasonUpstreamSafety:
		return "upstream_safety"
	case PublicPriceSnapshotReasonRestore:
		return "restore"
	default:
		return "unknown"
	}
}

func ParsePublicPriceSnapshotReason(value string) (PublicPriceSnapshotReason, bool) {
	switch value {
	case "root_publish":
		return PublicPriceSnapshotReasonRootPublish, true
	case "upstream_safety":
		return PublicPriceSnapshotReasonUpstreamSafety, true
	case "restore":
		return PublicPriceSnapshotReasonRestore, true
	default:
		return 0, false
	}
}

type PublicContentDocumentKind int

const (
	PublicContentDocumentSite    PublicContentDocumentKind = 1
	PublicContentDocumentHome    PublicContentDocumentKind = 2
	PublicContentDocumentAbout   PublicContentDocumentKind = 3
	PublicContentDocumentTerms   PublicContentDocumentKind = 4
	PublicContentDocumentPrivacy PublicContentDocumentKind = 5
)

func (kind PublicContentDocumentKind) String() string {
	switch kind {
	case PublicContentDocumentSite:
		return "site"
	case PublicContentDocumentHome:
		return "home"
	case PublicContentDocumentAbout:
		return "about"
	case PublicContentDocumentTerms:
		return "terms"
	case PublicContentDocumentPrivacy:
		return "privacy"
	default:
		return "unknown"
	}
}

func ParsePublicContentDocumentKind(value string) (PublicContentDocumentKind, bool) {
	switch value {
	case "site":
		return PublicContentDocumentSite, true
	case "home":
		return PublicContentDocumentHome, true
	case "about":
		return PublicContentDocumentAbout, true
	case "terms":
		return PublicContentDocumentTerms, true
	case "privacy":
		return PublicContentDocumentPrivacy, true
	default:
		return 0, false
	}
}

type PublicContentReviewState int

const (
	PublicContentReviewPending  PublicContentReviewState = 1
	PublicContentReviewApproved PublicContentReviewState = 2
)

func (state PublicContentReviewState) String() string {
	switch state {
	case PublicContentReviewPending:
		return "pending"
	case PublicContentReviewApproved:
		return "approved"
	default:
		return "unknown"
	}
}

func ParsePublicContentReviewState(value string) (PublicContentReviewState, bool) {
	switch value {
	case "pending":
		return PublicContentReviewPending, true
	case "approved":
		return PublicContentReviewApproved, true
	default:
		return 0, false
	}
}

type PublicPriceVisibility int

const (
	PublicPriceVisibilityVisible           PublicPriceVisibility = 1
	PublicPriceVisibilityAuthenticatedOnly PublicPriceVisibility = 2
)

func (visibility PublicPriceVisibility) String() string {
	switch visibility {
	case PublicPriceVisibilityVisible:
		return "visible"
	case PublicPriceVisibilityAuthenticatedOnly:
		return "authenticated_only"
	default:
		return "unknown"
	}
}

func ParsePublicPriceVisibility(value string) (PublicPriceVisibility, bool) {
	switch value {
	case "visible":
		return PublicPriceVisibilityVisible, true
	case "authenticated_only":
		return PublicPriceVisibilityAuthenticatedOnly, true
	default:
		return 0, false
	}
}

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

func (alertType RootAlertType) String() string {
	switch alertType {
	case RootAlertTypePublishedPriceBelowUpstream:
		return "published_price_below_upstream"
	case RootAlertTypeUpstreamMissing:
		return "upstream_missing"
	case RootAlertTypeAutomaticInactivation:
		return "automatic_inactivation"
	case RootAlertTypeUpstreamReappearance:
		return "upstream_reappearance"
	case RootAlertTypeCatalogSyncFailure:
		return "catalog_sync_failure"
	case RootAlertTypePriceNotComparable:
		return "price_not_comparable"
	case RootAlertTypeRendererFailure:
		return "renderer_failure"
	default:
		return "unknown"
	}
}

func ParseRootAlertType(value string) (RootAlertType, bool) {
	switch value {
	case "published_price_below_upstream":
		return RootAlertTypePublishedPriceBelowUpstream, true
	case "upstream_missing":
		return RootAlertTypeUpstreamMissing, true
	case "automatic_inactivation":
		return RootAlertTypeAutomaticInactivation, true
	case "upstream_reappearance":
		return RootAlertTypeUpstreamReappearance, true
	case "catalog_sync_failure":
		return RootAlertTypeCatalogSyncFailure, true
	case "price_not_comparable":
		return RootAlertTypePriceNotComparable, true
	case "renderer_failure":
		return RootAlertTypeRendererFailure, true
	default:
		return 0, false
	}
}

type RootAlertState int

const (
	RootAlertStateActive   RootAlertState = 1
	RootAlertStateResolved RootAlertState = 2
)

func (state RootAlertState) String() string {
	switch state {
	case RootAlertStateActive:
		return "active"
	case RootAlertStateResolved:
		return "resolved"
	default:
		return "unknown"
	}
}

func ParseRootAlertState(value string) (RootAlertState, bool) {
	switch value {
	case "active":
		return RootAlertStateActive, true
	case "resolved":
		return RootAlertStateResolved, true
	default:
		return 0, false
	}
}

type PublicRenderJobState int

const (
	PublicRenderJobQueued    PublicRenderJobState = 1
	PublicRenderJobLeased    PublicRenderJobState = 2
	PublicRenderJobSucceeded PublicRenderJobState = 3
	PublicRenderJobFailed    PublicRenderJobState = 4
)

func (state PublicRenderJobState) String() string {
	switch state {
	case PublicRenderJobQueued:
		return "queued"
	case PublicRenderJobLeased:
		return "leased"
	case PublicRenderJobSucceeded:
		return "succeeded"
	case PublicRenderJobFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func ParsePublicRenderJobState(value string) (PublicRenderJobState, bool) {
	switch value {
	case "queued":
		return PublicRenderJobQueued, true
	case "leased":
		return PublicRenderJobLeased, true
	case "succeeded":
		return PublicRenderJobSucceeded, true
	case "failed":
		return PublicRenderJobFailed, true
	default:
		return 0, false
	}
}

// PublicModelConfig preserves permanently reserved public and upstream identities.
type PublicModelConfig struct {
	ID                             int64 `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	AuditFields                    `gorm:"embedded" json:"-"`
	ModelKey                       string                  `gorm:"column:model_key;type:varchar(128);not null;<-:create" json:"model_key"`
	UpstreamModelID                string                  `gorm:"column:upstream_model_id;type:varchar(255);not null;<-:create" json:"upstream_model_id"`
	DisplayName                    string                  `gorm:"column:display_name;type:varchar(128);not null" json:"display_name"`
	Provider                       string                  `gorm:"column:provider;type:varchar(128);not null" json:"provider"`
	Capabilities                   JSONSlice               `gorm:"column:capabilities;type:json;not null" json:"capabilities"`
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
	ID                     int64                     `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	Guid                   int64                     `gorm:"column:guid;type:bigint;not null;<-:create" json:"guid"`
	CreatedAt              int64                     `gorm:"column:created_at;type:bigint;not null;<-:create" json:"created_at"`
	CreatedBy              *int64                    `gorm:"column:created_by;type:bigint;<-:create" json:"created_by,omitempty"`
	UpdatedAt              int64                     `gorm:"column:updated_at;type:bigint;not null;<-:create" json:"updated_at"`
	UpdatedBy              *int64                    `gorm:"column:updated_by;type:bigint;<-:create" json:"updated_by,omitempty"`
	IsDeleted              int                       `gorm:"column:is_deleted;type:int;not null;default:0" json:"-"`
	Version                int64                     `gorm:"column:version;type:bigint;not null;<-:create" json:"-"`
	Reason                 PublicPriceSnapshotReason `gorm:"column:reason;type:int;not null;<-:create" json:"-"`
	SourceRevision         int64                     `gorm:"column:source_revision;type:bigint;not null;<-:create" json:"-"`
	ContentHash            string                    `gorm:"column:content_hash;type:char(64);not null;<-:create" json:"-"`
	RestoredFromSnapshotID *int64                    `gorm:"column:restored_from_snapshot_id;type:bigint;<-:create" json:"-"`
	PublishedAt            int64                     `gorm:"column:published_at;type:bigint;not null;<-:create" json:"-"`
}

func (PublicPriceSnapshot) TableName() string { return "public_price_snapshots" }

type PublicPriceSnapshotItem struct {
	ID                             int64     `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	Guid                           int64     `gorm:"column:guid;type:bigint;not null;<-:create" json:"guid"`
	CreatedAt                      int64     `gorm:"column:created_at;type:bigint;not null;<-:create" json:"created_at"`
	CreatedBy                      *int64    `gorm:"column:created_by;type:bigint;<-:create" json:"created_by,omitempty"`
	UpdatedAt                      int64     `gorm:"column:updated_at;type:bigint;not null;<-:create" json:"updated_at"`
	UpdatedBy                      *int64    `gorm:"column:updated_by;type:bigint;<-:create" json:"updated_by,omitempty"`
	IsDeleted                      int       `gorm:"column:is_deleted;type:int;not null;default:0" json:"-"`
	SnapshotID                     int64     `gorm:"column:snapshot_id;type:bigint;not null;<-:create" json:"-"`
	ModelConfigID                  int64     `gorm:"column:model_config_id;type:bigint;not null;<-:create" json:"-"`
	ModelKey                       string    `gorm:"column:model_key;type:varchar(128);not null;<-:create" json:"model_key"`
	UpstreamModelID                string    `gorm:"column:upstream_model_id;type:varchar(255);not null;<-:create" json:"-"`
	DisplayName                    string    `gorm:"column:display_name;type:varchar(128);not null;<-:create" json:"display_name"`
	Provider                       string    `gorm:"column:provider;type:varchar(128);not null;<-:create" json:"provider"`
	Capabilities                   JSONSlice `gorm:"column:capabilities;type:json;not null;<-:create" json:"capabilities"`
	ContextWindow                  int64     `gorm:"column:context_window;type:bigint;not null;<-:create" json:"context_window"`
	InputPriceUSDPerMillionTokens  string    `gorm:"column:input_price_usd_per_million_tokens;type:decimal(20,8);not null;<-:create" json:"-"`
	OutputPriceUSDPerMillionTokens string    `gorm:"column:output_price_usd_per_million_tokens;type:decimal(20,8);not null;<-:create" json:"-"`
	UpstreamCheckedAt              *int64    `gorm:"column:upstream_checked_at;type:bigint;<-:create" json:"-"`
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
	ID                    int64                     `gorm:"column:id;type:bigint;not null;primaryKey;autoIncrement" json:"-"`
	Guid                  int64                     `gorm:"column:guid;type:bigint;not null;<-:create" json:"guid"`
	CreatedAt             int64                     `gorm:"column:created_at;type:bigint;not null;<-:create" json:"created_at"`
	CreatedBy             *int64                    `gorm:"column:created_by;type:bigint;<-:create" json:"created_by,omitempty"`
	UpdatedAt             int64                     `gorm:"column:updated_at;type:bigint;not null;<-:create" json:"updated_at"`
	UpdatedBy             *int64                    `gorm:"column:updated_by;type:bigint;<-:create" json:"updated_by,omitempty"`
	IsDeleted             int                       `gorm:"column:is_deleted;type:int;not null;default:0" json:"-"`
	DocumentKind          PublicContentDocumentKind `gorm:"column:document_kind;type:int;not null;<-:create" json:"-"`
	Version               int64                     `gorm:"column:version;type:bigint;not null;<-:create" json:"-"`
	SourceRevision        int64                     `gorm:"column:source_revision;type:bigint;not null;<-:create" json:"-"`
	Payload               JSONMap                   `gorm:"column:payload;type:json;not null;<-:create" json:"-"`
	ContentHash           string                    `gorm:"column:content_hash;type:char(64);not null;<-:create" json:"-"`
	RestoredFromReleaseID *int64                    `gorm:"column:restored_from_release_id;type:bigint;<-:create" json:"-"`
	PublishedAt           int64                     `gorm:"column:published_at;type:bigint;not null;<-:create" json:"-"`
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
