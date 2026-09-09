package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestRolePermissionExecutionOwnsCanonicalPromoteIntentAndHMAC(t *testing.T) {
	descriptor := rolePermissionTestDescriptor(t, actionsecurity.ActionUsersPromote)
	overrides := []actionsecurity.PermissionOverrideIntent{
		{Capability: "users.sessions.read", Effect: 2},
		{Capability: "users.delete", Effect: 3},
	}
	intent := actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: models.PermissionCatalogVersion, Overrides: overrides, Reason: "  reviewed promotion  "}
	execution, err := NewRolePermissionExecution(descriptor, intent, func() int64 { return 7001 }, &createAccountTestClock{now: 8001}, createAccountTestCrypto(t))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := descriptor.Encode(intent)
	if err != nil {
		t.Fatal(err)
	}
	digest := createAccountTestCrypto(t).IntentDigest(encoded)
	clear(encoded)
	if execution.requestHMAC != fmt.Sprintf("%x", digest) || execution.intent.Promote == nil || execution.intent.Promote.Reason != "reviewed promotion" {
		t.Fatalf("execution did not own canonical intent: %#v", execution)
	}
	if got := execution.intent.Promote.Overrides; len(got) != 2 || got[0].Capability != "users.delete" || got[1].Capability != "users.sessions.read" {
		t.Fatalf("owned overrides not canonical: %#v", got)
	}
	overrides[0] = actionsecurity.PermissionOverrideIntent{Capability: "users.read", Effect: 3}
	intent.Reason = "changed"
	if execution.intent.Promote.Reason != "reviewed promotion" || execution.intent.Promote.Overrides[1].Capability != "users.sessions.read" {
		t.Fatal("caller mutation changed owned intent")
	}

	reordered := actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: models.PermissionCatalogVersion, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.delete", Effect: 3}, {Capability: "users.sessions.read", Effect: 2}}, Reason: "reviewed promotion"}
	second, err := NewRolePermissionExecution(descriptor, reordered, func() int64 { return 7002 }, &createAccountTestClock{now: 8002}, createAccountTestCrypto(t))
	if err != nil || second.requestHMAC != execution.requestHMAC {
		t.Fatalf("canonical order changed HMAC: %v", err)
	}
}

func TestRolePermissionExecutionHMACBindsEveryIntentField(t *testing.T) {
	tests := []struct {
		action actionsecurity.Action
		base   any
		mutate func(any) any
	}{
		{actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.PromoteIntent); v.TargetGUID++; return v }},
		{actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.PromoteIntent); v.ExpectedAuthVersion++; return v }},
		{actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any {
			v := value.(actionsecurity.PromoteIntent)
			v.ExpectedPermissionsVersion++
			return v
		}},
		{actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.PromoteIntent); v.CatalogVersion++; return v }},
		{actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any {
			v := value.(actionsecurity.PromoteIntent)
			v.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}
			return v
		}},
		{actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.PromoteIntent); v.Reason = "other"; return v }},
		{actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "reason"}, func(value any) any {
			v := value.(actionsecurity.DemoteIntent)
			v.ExpectedPermissionsVersion++
			return v
		}},
		{actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.DemoteIntent); v.TargetGUID++; return v }},
		{actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.DemoteIntent); v.ExpectedAuthVersion++; return v }},
		{actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.DemoteIntent); v.CatalogVersion++; return v }},
		{actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.DemoteIntent); v.Reason = "other"; return v }},
		{actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.PermissionsWriteIntent); v.TargetGUID++; return v }},
		{actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any {
			v := value.(actionsecurity.PermissionsWriteIntent)
			v.ExpectedAuthVersion++
			return v
		}},
		{actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any {
			v := value.(actionsecurity.PermissionsWriteIntent)
			v.ExpectedPermissionsVersion++
			return v
		}},
		{actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.PermissionsWriteIntent); v.CatalogVersion++; return v }},
		{actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any {
			v := value.(actionsecurity.PermissionsWriteIntent)
			v.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}}
			return v
		}},
		{actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any {
			v := value.(actionsecurity.PermissionsWriteIntent)
			v.Overrides = []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}
			return v
		}},
		{actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 2}}, Reason: "reason"}, func(value any) any { v := value.(actionsecurity.PermissionsWriteIntent); v.Reason = "other"; return v }},
	}
	for index, tc := range tests {
		descriptor := rolePermissionTestDescriptor(t, tc.action)
		first, err := NewRolePermissionExecution(descriptor, tc.base, func() int64 { return 1 }, &createAccountTestClock{now: 1}, createAccountTestCrypto(t))
		if err != nil {
			t.Fatalf("case %d base: %v", index, err)
		}
		second, err := NewRolePermissionExecution(descriptor, tc.mutate(tc.base), func() int64 { return 1 }, &createAccountTestClock{now: 1}, createAccountTestCrypto(t))
		if err != nil {
			t.Fatalf("case %d mutation: %v", index, err)
		}
		if first.requestHMAC == second.requestHMAC {
			t.Fatalf("case %d field was not HMAC-bound", index)
		}
	}
}

