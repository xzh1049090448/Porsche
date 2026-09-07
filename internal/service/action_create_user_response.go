package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	createAccountResponseMediaType = "application/json"
	createAccountResponseBodyLimit = 4096
	redactedCreateResponseSHA256   = "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a"
)

var ErrCreatedAccountDeleted = &HTTPError{Status: http.StatusGone, Message: "created account deleted"}

// PersistedActionResponse is an owned copy of an immutable operation-bound
// HTTP response. Callers may write Body directly without consulting mutable
// business rows.
type PersistedActionResponse struct {
	HTTPStatus int
	MediaType  string
	Body       []byte
}

type createAccountResponseBody struct {
	OperationRef       string      `json:"operation_ref"`
	User               UserReadDTO `json:"user"`
	PermissionsVersion *string     `json:"permissions_version"`
}

func persistCreateAccountResponse(ctx context.Context, tx *gorm.DB, operation models.AdminOperation, actorID, guid, now int64, user models.User, group models.BusinessGroup, crypto *actionsecurity.Crypto) error {
	if !validCreateWriterTransaction(ctx, tx) || operation.ID <= 0 || actorID <= 0 || guid <= 0 || now <= 0 || operation.PublicRef == "" || crypto == nil {
		return ErrActionOperationUnavailable
	}
	projected, err := ProjectCreatedUserRead(user, group)
	if err != nil {
		return ErrActionOperationUnavailable
	}
	var permissionsVersion *string
	if projected.Role == models.UserRoleAdmin.String() {
		version := "1"
		permissionsVersion = &version
	}
	body, err := json.Marshal(createAccountResponseBody{OperationRef: operation.PublicRef, User: *projected, PermissionsVersion: permissionsVersion})
	if err != nil || len(body) < 2 || len(body) > createAccountResponseBodyLimit || !validCreateAccountResponseBody(body, operation.PublicRef, actionsecurity.Action(operation.Action), user.Guid) {
		return ErrActionOperationUnavailable
	}
	digest := sha256.Sum256(body)
	hmacValue, ok := createAccountResponseHMAC(crypto, operation, models.OperationResponseActive, user.Guid, http.StatusCreated, createAccountResponseMediaType, body)
	if !ok {
		clear(digest[:])
		return ErrActionOperationUnavailable
	}
	actor := actorID
	row := models.AdminOperationResponse{
		AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor},
		OperationID: operation.ID, TargetGUID: user.Guid, LifecycleState: models.OperationResponseActive,
		IntegrityVersion: models.OperationResponseIntegrityHMACV1, ResponseHMAC: &hmacValue,
		HTTPStatus: http.StatusCreated, MediaType: createAccountResponseMediaType,
		ResponseBody: append([]byte(nil), body...), BodySHA256: hex.EncodeToString(digest[:]),
	}
	clear(digest[:])
	created := createWriterDB(ctx, tx).Create(&row)
	if created.Error != nil || created.RowsAffected != 1 || row.ID <= 0 {
		return ErrActionOperationUnavailable
	}
	return nil
}

