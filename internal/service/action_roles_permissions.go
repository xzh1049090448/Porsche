package service

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/authz"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
)

// RolePermissionExecution owns one reviewed A08 request. It contains only
// immutable request facts and pure dependencies; Task 5B supplies persistence.
type RolePermissionExecution struct {
	descriptor  actionsecurity.Descriptor
	intent      rolePermissionIntent
	nextGUID    func() int64
	clock       persistence.Clock
	requestHMAC string
	invariant   *rolePermissionValidatedInvariant
	state       *rolePermissionExecutionState
}

type rolePermissionIntent struct {
	Promote          *actionsecurity.PromoteIntent
	Demote           *actionsecurity.DemoteIntent
	PermissionsWrite *actionsecurity.PermissionsWriteIntent
}

type rolePermissionExecutionState struct {
	mu      sync.Mutex
	started bool
}

// rolePermissionValidatedInvariant exists only after the constructor has
// matched an exact canonical descriptor to its typed intent.
type rolePermissionValidatedInvariant struct {
	action      actionsecurity.Action
	targetGUID  int64
	requestHMAC string
}

// RolePermissionTargetSnapshot is the public state read under the target lock.
type RolePermissionTargetSnapshot struct {
	ActorGUID   int64
	TargetGUID  int64
	Role        models.UserRole
	Status      models.UserStatus
	AuthVersion int
	Deleted     bool
}

// RolePermissionPolicySnapshot is nil when no policy head exists.
type RolePermissionPolicySnapshot struct {
	PolicyVersion  int64
	CatalogVersion int
}

// RolePermissionTransitionPlan is the complete desired state for a real A08
// transition. DesiredRules is always an independently owned canonical slice.
type RolePermissionTransitionPlan struct {
	DesiredRole           models.UserRole
	NextAuthVersion       int
	ExpectedPolicyVersion int64
	NextPolicyVersion     int64
	CatalogVersion        int
	DesiredRules          []actionsecurity.PermissionOverrideIntent
}

// NewRolePermissionExecution accepts only the exact inactive canonical A08
// descriptor and its corresponding typed intent.
func NewRolePermissionExecution(
	descriptor actionsecurity.Descriptor,
	intent any,
	nextGUID func() int64,
	clock persistence.Clock,
	crypto *actionsecurity.Crypto,
) (*RolePermissionExecution, error) {
	if !validInactiveRolePermissionDescriptor(descriptor) || nextGUID == nil || operationInterfaceNil(clock) || crypto == nil {
		return nil, ErrActionOperationUnavailable
	}
	encoded, err := descriptor.Encode(intent)
	if err != nil || len(encoded) == 0 {
		clear(encoded)
		return nil, ErrActionOperationUnavailable
	}
	digest := crypto.IntentDigest(encoded)
	clear(encoded)
	requestHMAC := hex.EncodeToString(digest[:])
	clear(digest[:])

	owned, ok := ownRolePermissionIntent(descriptor.Action, intent)
	if !ok {
		return nil, ErrActionOperationUnavailable
	}
	targetGUID := owned.targetGUID()
	return &RolePermissionExecution{
		descriptor: descriptor, intent: owned, nextGUID: nextGUID, clock: clock,
		requestHMAC: requestHMAC,
		invariant:   &rolePermissionValidatedInvariant{action: descriptor.Action, targetGUID: targetGUID, requestHMAC: requestHMAC},
		state:       &rolePermissionExecutionState{},
	}, nil
}

func validInactiveRolePermissionDescriptor(descriptor actionsecurity.Descriptor) bool {
	if !isA08RolePermissionAction(descriptor.Action) || descriptor.Active || descriptor.Encode == nil {
		return false
	}
	for _, expected := range actionsecurity.InactiveActionDescriptors() {
		if expected.Action != descriptor.Action {
			continue
		}
		return descriptor.Name == expected.Name && descriptor.Capability == expected.Capability &&
			descriptor.RootOnly == expected.RootOnly && descriptor.RootOnly &&
			descriptor.RequiresTicket == expected.RequiresTicket && descriptor.RequiresTicket &&
			descriptor.Active == expected.Active && descriptor.TargetKind == expected.TargetKind &&
			descriptor.TargetKind == actionsecurity.TargetUser && expected.Encode != nil &&
			reflect.ValueOf(descriptor.Encode).Pointer() == reflect.ValueOf(expected.Encode).Pointer()
	}
	return false
}