func TestRolePermissionExecutionRejectsDescriptorIntentAndDependencyMismatch(t *testing.T) {
	valid := rolePermissionTestDescriptor(t, actionsecurity.ActionUsersPromote)
	intent := actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "reason"}
	mutations := []func(*actionsecurity.Descriptor){
		func(d *actionsecurity.Descriptor) { d.Action = actionsecurity.ActionUsersDemote },
		func(d *actionsecurity.Descriptor) { d.Name = "users.demote" },
		func(d *actionsecurity.Descriptor) { d.Capability = "users.demote" },
		func(d *actionsecurity.Descriptor) { d.RootOnly = false },
		func(d *actionsecurity.Descriptor) { d.RequiresTicket = false },
		func(d *actionsecurity.Descriptor) { d.Active = true },
		func(d *actionsecurity.Descriptor) { d.TargetKind = actionsecurity.TargetNone },
		func(d *actionsecurity.Descriptor) {
			d.Encode = func(any) ([]byte, error) { return []byte("forged"), nil }
		},
	}
	for index, mutate := range mutations {
		descriptor := valid
		mutate(&descriptor)
		if got, err := NewRolePermissionExecution(descriptor, intent, func() int64 { return 1 }, &createAccountTestClock{now: 1}, createAccountTestCrypto(t)); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
			t.Fatalf("descriptor mutation %d accepted: %#v %v", index, got, err)
		}
	}
	invalid := []struct {
		descriptor actionsecurity.Descriptor
		intent     any
		next       func() int64
		clock      *createAccountTestClock
		cryptoNil  bool
	}{
		{valid, actionsecurity.DemoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 1, CatalogVersion: 1, Reason: "reason"}, func() int64 { return 1 }, &createAccountTestClock{now: 1}, false},
		{valid, intent, nil, &createAccountTestClock{now: 1}, false},
		{valid, intent, func() int64 { return 1 }, nil, false},
		{valid, intent, func() int64 { return 1 }, &createAccountTestClock{now: 1}, true},
	}
	for index, tc := range invalid {
		crypto := createAccountTestCrypto(t)
		if tc.cryptoNil {
			crypto = nil
		}
		if got, err := NewRolePermissionExecution(tc.descriptor, tc.intent, tc.next, tc.clock, crypto); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
			t.Fatalf("invalid case %d accepted: %#v %v", index, got, err)
		}
	}
	invalidRules := [][]actionsecurity.PermissionOverrideIntent{
		{{Capability: "users.read", Effect: 1}},
		{{Capability: "missing", Effect: 3}},
		{{Capability: "users.quota.adjust", Effect: 3}},
		{{Capability: "users.promote", Effect: 2}},
		{{Capability: "users.read", Effect: 2}, {Capability: "users.read", Effect: 3}},
	}
	for index, rules := range invalidRules {
		candidate := intent
		candidate.Overrides = rules
		if got, err := NewRolePermissionExecution(valid, candidate, func() int64 { return 1 }, &createAccountTestClock{now: 1}, createAccountTestCrypto(t)); got != nil || !errors.Is(err, ErrActionOperationUnavailable) {
			t.Fatalf("invalid rules %d accepted: %#v %v", index, got, err)
		}
	}
}