// CreateAccountResponse loads the canonical response saved in the same
// transaction as the successful create operation. It validates the operation
// binding, digest, and exact DTO before returning an owned byte slice.
func (s *ActionOperationService) CreateAccountResponse(ctx context.Context, action actionsecurity.Action, actor ActionActor, publicRef string) (*PersistedActionResponse, error) {
	if s == nil || ctx == nil || !validOperationActorClaims(actor) ||
		(action != actionsecurity.ActionUsersCreate && action != actionsecurity.ActionUsersCreateAdmin) || len(publicRef) != 46 {
		return nil, ErrActionOperationUnavailable
	}
	db := s.operationDB(ctx)
	var operations []models.AdminOperation
	if err := db.Unscoped().Where("public_ref = ? AND actor_user_id = ? AND actor_auth_version = ? AND action = ?", publicRef, actor.UserID, actor.AuthVersion, int(action)).Limit(2).Find(&operations).Error; err != nil || len(operations) != 1 {
		return nil, ErrActionOperationUnavailable
	}
	operation := operations[0]
	if operation.ID <= 0 || operation.PublicRef != publicRef {
		return nil, ErrActionOperationUnavailable
	}
	var responses []models.AdminOperationResponse
	if err := db.Unscoped().Select("id", "guid", "created_at", "created_by", "updated_at", "updated_by", "is_deleted", "operation_id", "target_guid", "lifecycle_state", "integrity_version", "response_hmac", "http_status", "media_type", "body_sha256").
		Where("operation_id = ?", operation.ID).Limit(2).Find(&responses).Error; err != nil || len(responses) != 1 {
		return nil, ErrActionOperationUnavailable
	}
	response := responses[0]
	if response.ID <= 0 || response.Guid <= 0 || response.OperationID != operation.ID || response.TargetGUID <= 0 || !validCreateResponseOperation(operation, response.TargetGUID) {
		return nil, ErrActionOperationUnavailable
	}
	var target models.User
	if err := db.Unscoped().Select("id", "guid", "is_deleted").Where("guid = ?", response.TargetGUID).First(&target).Error; err != nil || target.ID <= 0 || target.Guid != response.TargetGUID || (target.IsDeleted != 0 && target.IsDeleted != 1) {
		return nil, ErrActionOperationUnavailable
	}
	if target.IsDeleted == 1 {
		if response.IsDeleted != 1 || response.IntegrityVersion != models.OperationResponseIntegrityHMACV1 || response.ResponseHMAC == nil ||
			response.LifecycleState != models.OperationResponseRedacted || response.UpdatedAt < response.CreatedAt || response.UpdatedBy == nil || response.BodySHA256 != redactedCreateResponseSHA256 {
			return nil, ErrActionOperationUnavailable
		}
		body, ok := loadCreateAccountResponseBody(db, response.ID)
		if !ok {
			return nil, ErrActionOperationUnavailable
		}
		defer clear(body)
		response.ResponseBody = body
		if !bytes.Equal(response.ResponseBody, []byte("{}")) {
			return nil, ErrActionOperationUnavailable
		}
		if !matchingCreateAccountResponseHMAC(s.crypto, operation, response) {
			return nil, ErrActionOperationUnavailable
		}
		return nil, ErrCreatedAccountDeleted
	}
	if response.LifecycleState != models.OperationResponseActive || response.ID <= 0 || response.HTTPStatus != http.StatusCreated ||
		response.MediaType != createAccountResponseMediaType || response.CreatedAt <= 0 || response.UpdatedAt != response.CreatedAt || response.IsDeleted != 0 {
		return nil, ErrActionOperationUnavailable
	}
	body, ok := loadCreateAccountResponseBody(db, response.ID)
	if !ok {
		return nil, ErrActionOperationUnavailable
	}
	defer clear(body)
	response.ResponseBody = body
	if len(response.ResponseBody) < 2 || len(response.ResponseBody) > createAccountResponseBodyLimit || !matchingCreateAccountResponseDigest(response.ResponseBody, response.BodySHA256) ||
		!validCreateAccountResponseBody(response.ResponseBody, publicRef, action, response.TargetGUID) {
		return nil, ErrActionOperationUnavailable
	}
	if response.IntegrityVersion != models.OperationResponseIntegrityHMACV1 || response.ResponseHMAC == nil || !matchingCreateAccountResponseHMAC(s.crypto, operation, response) {
		return nil, ErrActionOperationUnavailable
	}
	return &PersistedActionResponse{HTTPStatus: response.HTTPStatus, MediaType: strings.Clone(response.MediaType), Body: append([]byte(nil), response.ResponseBody...)}, nil
}

