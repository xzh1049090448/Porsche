package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// CreateAccountRequestMetadata contains only the request facts already made
// safe by the HTTP boundary. It deliberately excludes headers and credentials.
type CreateAccountRequestMetadata struct {
	RequestID string
	TrustedIP string
}

// CreateAccountExecution owns one reviewed, normalized account creation and
// the sensitive password hash needed by its single transactional invocation.
type CreateAccountExecution struct {
	descriptor actionsecurity.Descriptor
	intent     actionsecurity.CreateAccountIntent
	metadata   CreateAccountRequestMetadata
	nextGUID   func() int64
	clock      persistence.Clock
	state      *createAccountExecutionState
}

type createAccountExecutionState struct {
	mu              sync.Mutex
	passwordHash    []byte
	consumerStarted bool
	auditStarted    bool
	resultRecorded  bool
	resultCommitted bool
	resultUser      models.User
	resultGroup     models.BusinessGroup
}

// NewCreateAccountExecution validates and takes immutable copies of all
// mutable request data. Callers retain responsibility for clearing their input
// hash; this execution clears its independently owned copy.
func NewCreateAccountExecution(
	descriptor actionsecurity.Descriptor,
	intent actionsecurity.CreateAccountIntent,
	passwordHash []byte,
	metadata CreateAccountRequestMetadata,
	nextGUID func() int64,
	clock persistence.Clock,
) (*CreateAccountExecution, error) {
	ownedHash := append([]byte(nil), passwordHash...)
	fail := func() (*CreateAccountExecution, error) {
		clear(ownedHash)
		clear(passwordHash)
		return nil, ErrActionOperationUnavailable
	}
	role, ok := createAccountDescriptorRole(descriptor)
	if !ok || nextGUID == nil || operationInterfaceNil(clock) || !validManagedCreatePasswordHash(ownedHash) || !validCreateRequestID(metadata.RequestID) {
		return fail()
	}
	address, err := netip.ParseAddr(metadata.TrustedIP)
	if err != nil {
		return fail()
	}
	username, err := NormalizeUsername(intent.Username)
	if err != nil || username != intent.Username || intent.Password != nil || intent.Role != role.String() ||
		(intent.Role == models.UserRoleUser.String() && len(intent.Overrides) != 0) ||
		len(intent.AllowedModels) != 0 || intent.DailyCallLimit != 100 ||
		(intent.PlanType != int(models.PlanFree) && intent.PlanType != int(models.PlanProfessional) && intent.PlanType != int(models.PlanEnterprise)) {
		return fail()
	}
	var nickname *string
	if intent.Nickname != nil {
		normalized, err := NormalizeManagedUserNickname(*intent.Nickname)
		if err != nil || normalized != *intent.Nickname {
			return fail()
		}
		value := strings.Clone(normalized)
		nickname = &value
	}
	var groupGUID *int64
	if intent.GroupGUID != nil {
		if *intent.GroupGUID <= 0 {
			return fail()
		}
		value := *intent.GroupGUID
		groupGUID = &value
	}
	allowedModels := make([]string, 0)
	overrides := append([]actionsecurity.PermissionOverrideIntent(nil), intent.Overrides...)
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Capability < overrides[j].Capability })
	for i := range overrides {
		overrides[i].Capability = strings.Clone(overrides[i].Capability)
		if !validCreateOverride(overrides[i]) || (i > 0 && overrides[i-1].Capability == overrides[i].Capability) {
			return fail()
		}
	}
	owned := actionsecurity.CreateAccountIntent{
		Username: strings.Clone(intent.Username), Nickname: nickname, Role: strings.Clone(intent.Role), GroupGUID: groupGUID,
		PlanType: intent.PlanType, AllowedModels: allowedModels, DailyCallLimit: intent.DailyCallLimit, Overrides: overrides,
	}
	return &CreateAccountExecution{
		descriptor: descriptor,
		intent:     owned,
		metadata:   CreateAccountRequestMetadata{RequestID: strings.Clone(metadata.RequestID), TrustedIP: strings.Clone(address.String())},
		nextGUID:   nextGUID,
		clock:      clock,
		state:      &createAccountExecutionState{passwordHash: ownedHash},
	}, nil
}