func TestRolePermissionExecutionRedactsAndStartsOnlyOnceAcrossCopies(t *testing.T) {
	execution := rolePermissionTestExecution(t, actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}, Reason: "private reason"})
	for _, rendered := range []string{fmt.Sprint(execution), fmt.Sprintf("%+v", execution), fmt.Sprintf("%#v", execution)} {
		if strings.Contains(rendered, "private reason") || strings.Contains(rendered, execution.requestHMAC) || strings.Contains(rendered, "users.read") {
			t.Fatalf("execution leaked through formatting: %s", rendered)
		}
	}
	encoded, err := json.Marshal(execution)
	if err != nil || string(encoded) != "{}" {
		t.Fatalf("marshal = %s, %v", encoded, err)
	}
	copied := *execution
	var successes atomic.Int32
	var group sync.WaitGroup
	for _, candidate := range []*RolePermissionExecution{execution, &copied} {
		group.Add(1)
		go func(value *RolePermissionExecution) {
			defer group.Done()
			if value.Begin() == nil {
				successes.Add(1)
			}
		}(candidate)
	}
	group.Wait()
	if successes.Load() != 1 || !execution.Started() || !copied.Started() {
		t.Fatalf("single-use state = successes %d, started %t/%t", successes.Load(), execution.Started(), copied.Started())
	}
}

func TestRolePermissionPlanThreeActions(t *testing.T) {
	active := []actionsecurity.PermissionOverrideIntent{{Capability: "users.delete", Effect: 3}}
	tests := []struct {
		name       string
		action     actionsecurity.Action
		intent     any
		targetRole models.UserRole
		head       *RolePermissionPolicySnapshot
		active     []actionsecurity.PermissionOverrideIntent
		want       RolePermissionTransitionPlan
	}{
		{"promote no head", actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 0, CatalogVersion: 1, Reason: "reason"}, models.UserRoleUser, nil, nil, RolePermissionTransitionPlan{DesiredRole: models.UserRoleAdmin, NextAuthVersion: 8, ExpectedPolicyVersion: 0, NextPolicyVersion: 1, CatalogVersion: 1, DesiredRules: []actionsecurity.PermissionOverrideIntent{}}},
		{"promote existing", actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}}, Reason: "reason"}, models.UserRoleUser, &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 1}, active, RolePermissionTransitionPlan{DesiredRole: models.UserRoleAdmin, NextAuthVersion: 8, ExpectedPolicyVersion: 2, NextPolicyVersion: 3, CatalogVersion: 1, DesiredRules: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}}}},
		{"demote disabled", actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "reason"}, models.UserRoleAdmin, &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 1}, active, RolePermissionTransitionPlan{DesiredRole: models.UserRoleUser, NextAuthVersion: 8, ExpectedPolicyVersion: 2, NextPolicyVersion: 3, CatalogVersion: 1, DesiredRules: []actionsecurity.PermissionOverrideIntent{}}},
		{"permissions replace", actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}, {Capability: "users.delete", Effect: 3}}, Reason: "reason"}, models.UserRoleAdmin, &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 1}, active, RolePermissionTransitionPlan{DesiredRole: models.UserRoleAdmin, NextAuthVersion: 8, ExpectedPolicyVersion: 2, NextPolicyVersion: 3, CatalogVersion: 1, DesiredRules: []actionsecurity.PermissionOverrideIntent{{Capability: "users.delete", Effect: 3}, {Capability: "users.sessions.read", Effect: 2}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			execution := rolePermissionTestExecution(t, tc.action, tc.intent)
			status := models.UserStatusActive
			if tc.name == "demote disabled" {
				status = models.UserStatusDisabled
			}
			plan, failure := execution.PlanTransition(RolePermissionTargetSnapshot{ActorGUID: 50, TargetGUID: 91, Role: tc.targetRole, Status: status, AuthVersion: 7}, tc.head, tc.active)
			if failure != nil || plan == nil || !reflect.DeepEqual(*plan, tc.want) {
				t.Fatalf("plan = %#v, failure = %#v, want %#v", plan, failure, tc.want)
			}
		})
	}
}