func loadCreateAccountResponseBody(db *gorm.DB, responseID int64) ([]byte, bool) {
	if db == nil || responseID <= 0 {
		return nil, false
	}
	var rows []struct {
		ResponseBody []byte `gorm:"column:response_body"`
	}
	if err := db.Unscoped().Table("admin_operation_responses").Select("response_body").Where("id = ?", responseID).Limit(2).Find(&rows).Error; err != nil || len(rows) != 1 || len(rows[0].ResponseBody) == 0 {
		return nil, false
	}
	return append([]byte(nil), rows[0].ResponseBody...), true
}

type createAccountResponseIntegrity struct {
	Version      int    `json:"version"`
	Lifecycle    int    `json:"lifecycle"`
	OperationID  int64  `json:"operation_id"`
	PublicRef    string `json:"operation_ref"`
	Action       int    `json:"action"`
	State        int    `json:"state"`
	ResultKind   int    `json:"result_kind"`
	ResultGUID   int64  `json:"result_guid"`
	HTTPStatus   int    `json:"http_status"`
	MediaType    string `json:"media_type"`
	ResponseBody []byte `json:"response_body"`
}

func createAccountResponseHMAC(crypto *actionsecurity.Crypto, operation models.AdminOperation, lifecycle models.AdminOperationResponseLifecycle, resultGUID int64, status int, media string, body []byte) (string, bool) {
	if crypto == nil || operation.ID <= 0 || operation.PublicRef == "" || (lifecycle != models.OperationResponseActive && lifecycle != models.OperationResponseRedacted) || resultGUID <= 0 || status != http.StatusCreated || media != createAccountResponseMediaType || len(body) == 0 {
		return "", false
	}
	encoded, err := json.Marshal(createAccountResponseIntegrity{Version: models.OperationResponseIntegrityHMACV1, Lifecycle: int(lifecycle),
		OperationID: operation.ID, PublicRef: operation.PublicRef, Action: operation.Action, State: int(models.OperationSucceeded),
		ResultKind: int(models.ResultUser), ResultGUID: resultGUID, HTTPStatus: status, MediaType: media, ResponseBody: body})
	if err != nil {
		clear(encoded)
		return "", false
	}
	digest := crypto.ResponseDigest(encoded)
	clear(encoded)
	value := hex.EncodeToString(digest[:])
	clear(digest[:])
	return value, true
}

func matchingCreateAccountResponseHMAC(crypto *actionsecurity.Crypto, operation models.AdminOperation, response models.AdminOperationResponse) bool {
	if response.ResponseHMAC == nil || len(*response.ResponseHMAC) != sha256.Size*2 || response.TargetGUID <= 0 {
		return false
	}
	want, ok := createAccountResponseHMAC(crypto, operation, response.LifecycleState, response.TargetGUID, response.HTTPStatus, response.MediaType, response.ResponseBody)
	if !ok {
		return false
	}
	match := constantTimeOperationStringEqual(want, *response.ResponseHMAC)
	want = ""
	return match
}

func validCreateResponseOperation(operation models.AdminOperation, targetGUID int64) bool {
	if operation.ID <= 0 || operation.PublicRef == "" || operation.FinishedAt == nil || operation.ErrorCode != nil || targetGUID <= 0 {
		return false
	}
	switch operation.State {
	case models.OperationSucceeded:
		return operation.IsDeleted == 0 && operation.ResultKind != nil && *operation.ResultKind == models.ResultUser &&
			operation.ResultGUID != nil && *operation.ResultGUID == targetGUID && operation.ResultHTTPStatus != nil && *operation.ResultHTTPStatus == http.StatusCreated
	case models.OperationExpired:
		return operation.IsDeleted == 1 && operation.ResultKind == nil && operation.ResultGUID == nil && operation.ResultHTTPStatus == nil
	default:
		return false
	}
}