func validManagedCreatePasswordHash(value []byte) bool {
	if len(value) == 0 || len(value) > 255 || !utf8.Valid(value) {
		return false
	}
	parts := bytes.Split(value, []byte("$"))
	if len(parts) != 6 || len(parts[0]) != 0 || !bytes.Equal(parts[1], []byte("argon2id")) ||
		!bytes.Equal(parts[2], []byte("v=19")) || !bytes.Equal(parts[3], []byte("m=65536,t=3,p=4")) {
		return false
	}
	encoding := base64.RawStdEncoding.Strict()
	salt := make([]byte, encoding.DecodedLen(len(parts[4])))
	digest := make([]byte, encoding.DecodedLen(len(parts[5])))
	saltLength, saltErr := encoding.Decode(salt, parts[4])
	digestLength, digestErr := encoding.Decode(digest, parts[5])
	valid := saltErr == nil && digestErr == nil && saltLength == 16 && digestLength == 32
	clear(salt)
	clear(digest)
	return valid
}

func createAccountDescriptorRole(descriptor actionsecurity.Descriptor) (models.UserRole, bool) {
	var role models.UserRole
	switch descriptor.Action {
	case actionsecurity.ActionUsersCreate:
		role = models.UserRoleUser
	case actionsecurity.ActionUsersCreateAdmin:
		role = models.UserRoleAdmin
	default:
		return 0, false
	}
	var expected actionsecurity.Descriptor
	found := false
	for _, candidate := range actionsecurity.FutureActionDescriptors() {
		if candidate.Action == descriptor.Action {
			expected, found = candidate, true
			break
		}
	}
	if !found || descriptor.Name != expected.Name || descriptor.Capability != expected.Capability ||
		descriptor.RootOnly != expected.RootOnly || descriptor.RequiresTicket != expected.RequiresTicket ||
		descriptor.Active != expected.Active || descriptor.TargetKind != expected.TargetKind || descriptor.Encode == nil ||
		expected.Encode == nil || reflect.ValueOf(descriptor.Encode).Pointer() != reflect.ValueOf(expected.Encode).Pointer() {
		return 0, false
	}
	return role, true
}

func validCreateOverride(override actionsecurity.PermissionOverrideIntent) bool {
	if override.Capability == "" || !utf8.ValidString(override.Capability) || (override.Effect != 2 && override.Effect != 3) {
		return false
	}
	for _, definition := range authz.Catalog() {
		if definition.Name == override.Capability {
			return definition.Grantable && !definition.RootOnly && !definition.Unavailable
		}
	}
	return false
}

func validCreateRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func (execution *CreateAccountExecution) String() string {
	if execution == nil {
		return "CreateAccountExecution<nil>"
	}
	return "CreateAccountExecution{Action:" + execution.descriptor.Name + ",PasswordHash:<redacted>}"
}

func (execution CreateAccountExecution) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, execution.String())
}

func (CreateAccountExecution) MarshalJSON() ([]byte, error) { return json.Marshal(struct{}{}) }

// ClearSecrets clears the execution-owned password digest. It is safe to call
// repeatedly and concurrently, including while ActionOperationService.Execute
// is returning from a pre-consumer failure.
func (execution *CreateAccountExecution) ClearSecrets() {
	if execution == nil || execution.state == nil {
		return
	}
	execution.state.mu.Lock()
	clear(execution.state.passwordHash)
	execution.state.passwordHash = nil
	execution.state.mu.Unlock()
}

var _ TransactionalActionConsumer = (*CreateAccountExecution)(nil)
var _ actionExecutionSecretClearer = (*CreateAccountExecution)(nil)
var _ actionExecutionCommitObserver = (*CreateAccountExecution)(nil)

