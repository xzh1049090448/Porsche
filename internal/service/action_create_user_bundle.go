package service

import (
	cryptorand "crypto/rand"
	"io"
	"reflect"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

// UserManagementActions is the complete internal service boundary for the
// active users.create, users.create_admin, and users.delete action set.
// Callers must not expose any member unless construction of the whole bundle
// succeeds.
type UserManagementActions struct {
	Verifications      *ActionVerificationService
	Operations         *ActionOperationService
	DeleteOutbox       *AdminActionOutboxWriter
	CreateOutbox       *CreateAccountOutboxWriter
	NewDeleteExecution func(actionsecurity.DeleteUserIntent) (*DeleteUserExecution, error)
	NewCreateExecution func(actionsecurity.Action, actionsecurity.CreateAccountIntent, []byte, CreateAccountRequestMetadata) (*CreateAccountExecution, error)
}

// NewUserManagementActions constructs the production user-management services
// from one reviewed dependency set.
func NewUserManagementActions(db *gorm.DB, authRedis *AuthRedis, crypto *actionsecurity.Crypto) (*UserManagementActions, error) {
	return newUserManagementActions(db, authRedis, crypto, actionsecurity.ActiveActionRegistry, persistence.SystemClock(), cryptorand.Reader, persistence.NextGUID)
}

func newUserManagementActions(
	db *gorm.DB,
	authRedis *AuthRedis,
	crypto *actionsecurity.Crypto,
	activeRegistry func() []actionsecurity.Descriptor,
	clock persistence.Clock,
	random io.Reader,
	nextGUID func() int64,
) (*UserManagementActions, error) {
	if db == nil || db.Statement == nil || operationInterfaceNil(db.Statement.ConnPool) || authRedis == nil ||
		redisClientIsNil(authRedis.client) || crypto == nil || activeRegistry == nil ||
		operationInterfaceNil(clock) || operationInterfaceNil(random) || nextGUID == nil {
		return nil, ErrActionVerificationUnavailable
	}
	descriptors := activeRegistry()
	if !exactActiveUserManagementDescriptors(descriptors) {
		return nil, ErrActionVerificationUnavailable
	}
	owned := append([]actionsecurity.Descriptor(nil), descriptors...)
	resolve := func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		for _, descriptor := range owned {
			if descriptor.Action == action {
				return descriptor, true
			}
		}
		return actionsecurity.Descriptor{}, false
	}
	limiter, err := NewActionSecurityRedis(authRedis.client, crypto)
	if err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	verifications, err := newActionVerificationService(db, limiter, authRedis, crypto, resolve, clock, random, nextGUID)
	if err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	operations, err := newActionOperationService(db, limiter, authRedis, crypto, resolve, clock, random, nextGUID)
	if err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	deleteOutbox, err := NewAdminActionOutboxWriter(nextGUID, clock)
	if err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	createOutbox, err := NewCreateAccountOutboxWriter(nextGUID, clock)
	if err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	descriptorByAction := make(map[actionsecurity.Action]actionsecurity.Descriptor, len(owned))
	for _, descriptor := range owned {
		descriptorByAction[descriptor.Action] = descriptor
	}
	bundle := &UserManagementActions{
		Verifications: verifications,
		Operations:    operations,
		DeleteOutbox:  deleteOutbox,
		CreateOutbox:  createOutbox,
		NewDeleteExecution: func(intent actionsecurity.DeleteUserIntent) (*DeleteUserExecution, error) {
			return newDeleteUserExecution(descriptorByAction[actionsecurity.ActionUsersDelete], intent, nextGUID, clock, crypto)
		},
		NewCreateExecution: func(action actionsecurity.Action, intent actionsecurity.CreateAccountIntent, passwordHash []byte, metadata CreateAccountRequestMetadata) (*CreateAccountExecution, error) {
			descriptor, ok := descriptorByAction[action]
			if !ok || (action != actionsecurity.ActionUsersCreate && action != actionsecurity.ActionUsersCreateAdmin) {
				clear(passwordHash)
				return nil, ErrActionOperationUnavailable
			}
			return NewCreateAccountExecution(descriptor, intent, passwordHash, metadata, nextGUID, clock, crypto)
		},
	}
	if !completeUserManagementActions(bundle) {
		return nil, ErrActionVerificationUnavailable
	}
	return bundle, nil
}

func completeUserManagementActions(bundle *UserManagementActions) bool {
	return bundle != nil && bundle.Verifications != nil && bundle.Operations != nil &&
		bundle.DeleteOutbox != nil && bundle.CreateOutbox != nil &&
		bundle.NewDeleteExecution != nil && bundle.NewCreateExecution != nil
}

// DeleteActions returns the existing users.delete boundary backed by the same
// services, writer, and execution factory as this complete bundle.
func (bundle *UserManagementActions) DeleteActions() *UserDeleteActions {
	if !completeUserManagementActions(bundle) {
		return nil
	}
	return &UserDeleteActions{
		Verifications: bundle.Verifications,
		Operations:    bundle.Operations,
		Outbox:        bundle.DeleteOutbox,
		NewExecution:  bundle.NewDeleteExecution,
	}
}

func exactActiveUserManagementDescriptors(descriptors []actionsecurity.Descriptor) bool {
	expected := actionsecurity.FutureActionDescriptors()
	if len(descriptors) != 3 || len(expected) != len(descriptors) {
		return false
	}
	for index := range expected {
		got, want := descriptors[index], expected[index]
		if got.Action != want.Action || got.Name != want.Name || got.Capability != want.Capability ||
			got.RootOnly != want.RootOnly || got.RequiresTicket != want.RequiresTicket || !got.Active || !want.Active ||
			got.TargetKind != want.TargetKind || got.Encode == nil || want.Encode == nil ||
			reflect.ValueOf(got.Encode).Pointer() != reflect.ValueOf(want.Encode).Pointer() {
			return false
		}
	}
	return true
}
