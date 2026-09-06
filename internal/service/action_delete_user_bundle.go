package service

import (
	cryptorand "crypto/rand"
	"io"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
)

// UserDeleteActions is the complete internal users.delete service boundary.
// Callers must not expose any member unless construction of the whole bundle
// succeeds.
type UserDeleteActions struct {
	Verifications *ActionVerificationService
	Operations    *ActionOperationService
	Outbox        *AdminActionOutboxWriter
	NewExecution  func(actionsecurity.DeleteUserIntent) (*DeleteUserExecution, error)
}

// NewUserDeleteActions constructs the production users.delete services from
// one reviewed dependency set.
func NewUserDeleteActions(db *gorm.DB, authRedis *AuthRedis, crypto *actionsecurity.Crypto) (*UserDeleteActions, error) {
	return newUserDeleteActions(db, authRedis, crypto, actionsecurity.ActiveActionRegistry, persistence.SystemClock(), cryptorand.Reader, persistence.NextGUID)
}

func newUserDeleteActions(
	db *gorm.DB,
	authRedis *AuthRedis,
	crypto *actionsecurity.Crypto,
	activeRegistry func() []actionsecurity.Descriptor,
	clock persistence.Clock,
	random io.Reader,
	nextGUID func() int64,
) (*UserDeleteActions, error) {
	if db == nil || db.Statement == nil || operationInterfaceNil(db.Statement.ConnPool) || authRedis == nil ||
		redisClientIsNil(authRedis.client) || crypto == nil || activeRegistry == nil ||
		operationInterfaceNil(clock) || operationInterfaceNil(random) || nextGUID == nil {
		return nil, ErrActionVerificationUnavailable
	}
	descriptors := activeRegistry()
	var descriptor actionsecurity.Descriptor
	switch {
	case len(descriptors) == 1 && exactActiveUserDeleteDescriptor(descriptors[0]) && typedUserDeleteEncoder(descriptors[0].Encode):
		descriptor = descriptors[0]
	case exactActiveUserManagementDescriptors(descriptors):
		descriptor = descriptors[2]
	default:
		return nil, ErrActionVerificationUnavailable
	}
	resolverDescriptor := descriptor
	resolve := func(action actionsecurity.Action) (actionsecurity.Descriptor, bool) {
		if action != actionsecurity.ActionUsersDelete {
			return actionsecurity.Descriptor{}, false
		}
		return resolverDescriptor, true
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
	outbox, err := NewAdminActionOutboxWriter(nextGUID, clock)
	if err != nil {
		return nil, ErrActionVerificationUnavailable
	}
	executionDescriptor := descriptor
	return &UserDeleteActions{
		Verifications: verifications,
		Operations:    operations,
		Outbox:        outbox,
		NewExecution: func(intent actionsecurity.DeleteUserIntent) (*DeleteUserExecution, error) {
			return newDeleteUserExecution(executionDescriptor, intent, nextGUID, clock)
		},
	}, nil
}

func exactActiveUserDeleteDescriptor(descriptor actionsecurity.Descriptor) bool {
	return descriptor.Action == actionsecurity.ActionUsersDelete && descriptor.Name == "users.delete" &&
		descriptor.Capability == "users.delete" && !descriptor.RootOnly && descriptor.RequiresTicket && descriptor.Active &&
		descriptor.TargetKind == actionsecurity.TargetUser && descriptor.Encode != nil
}

func typedUserDeleteEncoder(encode func(any) ([]byte, error)) bool {
	if encode == nil {
		return false
	}
	encoded, err := encode(actionsecurity.DeleteUserIntent{TargetGUID: 1, ExpectedAuthVersion: 1, Reason: "registry validation"})
	valid := err == nil && len(encoded) > 0
	clear(encoded)
	wrong, wrongErr := encode(struct{}{})
	clear(wrong)
	return valid && wrongErr != nil
}