// Execute creates the reviewed account and its authentication audit using only
// the caller-owned transaction. The execution is deliberately single-use even
// across shallow copies.
func (execution *CreateAccountExecution) Execute(ctx context.Context, tx *gorm.DB, operation models.AdminOperation) (TerminalOutcome, error) {
	if execution == nil || execution.state == nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	execution.state.mu.Lock()
	if execution.state.consumerStarted || len(execution.state.passwordHash) == 0 {
		execution.state.consumerStarted = true
		execution.state.mu.Unlock()
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	execution.state.consumerStarted = true
	passwordHash := execution.state.passwordHash
	execution.state.passwordHash = nil
	execution.state.mu.Unlock()
	defer clear(passwordHash)
	if !validCreateWriterTransaction(ctx, tx) {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}

	role, ok := createAccountDescriptorRole(execution.descriptor)
	now := execution.clock.NowMillis()
	if !ok || !validCreateAccountOperation(operation, execution.descriptor, now) || role.String() != execution.intent.Role {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	db := createWriterDB(ctx, tx)
	actor, evaluator, err := lockCreateAccountAuthority(db, operation, now)
	if err != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if evaluator.Create(role) != authz.Allowed {
		return createAccountFailure(models.FailureActionRejected, 403), nil
	}
	group, err := lockCreateAccountGroup(db, execution.intent.GroupGUID)
	if err != nil {
		if errors.Is(err, errCreateAccountGroupRejected) {
			return createAccountFailure(models.FailureConsumerValidation, 404), nil
		}
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if group.Key != "default" && !createAccountCapabilityAllowed(evaluator, actor, role, "users.group.change") {
		return createAccountFailure(models.FailureActionRejected, 403), nil
	}
	if models.PlanType(execution.intent.PlanType) != models.PlanFree && !createAccountCapabilityAllowed(evaluator, actor, role, "users.plan.change") {
		return createAccountFailure(models.FailureActionRejected, 403), nil
	}
	if conflict, err := lockCreateAccountUsername(db, execution.intent.Username); err != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	} else if conflict {
		return createAccountFailure(models.FailureConsumerValidation, 409), nil
	}

	userGUID := execution.nextGUID()
	if userGUID <= 0 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	actorID := actor.ID
	username := strings.Clone(execution.intent.Username)
	password := string(passwordHash)
	user := models.User{
		AuditFields: models.AuditFields{Guid: userGUID, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID},
		GroupID:     group.ID, Username: &username, PasswordHash: &password, Nickname: copyString(execution.intent.Nickname),
		PlanType: models.PlanType(execution.intent.PlanType), Status: models.UserStatusActive, Role: role, AuthVersion: 1,
		AllowedModels: append(models.JSONSlice{}, execution.intent.AllowedModels...), DailyCallLimit: execution.intent.DailyCallLimit,
		DailyCallsUsed: 0, TotalTokensUsed: 0,
	}
	created := db.Create(&user)
	password = ""
	user.PasswordHash = nil
	if created.Error != nil {
		if createAccountUsernameDuplicate(created.Error) {
			return createAccountFailure(models.FailureConsumerValidation, 409), nil
		}
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if created.RowsAffected != 1 || user.ID <= 0 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	if role == models.UserRoleAdmin {
		if err := execution.createAdminPermissionPolicy(db, user.ID, actorID, now); err != nil {
			return TerminalOutcome{}, err
		}
	}
	auditGUID := execution.nextGUID()
	if auditGUID <= 0 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	method := models.LoginMethodPassword
	ip := strings.Clone(execution.metadata.TrustedIP)
	audit := models.AuthAuditEvent{
		AuditFields: models.AuditFields{Guid: auditGUID, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID},
		UserID:      &user.ID, EventType: models.AuthAuditEventRegistered, LoginMethod: &method, IP: &ip,
	}
	created = db.Create(&audit)
	if created.Error != nil || created.RowsAffected != 1 {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	// The response is a one-to-one child of the operation and reuses its
	// snowflake GUID as the stable internal snapshot identity.
	if persistCreateAccountResponse(ctx, tx, operation, actorID, operation.Guid, now, user, group) != nil {
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	execution.state.mu.Lock()
	if execution.state.resultRecorded {
		execution.state.mu.Unlock()
		return TerminalOutcome{}, ErrActionOperationUnavailable
	}
	execution.state.resultUser = copyCreateAccountUser(user)
	execution.state.resultGroup = group
	execution.state.resultRecorded = true
	execution.state.mu.Unlock()
	resultGUID := userGUID
	return TerminalOutcome{HTTPStatus: 201, ResultKind: models.ResultUser, ResultGUID: &resultGUID}, nil
}

func (execution *CreateAccountExecution) createAdminPermissionPolicy(tx *gorm.DB, userID, actorID, now int64) error {
	headGUID := execution.nextGUID()
	if headGUID <= 0 {
		return ErrActionOperationUnavailable
	}
	head := models.PermissionPolicyHead{
		AuditFields:    models.AuditFields{Guid: headGUID, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID},
		UserID:         userID,
		PolicyVersion:  1,
		CatalogVersion: models.PermissionCatalogVersion,
		RuleCount:      len(execution.intent.Overrides),
	}
	created := tx.Create(&head)
	if created.Error != nil || created.RowsAffected != 1 || head.ID <= 0 {
		return ErrActionOperationUnavailable
	}
	for _, override := range execution.intent.Overrides {
		capability, ok := models.PermissionCapabilityCode(override.Capability)
		if !ok || !validCreateOverride(override) {
			return ErrActionOperationUnavailable
		}
		guid := execution.nextGUID()
		if guid <= 0 {
			return ErrActionOperationUnavailable
		}
		row := models.PermissionOverride{
			AuditFields:   models.AuditFields{Guid: guid, CreatedAt: now, CreatedBy: &actorID, UpdatedAt: now, UpdatedBy: &actorID},
			UserID:        userID,
			PolicyVersion: 1,
			Capability:    capability,
			Effect:        override.Effect,
		}
		created = tx.Create(&row)
		if created.Error != nil || created.RowsAffected != 1 || row.ID <= 0 {
			return ErrActionOperationUnavailable
		}
	}
	return nil
}

func validCreateAccountOperation(operation models.AdminOperation, descriptor actionsecurity.Descriptor, now int64) bool {
	if now <= 0 || operation.ID <= 0 || operation.Guid <= 0 || operation.CreatedAt <= 0 || operation.UpdatedAt < operation.CreatedAt || operation.UpdatedAt > now ||
		operation.CreatedBy == nil || *operation.CreatedBy != operation.ActorUserID || operation.UpdatedBy == nil || *operation.UpdatedBy != operation.ActorUserID ||
		operation.IsDeleted != 0 || operation.ActorUserID <= 0 || operation.ActorAuthVersion <= 0 || operation.ActorAuthVersion > math.MaxInt32 ||
		operation.SessionID <= 0 || operation.Action != int(descriptor.Action) || operation.State != models.OperationProcessing || operation.QueryExpiresAt <= now {
		return false
	}
	if descriptor.Action == actionsecurity.ActionUsersCreate {
		if operation.VerificationID != nil {
			return false
		}
	} else if descriptor.Action == actionsecurity.ActionUsersCreateAdmin {
		if operation.VerificationID == nil || *operation.VerificationID <= 0 {
			return false
		}
	} else {
		return false
	}
	_, err := actionsecurity.ParsePublicRef(operation.PublicRef)
	return err == nil
}

func lockCreateAccountAuthority(tx *gorm.DB, operation models.AdminOperation, now int64) (models.User, *authz.Evaluator, error) {
	var actor models.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "role", "status", "is_deleted", "auth_version").
		Where("id = ?", operation.ActorUserID).First(&actor).Error; err != nil || actor.ID != operation.ActorUserID || actor.Guid <= 0 ||
		actor.IsDeleted != 0 || actor.Status != models.UserStatusActive || actor.AuthVersion != operation.ActorAuthVersion ||
		(actor.Role != models.UserRoleAdmin && actor.Role != models.UserRoleRoot) {
		return models.User{}, nil, ErrActionOperationUnavailable
	}
	var sessions []models.Session
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "user_id", "session_version", "expires_at", "revoked_at", "is_deleted").
		Where("id = ?", operation.SessionID).Limit(2).Find(&sessions).Error; err != nil || len(sessions) != 1 {
		return models.User{}, nil, ErrActionOperationUnavailable
	}
	session := sessions[0]
	if session.ID != operation.SessionID || session.Guid <= 0 || session.UserID != actor.ID || session.SessionVersion <= 0 ||
		session.ExpiresAt <= now || session.RevokedAt != nil || session.IsDeleted != 0 {
		return models.User{}, nil, ErrActionOperationUnavailable
	}
	var heads []models.PermissionPolicyHead
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "is_deleted", "policy_version", "catalog_version", "rule_count").
		Where("user_id = ?", actor.ID).Order("id ASC").Limit(2).Find(&heads).Error; err != nil || len(heads) > 1 {
		return models.User{}, nil, ErrActionOperationUnavailable
	}
	var storedRules []models.PermissionOverride
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "is_deleted", "policy_version", "capability", "effect").
		Where("user_id = ?", actor.ID).Order("id ASC").Limit(len(authz.Catalog()) + 1).Find(&storedRules).Error; err != nil {
		return models.User{}, nil, ErrActionOperationUnavailable
	}
	if len(heads) == 0 {
		if len(storedRules) != 0 {
			return models.User{}, nil, ErrActionOperationUnavailable
		}
		evaluator, err := authz.NewEvaluator(actionAccount(actor), nil)
		return actor, evaluator, err
	}
	head := heads[0]
	if head.ID <= 0 || head.Guid <= 0 || head.IsDeleted != 0 || head.PolicyVersion <= 0 || head.CatalogVersion != models.PermissionCatalogVersion ||
		head.RuleCount < 0 || head.RuleCount != len(storedRules) || head.RuleCount > len(authz.Catalog()) {
		return models.User{}, nil, ErrActionOperationUnavailable
	}
	rules := make([]authz.Override, 0, len(storedRules))
	seen := make(map[string]bool, len(storedRules))
	for _, row := range storedRules {
		name, nameOK := models.PermissionCapabilityName(row.Capability)
		effect, effectOK := models.PermissionEffectName(row.Effect)
		if row.ID <= 0 || row.Guid <= 0 || row.IsDeleted != 0 || row.PolicyVersion != head.PolicyVersion || !nameOK || !effectOK || seen[name] {
			return models.User{}, nil, ErrActionOperationUnavailable
		}
		seen[name] = true
		rules = append(rules, authz.Override{Capability: name, Effect: authz.Effect(effect)})
	}
	evaluator, err := authz.NewEvaluator(actionAccount(actor), rules)
	if err != nil {
		return models.User{}, nil, ErrActionOperationUnavailable
	}
	return actor, evaluator, nil
}

var errCreateAccountGroupRejected = errors.New("create account group rejected")

func lockCreateAccountGroup(tx *gorm.DB, guid *int64) (models.BusinessGroup, error) {
	var groups []models.BusinessGroup
	query := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "group_key", "display_name", "status", "is_deleted").Order("id ASC").Limit(2)
	if guid == nil {
		query = query.Where("group_key = ? AND is_deleted = 0", "default")
	} else {
		query = query.Where("guid = ?", *guid)
	}
	if err := query.Find(&groups).Error; err != nil {
		return models.BusinessGroup{}, err
	}
	if len(groups) != 1 {
		return models.BusinessGroup{}, errCreateAccountGroupRejected
	}
	group := groups[0]
	if group.ID <= 0 || group.Guid <= 0 || group.Key == "" || !utf8.ValidString(group.Key) || len(group.Key) > 64 ||
		group.DisplayName == "" || !utf8.ValidString(group.DisplayName) || group.Status != models.BusinessGroupStatusActive || group.IsDeleted != 0 ||
		(guid == nil && group.Key != "default") || (guid != nil && group.Guid != *guid) {
		return models.BusinessGroup{}, errCreateAccountGroupRejected
	}
	return group, nil
}