func ownRolePermissionIntent(action actionsecurity.Action, value any) (rolePermissionIntent, bool) {
	switch action {
	case actionsecurity.ActionUsersPromote:
		intent, ok := value.(actionsecurity.PromoteIntent)
		if !ok {
			return rolePermissionIntent{}, false
		}
		intent.Reason = strings.Clone(strings.TrimSpace(intent.Reason))
		intent.Overrides = ownCanonicalRolePermissionRules(intent.Overrides)
		return rolePermissionIntent{Promote: &intent}, true
	case actionsecurity.ActionUsersDemote:
		intent, ok := value.(actionsecurity.DemoteIntent)
		if !ok {
			return rolePermissionIntent{}, false
		}
		intent.Reason = strings.Clone(strings.TrimSpace(intent.Reason))
		return rolePermissionIntent{Demote: &intent}, true
	case actionsecurity.ActionUsersPermissionsWrite:
		intent, ok := value.(actionsecurity.PermissionsWriteIntent)
		if !ok {
			return rolePermissionIntent{}, false
		}
		intent.Reason = strings.Clone(strings.TrimSpace(intent.Reason))
		intent.Overrides = ownCanonicalRolePermissionRules(intent.Overrides)
		return rolePermissionIntent{PermissionsWrite: &intent}, true
	default:
		return rolePermissionIntent{}, false
	}
}

func ownCanonicalRolePermissionRules(source []actionsecurity.PermissionOverrideIntent) []actionsecurity.PermissionOverrideIntent {
	owned := append([]actionsecurity.PermissionOverrideIntent(nil), source...)
	for index := range owned {
		owned[index].Capability = strings.Clone(owned[index].Capability)
	}
	sort.Slice(owned, func(i, j int) bool { return owned[i].Capability < owned[j].Capability })
	if owned == nil {
		return make([]actionsecurity.PermissionOverrideIntent, 0)
	}
	return owned
}

func (execution *RolePermissionExecution) String() string {
	if execution == nil {
		return "RolePermissionExecution<nil>"
	}
	return "RolePermissionExecution{Action:" + execution.descriptor.Name + ",TargetGUID:" + strconv.FormatInt(execution.targetGUID(), 10) + "}"
}

func (execution *RolePermissionExecution) GoString() string { return execution.String() }

func (execution RolePermissionExecution) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, (&execution).String())
}

func (RolePermissionExecution) MarshalJSON() ([]byte, error) { return json.Marshal(struct{}{}) }

// Begin reserves this execution for one consumer invocation. Shallow copies
// share the guard, so retries cannot run the reviewed mutation twice.
func (execution *RolePermissionExecution) Begin() error {
	if execution == nil || execution.state == nil {
		return ErrActionOperationUnavailable
	}
	execution.state.mu.Lock()
	defer execution.state.mu.Unlock()
	if execution.state.started {
		return ErrActionOperationUnavailable
	}
	execution.state.started = true
	return nil
}

func (execution *RolePermissionExecution) targetGUID() int64 {
	if execution == nil {
		return 0
	}
	switch {
	case execution.intent.Promote != nil:
		return execution.intent.Promote.TargetGUID
	case execution.intent.Demote != nil:
		return execution.intent.Demote.TargetGUID
	case execution.intent.PermissionsWrite != nil:
		return execution.intent.PermissionsWrite.TargetGUID
	default:
		return 0
	}
}

func (intent rolePermissionIntent) targetGUID() int64 {
	switch {
	case intent.Promote != nil:
		return intent.Promote.TargetGUID
	case intent.Demote != nil:
		return intent.Demote.TargetGUID
	case intent.PermissionsWrite != nil:
		return intent.PermissionsWrite.TargetGUID
	default:
		return 0
	}
}

func (execution *RolePermissionExecution) validLocalInvariant() bool {
	if execution == nil || execution.invariant == nil || execution.state == nil ||
		execution.invariant.targetGUID <= 0 || execution.invariant.requestHMAC == "" ||
		execution.invariant.requestHMAC != execution.requestHMAC || execution.invariant.targetGUID != execution.intent.targetGUID() {
		return false
	}
	switch execution.invariant.action {
	case actionsecurity.ActionUsersPromote:
		return execution.intent.Promote != nil && execution.intent.Demote == nil && execution.intent.PermissionsWrite == nil
	case actionsecurity.ActionUsersDemote:
		return execution.intent.Promote == nil && execution.intent.Demote != nil && execution.intent.PermissionsWrite == nil
	case actionsecurity.ActionUsersPermissionsWrite:
		return execution.intent.Promote == nil && execution.intent.Demote == nil && execution.intent.PermissionsWrite != nil
	default:
		return false
	}
}