func redactCreatedAccountResponse(ctx context.Context, tx *gorm.DB, targetGUID, actorID, now int64, crypto *actionsecurity.Crypto) error {
	if !validDeleteWriterTransaction(ctx, tx) || targetGUID <= 0 || actorID <= 0 || now <= 0 || crypto == nil {
		return ErrActionOperationUnavailable
	}
	db := deleteWriterDB(ctx, tx)
	actions := []int{int(actionsecurity.ActionUsersCreateAdmin), int(actionsecurity.ActionUsersCreate)}
	var markers []models.AdminActionOutbox
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Unscoped().
		Select("id", "operation_id", "public_ref", "action", "state", "failure_code", "result_guid").
		Where("result_guid = ? AND state = ? AND action IN ?", targetGUID, models.OperationSucceeded, actions).
		Order("operation_id ASC").Find(&markers).Error; err != nil {
		return ErrActionOperationUnavailable
	}
	var responses []models.AdminOperationResponse
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Unscoped().
		Select("id", "guid", "created_at", "created_by", "updated_at", "updated_by", "is_deleted", "operation_id", "target_guid", "lifecycle_state", "integrity_version", "response_hmac", "http_status", "media_type", "body_sha256").
		Where("target_guid = ?", targetGUID).Order("operation_id ASC").Find(&responses).Error; err != nil {
		return ErrActionOperationUnavailable
	}
	if len(markers) == 0 && len(responses) == 0 {
		return nil
	}
	if len(markers) == 0 || len(markers) != len(responses) {
		return ErrActionOperationUnavailable
	}
	operationIDs := make([]int64, 0, len(markers))
	markerByOperation := make(map[int64]models.AdminActionOutbox, len(markers))
	for _, marker := range markers {
		if marker.ID <= 0 || marker.OperationID <= 0 || marker.PublicRef == "" || marker.State != models.OperationSucceeded || marker.FailureCode != nil ||
			marker.ResultGUID == nil || *marker.ResultGUID != targetGUID || (marker.Action != int(actionsecurity.ActionUsersCreate) && marker.Action != int(actionsecurity.ActionUsersCreateAdmin)) {
			return ErrActionOperationUnavailable
		}
		if _, duplicate := markerByOperation[marker.OperationID]; duplicate {
			return ErrActionOperationUnavailable
		}
		markerByOperation[marker.OperationID] = marker
		operationIDs = append(operationIDs, marker.OperationID)
	}
	var operations []models.AdminOperation
	if err := db.Clauses(clause.Locking{Strength: "UPDATE"}).Unscoped().
		Where("id IN ?", operationIDs).Order("id ASC").Find(&operations).Error; err != nil || len(operations) != len(operationIDs) {
		return ErrActionOperationUnavailable
	}
	operationByID := make(map[int64]models.AdminOperation, len(operations))
	for _, operation := range operations {
		marker, ok := markerByOperation[operation.ID]
		if !ok || marker.PublicRef != operation.PublicRef || marker.Action != operation.Action || !validCreateResponseOperation(operation, targetGUID) {
			return ErrActionOperationUnavailable
		}
		operationByID[operation.ID] = operation
	}
	for _, response := range responses {
		operation, ok := operationByID[response.OperationID]
		if !ok || response.ID <= 0 || response.TargetGUID != targetGUID || response.HTTPStatus != http.StatusCreated || response.MediaType != createAccountResponseMediaType ||
			response.IntegrityVersion != models.OperationResponseIntegrityHMACV1 || response.ResponseHMAC == nil {
			return ErrActionOperationUnavailable
		}
		body, ok := loadCreateAccountResponseBody(db, response.ID)
		if !ok {
			return ErrActionOperationUnavailable
		}
		response.ResponseBody = body
		if !matchingCreateAccountResponseDigest(response.ResponseBody, response.BodySHA256) {
			clear(body)
			return ErrActionOperationUnavailable
		}
		switch response.LifecycleState {
		case models.OperationResponseActive:
			if response.IsDeleted != 0 || response.CreatedAt <= 0 || response.UpdatedAt != response.CreatedAt || !matchingCreateAccountResponseHMAC(crypto, operation, response) {
				clear(body)
				return ErrActionOperationUnavailable
			}
		case models.OperationResponseRedacted:
			migrationSentinel := *response.ResponseHMAC == strings.Repeat("0", sha256.Size*2)
			if response.IsDeleted != 1 || response.UpdatedAt < response.CreatedAt || response.UpdatedBy == nil || !bytes.Equal(body, []byte("{}")) ||
				(!migrationSentinel && !matchingCreateAccountResponseHMAC(crypto, operation, response)) {
				clear(body)
				return ErrActionOperationUnavailable
			}
			clear(body)
			continue
		default:
			clear(body)
			return ErrActionOperationUnavailable
		}
		redactedBody := []byte("{}")
		redactedHMAC, sealed := createAccountResponseHMAC(crypto, operation, models.OperationResponseRedacted, targetGUID, http.StatusCreated, createAccountResponseMediaType, redactedBody)
		clear(body)
		if !sealed {
			clear(redactedBody)
			return ErrActionOperationUnavailable
		}
		updated := db.Unscoped().Model(&models.AdminOperationResponse{}).
			Where("id = ? AND operation_id = ? AND target_guid = ? AND lifecycle_state = ? AND is_deleted = 0 AND integrity_version = ?", response.ID, operation.ID, targetGUID, models.OperationResponseActive, models.OperationResponseIntegrityHMACV1).
			Updates(map[string]any{"lifecycle_state": models.OperationResponseRedacted, "integrity_version": models.OperationResponseIntegrityHMACV1,
				"response_hmac": redactedHMAC, "response_body": redactedBody, "body_sha256": redactedCreateResponseSHA256,
				"is_deleted": 1, "updated_at": now, "updated_by": actorID})
		redactedHMAC = ""
		clear(redactedBody)
		if updated.Error != nil || updated.RowsAffected != 1 {
			return ErrActionOperationUnavailable
		}
	}
	return nil
}