func lockCreateAccountUsername(tx *gorm.DB, username string) (bool, error) {
	var rows []struct {
		ID        int64
		Guid      int64
		IsDeleted int
	}
	if err := tx.Model(&models.User{}).Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "guid", "is_deleted").Where("username = ?", username).Order("id ASC").Limit(2).Find(&rows).Error; err != nil {
		return false, err
	}
	return len(rows) != 0, nil
}

func createAccountCapabilityAllowed(evaluator *authz.Evaluator, actor models.User, role models.UserRole, capability string) bool {
	targetID, targetGUID := int64(1), int64(1)
	if actor.ID == targetID {
		targetID++
	}
	if actor.Guid == targetGUID {
		targetGUID++
	}
	target := authz.Account{ID: targetID, GUID: targetGUID, Role: role, Status: models.UserStatusActive}
	return evaluator.User(capability, target) == authz.Allowed
}

func createAccountFailure(failure models.AdminOperationFailure, status int) TerminalOutcome {
	value := failure
	return TerminalOutcome{Failure: &value, HTTPStatus: status}
}

func createAccountUsernameDuplicate(err error) bool {
	var mysqlError *mysqlDriver.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062 && strings.Contains(strings.ToLower(mysqlError.Message), "uk_users_username")
}

func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := strings.Clone(*value)
	return &copy
}

