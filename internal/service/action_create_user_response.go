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
)

const (
	createAccountResponseMediaType = "application/json"
	createAccountResponseBodyLimit = 4096
)

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

func persistCreateAccountResponse(ctx context.Context, tx *gorm.DB, operation models.AdminOperation, actorID, guid, now int64, user models.User, group models.BusinessGroup) error {
	if !validCreateWriterTransaction(ctx, tx) || operation.ID <= 0 || actorID <= 0 || guid <= 0 || now <= 0 || operation.PublicRef == "" {
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
	actor := actorID
	row := models.AdminOperationResponse{
		AuditFields: models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor},
		OperationID: operation.ID, HTTPStatus: http.StatusCreated, MediaType: createAccountResponseMediaType,
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
	if err := db.Where("public_ref = ? AND actor_user_id = ? AND actor_auth_version = ? AND action = ? AND is_deleted = 0", publicRef, actor.UserID, actor.AuthVersion, int(action)).Limit(2).Find(&operations).Error; err != nil || len(operations) != 1 {
		return nil, ErrActionOperationUnavailable
	}
	operation := operations[0]
	if operation.ID <= 0 || operation.PublicRef != publicRef || operation.State != models.OperationSucceeded || operation.FinishedAt == nil || operation.ErrorCode != nil ||
		operation.ResultKind == nil || *operation.ResultKind != models.ResultUser || operation.ResultGUID == nil || *operation.ResultGUID <= 0 ||
		operation.ResultHTTPStatus == nil || *operation.ResultHTTPStatus != http.StatusCreated {
		return nil, ErrActionOperationUnavailable
	}
	var responses []models.AdminOperationResponse
	if err := db.Where("operation_id = ? AND is_deleted = 0", operation.ID).Limit(2).Find(&responses).Error; err != nil || len(responses) != 1 {
		return nil, ErrActionOperationUnavailable
	}
	response := responses[0]
	if response.ID <= 0 || response.Guid <= 0 || response.OperationID != operation.ID || response.HTTPStatus != *operation.ResultHTTPStatus ||
		response.MediaType != createAccountResponseMediaType || len(response.ResponseBody) < 2 || len(response.ResponseBody) > createAccountResponseBodyLimit ||
		response.CreatedAt <= 0 || response.UpdatedAt != response.CreatedAt || response.IsDeleted != 0 || !matchingCreateAccountResponseDigest(response.ResponseBody, response.BodySHA256) ||
		!validCreateAccountResponseBody(response.ResponseBody, publicRef, action, *operation.ResultGUID) {
		return nil, ErrActionOperationUnavailable
	}
	return &PersistedActionResponse{HTTPStatus: response.HTTPStatus, MediaType: strings.Clone(response.MediaType), Body: append([]byte(nil), response.ResponseBody...)}, nil
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