func matchingCreateAccountResponseDigest(body []byte, encoded string) bool {
	if len(encoded) != sha256.Size*2 {
		return false
	}
	want := make([]byte, sha256.Size)
	if _, err := hex.Decode(want, []byte(encoded)); err != nil {
		clear(want)
		return false
	}
	got := sha256.Sum256(body)
	match := subtle.ConstantTimeCompare(got[:], want) == 1
	clear(got[:])
	clear(want)
	return match
}

func validCreateAccountResponseBody(body []byte, publicRef string, action actionsecurity.Action, resultGUID int64) bool {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var response createAccountResponseBody
	if err := decoder.Decode(&response); err != nil {
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return false
	}
	canonical, err := json.Marshal(response)
	if err != nil || !bytes.Equal(canonical, body) || response.OperationRef != publicRef || response.User.Username == nil || *response.User.Username == "" ||
		response.User.Nickname != nil && *response.User.Nickname == "" || response.User.Email != nil || response.User.Group == nil || *response.User.Group == "" ||
		response.User.Status != models.UserStatusActive.String() || response.User.AuthVersion != 1 || response.User.LastLoginAt != nil {
		return false
	}
	parsedGUID, err := strconv.ParseInt(response.User.GUID, 10, 64)
	if err != nil || parsedGUID != resultGUID || strconv.FormatInt(parsedGUID, 10) != response.User.GUID {
		return false
	}
	if _, err := time.Parse(time.RFC3339Nano, response.User.CreatedAt); err != nil || !strings.HasSuffix(response.User.CreatedAt, "Z") {
		return false
	}
	if plan, ok := models.ParsePlanType(response.User.PlanType); !ok || (plan != models.PlanFree && plan != models.PlanProfessional && plan != models.PlanEnterprise) {
		return false
	}
	switch action {
	case actionsecurity.ActionUsersCreate:
		return response.User.Role == models.UserRoleUser.String() && response.PermissionsVersion == nil
	case actionsecurity.ActionUsersCreateAdmin:
		return response.User.Role == models.UserRoleAdmin.String() && response.PermissionsVersion != nil && *response.PermissionsVersion == "1"
	default:
		return false
	}
}