func copyCreateAccountUser(user models.User) models.User {
	copy := user
	copy.Username = copyString(user.Username)
	copy.Nickname = copyString(user.Nickname)
	copy.PasswordHash = nil
	copy.AllowedModels = append(models.JSONSlice(nil), user.AllowedModels...)
	return copy
}

// ResultUser returns an independent public projection after the consumer has
// successfully produced the user and registration audit writes.
func (execution *CreateAccountExecution) ResultUser() (*UserReadDTO, bool) {
	if execution == nil || execution.state == nil {
		return nil, false
	}
	execution.state.mu.Lock()
	if !execution.state.resultRecorded || !execution.state.resultCommitted {
		execution.state.mu.Unlock()
		return nil, false
	}
	user := copyCreateAccountUser(execution.state.resultUser)
	group := execution.state.resultGroup
	execution.state.mu.Unlock()
	result, err := ProjectCreatedUserRead(user, group)
	return result, err == nil
}

// actionCommitConfirmed is called only by ActionOperationService after its
// transaction runner has confirmed commit success.
func (execution *CreateAccountExecution) actionCommitConfirmed() {
	if execution == nil || execution.state == nil {
		return
	}
	execution.state.mu.Lock()
	if execution.state.resultRecorded {
		execution.state.resultCommitted = true
	}
	execution.state.mu.Unlock()
}