// PlanTransition computes the complete next role and policy state. It neither
// starts the execution nor retains or mutates caller-owned snapshots.
func (execution *RolePermissionExecution) PlanTransition(
	target RolePermissionTargetSnapshot,
	policy *RolePermissionPolicySnapshot,
	activeRules []actionsecurity.PermissionOverrideIntent,
) (*RolePermissionTransitionPlan, *models.AdminOperationFailure) {
	if !execution.validLocalInvariant() {
		return nil, rolePermissionFailure(models.FailureConsumerValidation)
	}
	if target.ActorGUID <= 0 || target.ActorGUID == target.TargetGUID {
		return nil, rolePermissionFailure(models.FailureActionRejected)
	}
	if target.TargetGUID <= 0 || target.TargetGUID != execution.targetGUID() || target.Deleted ||
		(target.Status != models.UserStatusActive && target.Status != models.UserStatusDisabled) {
		return nil, rolePermissionFailure(models.FailureTargetStateConflict)
	}
	expectedAuth, expectedPolicy, catalog, desiredRole, desiredRules, eligibleRole := execution.transitionIntent()
	if target.Role != eligibleRole {
		return nil, rolePermissionFailure(models.FailureTargetStateConflict)
	}
	if target.AuthVersion != expectedAuth {
		return nil, rolePermissionFailure(models.FailureTargetVersionConflict)
	}
	if expectedAuth <= 0 || expectedAuth >= math.MaxInt32 {
		return nil, rolePermissionFailure(models.FailureConsumerValidation)
	}
	if catalog != models.PermissionCatalogVersion {
		return nil, rolePermissionFailure(models.FailurePolicyVersionConflict)
	}

	canonicalActive, valid := validateAndOwnRolePermissionRules(activeRules)
	if !valid {
		return nil, rolePermissionFailure(models.FailureConsumerValidation)
	}
	if policy == nil {
		if execution.invariant.action != actionsecurity.ActionUsersPromote || expectedPolicy != 0 || len(canonicalActive) != 0 {
			return nil, rolePermissionFailure(models.FailurePolicyVersionConflict)
		}
	} else if policy.PolicyVersion <= 0 || policy.PolicyVersion != expectedPolicy || policy.CatalogVersion != models.PermissionCatalogVersion {
		return nil, rolePermissionFailure(models.FailurePolicyVersionConflict)
	}
	if expectedPolicy == math.MaxInt64 {
		return nil, rolePermissionFailure(models.FailureConsumerValidation)
	}
	if execution.invariant.action == actionsecurity.ActionUsersPermissionsWrite && reflect.DeepEqual(canonicalActive, desiredRules) {
		return nil, rolePermissionFailure(models.FailureTargetStateConflict)
	}

	return &RolePermissionTransitionPlan{
		DesiredRole: desiredRole, NextAuthVersion: expectedAuth + 1,
		ExpectedPolicyVersion: expectedPolicy, NextPolicyVersion: expectedPolicy + 1,
		CatalogVersion: catalog, DesiredRules: ownCanonicalRolePermissionRules(desiredRules),
	}, nil
}

func (execution *RolePermissionExecution) transitionIntent() (int, int64, int, models.UserRole, []actionsecurity.PermissionOverrideIntent, models.UserRole) {
	switch execution.invariant.action {
	case actionsecurity.ActionUsersPromote:
		intent := execution.intent.Promote
		if intent == nil {
			return 0, 0, 0, 0, nil, 0
		}
		return intent.ExpectedAuthVersion, intent.ExpectedPermissionsVersion, intent.CatalogVersion, models.UserRoleAdmin, intent.Overrides, models.UserRoleUser
	case actionsecurity.ActionUsersDemote:
		intent := execution.intent.Demote
		if intent == nil {
			return 0, 0, 0, 0, nil, 0
		}
		return intent.ExpectedAuthVersion, intent.ExpectedPermissionsVersion, intent.CatalogVersion, models.UserRoleUser, []actionsecurity.PermissionOverrideIntent{}, models.UserRoleAdmin
	case actionsecurity.ActionUsersPermissionsWrite:
		intent := execution.intent.PermissionsWrite
		if intent == nil {
			return 0, 0, 0, 0, nil, 0
		}
		return intent.ExpectedAuthVersion, intent.ExpectedPermissionsVersion, intent.CatalogVersion, models.UserRoleAdmin, intent.Overrides, models.UserRoleAdmin
	default:
		return 0, 0, 0, 0, nil, 0
	}
}

func validateAndOwnRolePermissionRules(source []actionsecurity.PermissionOverrideIntent) ([]actionsecurity.PermissionOverrideIntent, bool) {
	definitions := make(map[string]authz.Definition)
	for _, definition := range authz.Catalog() {
		definitions[definition.Name] = definition
	}
	owned := ownCanonicalRolePermissionRules(source)
	for index, rule := range owned {
		definition, ok := definitions[rule.Capability]
		if !ok || definition.Unavailable || (rule.Effect != 2 && rule.Effect != 3) ||
			(rule.Effect == 2 && (!definition.Grantable || definition.RootOnly)) ||
			(index > 0 && owned[index-1].Capability == rule.Capability) {
			return nil, false
		}
	}
	return owned, true
}

func rolePermissionFailure(value models.AdminOperationFailure) *models.AdminOperationFailure {
	failure := value
	return &failure
}