func TestRolePermissionPlanRejectsStateVersionCatalogOverflowAndInvalidRules(t *testing.T) {
	baseIntent := actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}, Reason: "reason"}
	baseTarget := RolePermissionTargetSnapshot{ActorGUID: 50, TargetGUID: 91, Role: models.UserRoleAdmin, Status: models.UserStatusDisabled, AuthVersion: 7}
	baseHead := &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 1}
	tests := []struct {
		name   string
		intent actionsecurity.PermissionsWriteIntent
		target RolePermissionTargetSnapshot
		head   *RolePermissionPolicySnapshot
		active []actionsecurity.PermissionOverrideIntent
		want   models.AdminOperationFailure
	}{
		{"self", baseIntent, withRoleTarget(baseTarget, func(v *RolePermissionTargetSnapshot) { v.ActorGUID = v.TargetGUID }), baseHead, nil, models.FailureActionRejected},
		{"root", baseIntent, withRoleTarget(baseTarget, func(v *RolePermissionTargetSnapshot) { v.Role = models.UserRoleRoot }), baseHead, nil, models.FailureTargetStateConflict},
		{"deleted", baseIntent, withRoleTarget(baseTarget, func(v *RolePermissionTargetSnapshot) { v.Deleted = true }), baseHead, nil, models.FailureTargetStateConflict},
		{"wrong status", baseIntent, withRoleTarget(baseTarget, func(v *RolePermissionTargetSnapshot) { v.Status = 99 }), baseHead, nil, models.FailureTargetStateConflict},
		{"wrong role", baseIntent, withRoleTarget(baseTarget, func(v *RolePermissionTargetSnapshot) { v.Role = models.UserRoleUser }), baseHead, nil, models.FailureTargetStateConflict},
		{"wrong target", baseIntent, withRoleTarget(baseTarget, func(v *RolePermissionTargetSnapshot) { v.TargetGUID++ }), baseHead, nil, models.FailureTargetStateConflict},
		{"stale auth", baseIntent, withRoleTarget(baseTarget, func(v *RolePermissionTargetSnapshot) { v.AuthVersion++ }), baseHead, nil, models.FailureTargetVersionConflict},
		{"auth overflow", withPermissionsIntent(baseIntent, func(v *actionsecurity.PermissionsWriteIntent) { v.ExpectedAuthVersion = math.MaxInt32 }), withRoleTarget(baseTarget, func(v *RolePermissionTargetSnapshot) { v.AuthVersion = math.MaxInt32 }), baseHead, nil, models.FailureConsumerValidation},
		{"missing head", baseIntent, baseTarget, nil, nil, models.FailurePolicyVersionConflict},
		{"stale policy", baseIntent, baseTarget, &RolePermissionPolicySnapshot{PolicyVersion: 3, CatalogVersion: 1}, nil, models.FailurePolicyVersionConflict},
		{"stale intent catalog", withPermissionsIntent(baseIntent, func(v *actionsecurity.PermissionsWriteIntent) { v.CatalogVersion = 2 }), baseTarget, baseHead, nil, models.FailurePolicyVersionConflict},
		{"stale head catalog", baseIntent, baseTarget, &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 2}, nil, models.FailurePolicyVersionConflict},
		{"policy overflow", withPermissionsIntent(baseIntent, func(v *actionsecurity.PermissionsWriteIntent) { v.ExpectedPermissionsVersion = math.MaxInt64 }), baseTarget, &RolePermissionPolicySnapshot{PolicyVersion: math.MaxInt64, CatalogVersion: 1}, nil, models.FailureConsumerValidation},
		{"active inherit", baseIntent, baseTarget, baseHead, []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 1}}, models.FailureConsumerValidation},
		{"active unknown", baseIntent, baseTarget, baseHead, []actionsecurity.PermissionOverrideIntent{{Capability: "missing", Effect: 3}}, models.FailureConsumerValidation},
		{"active duplicate", baseIntent, baseTarget, baseHead, []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}, {Capability: "users.read", Effect: 3}}, models.FailureConsumerValidation},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			execution, err := NewRolePermissionExecution(rolePermissionTestDescriptor(t, actionsecurity.ActionUsersPermissionsWrite), tc.intent, func() int64 { return 1 }, &createAccountTestClock{now: 1}, createAccountTestCrypto(t))
			if err != nil {
				// Invalid catalog is rejected before planning, which is the same fail-closed boundary.
				if tc.name == "stale intent catalog" {
					return
				}
				t.Fatal(err)
			}
			plan, failure := execution.PlanTransition(tc.target, tc.head, tc.active)
			if plan != nil || failure == nil || *failure != tc.want {
				t.Fatalf("plan = %#v failure = %#v, want %v", plan, failure, tc.want)
			}
		})
	}
}

func TestRolePermissionPlanNoOpAndOutputAreMutationFree(t *testing.T) {
	for _, tc := range []struct {
		action actionsecurity.Action
		intent any
		role   models.UserRole
	}{
		{actionsecurity.ActionUsersPromote, actionsecurity.PromoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "reason"}, models.UserRoleAdmin},
		{actionsecurity.ActionUsersDemote, actionsecurity.DemoteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Reason: "reason"}, models.UserRoleUser},
	} {
		execution := rolePermissionTestExecution(t, tc.action, tc.intent)
		plan, failure := execution.PlanTransition(RolePermissionTargetSnapshot{ActorGUID: 50, TargetGUID: 91, Role: tc.role, Status: models.UserStatusActive, AuthVersion: 7}, &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 1}, nil)
		if plan != nil || failure == nil || *failure != models.FailureTargetStateConflict || execution.Started() {
			t.Fatalf("same-role action %d = %#v %#v started=%t", tc.action, plan, failure, execution.Started())
		}
	}

	intent := actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}, Reason: "reason"}
	execution := rolePermissionTestExecution(t, actionsecurity.ActionUsersPermissionsWrite, intent)
	active := []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}
	plan, failure := execution.PlanTransition(RolePermissionTargetSnapshot{ActorGUID: 50, TargetGUID: 91, Role: models.UserRoleAdmin, Status: models.UserStatusActive, AuthVersion: 7}, &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 1}, active)
	if plan != nil || failure == nil || *failure != models.FailureTargetStateConflict {
		t.Fatalf("same policy = %#v %#v", plan, failure)
	}
	if execution.Started() || !reflect.DeepEqual(active, []actionsecurity.PermissionOverrideIntent{{Capability: "users.read", Effect: 3}}) {
		t.Fatal("no-op planning mutated execution or input")
	}

	replacement := rolePermissionTestExecution(t, actionsecurity.ActionUsersPermissionsWrite, actionsecurity.PermissionsWriteIntent{TargetGUID: 91, ExpectedAuthVersion: 7, ExpectedPermissionsVersion: 2, CatalogVersion: 1, Overrides: []actionsecurity.PermissionOverrideIntent{{Capability: "users.sessions.read", Effect: 2}}, Reason: "reason"})
	plan, failure = replacement.PlanTransition(RolePermissionTargetSnapshot{ActorGUID: 50, TargetGUID: 91, Role: models.UserRoleAdmin, Status: models.UserStatusActive, AuthVersion: 7}, &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 1}, active)
	if failure != nil || plan == nil {
		t.Fatalf("replacement = %#v %#v", plan, failure)
	}
	plan.DesiredRules[0].Capability = "mutated"
	second, failure := replacement.PlanTransition(RolePermissionTargetSnapshot{ActorGUID: 50, TargetGUID: 91, Role: models.UserRoleAdmin, Status: models.UserStatusActive, AuthVersion: 7}, &RolePermissionPolicySnapshot{PolicyVersion: 2, CatalogVersion: 1}, active)
	if failure != nil || second.DesiredRules[0].Capability != "users.sessions.read" {
		t.Fatal("caller mutated execution-owned desired rules")
	}
}

func rolePermissionTestDescriptor(t *testing.T, action actionsecurity.Action) actionsecurity.Descriptor {
	t.Helper()
	for _, descriptor := range actionsecurity.InactiveActionDescriptors() {
		if descriptor.Action == action {
			return descriptor
		}
	}
	t.Fatalf("missing descriptor %d", action)
	return actionsecurity.Descriptor{}
}

func rolePermissionTestExecution(t *testing.T, action actionsecurity.Action, intent any) *RolePermissionExecution {
	t.Helper()
	execution, err := NewRolePermissionExecution(rolePermissionTestDescriptor(t, action), intent, func() int64 { return 7001 }, &createAccountTestClock{now: 8001}, createAccountTestCrypto(t))
	if err != nil {
		t.Fatal(err)
	}
	return execution
}

func withRoleTarget(value RolePermissionTargetSnapshot, mutate func(*RolePermissionTargetSnapshot)) RolePermissionTargetSnapshot {
	mutate(&value)
	return value
}

func withPermissionsIntent(value actionsecurity.PermissionsWriteIntent, mutate func(*actionsecurity.PermissionsWriteIntent)) actionsecurity.PermissionsWriteIntent {
	mutate(&value)
	return value
}
