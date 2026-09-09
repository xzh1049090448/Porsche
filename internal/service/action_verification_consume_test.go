package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestPublicVerificationBindingRequiresExactResourceTarget(t *testing.T) {
	model := int64(11)
	release := int64(12)
	tests := []struct {
		action actionsecurity.Action
		intent any
		target *int64
	}{
		{actionsecurity.ActionPublicModelDelete, actionsecurity.PublicModelDeleteIntent{ModelGUID: model, ExpectedRevision: 1, Reason: "retired"}, &model},
		{actionsecurity.ActionPublicPricingPublish, actionsecurity.PublicPricingPublishIntent{ExpectedRevision: 1}, nil},
		{actionsecurity.ActionPublicPricingRestore, actionsecurity.PublicPricingRestoreIntent{ReleaseGUID: release, ExpectedRevision: 1}, &release},
		{actionsecurity.ActionPublicContentPublish, actionsecurity.PublicContentPublishIntent{PriceReleaseGUID: release, ExpectedRevision: 1}, &release},
		{actionsecurity.ActionPublicContentRestore, actionsecurity.PublicContentRestoreIntent{ReleaseGUID: release, ExpectedRevision: 1}, &release},
	}
	for _, tc := range tests {
		if err := validatePublicVerificationBinding(tc.action, tc.intent, tc.target); err != nil {
			t.Fatalf("action %d valid binding: %v", tc.action, err)
		}
		wrong := int64(99)
		if err := validatePublicVerificationBinding(tc.action, tc.intent, &wrong); !errors.Is(err, ErrActionVerificationConflict) {
			t.Fatalf("action %d wrong target = %v", tc.action, err)
		}
	}
}

func TestStoredVerificationConsumeBindingRejectsExpiryReplayAndClaimDrift(t *testing.T) {
	now := int64(1000)
	target := int64(12)
	stored := models.AdminActionVerification{ID: 1, ActorUserID: 2, ActorAuthVersion: 3, SessionID: 4,
		Action: int(actionsecurity.ActionPublicPricingRestore), TargetKind: int(actionsecurity.TargetPublicContent), TargetGUID: &target,
		IntentHMAC: strings.Repeat("a", 64), TicketHMAC: strings.Repeat("b", 64), ExpiresAt: now + 1}
	if !validVerificationConsumeBinding(stored, 2, 3, 4, actionsecurity.Descriptor{Action: actionsecurity.ActionPublicPricingRestore, TargetKind: actionsecurity.TargetPublicContent}, &target, stored.IntentHMAC, stored.TicketHMAC, now) {
		t.Fatal("valid stored binding rejected")
	}
	consumed := now - 1
	cases := []models.AdminActionVerification{stored, stored, stored, stored}
	cases[0].ExpiresAt = now
	cases[1].ConsumedAt = &consumed
	cases[2].IsDeleted = 1
	cases[3].ActorAuthVersion++
	for index, candidate := range cases {
		if validVerificationConsumeBinding(candidate, 2, 3, 4, actionsecurity.Descriptor{Action: actionsecurity.ActionPublicPricingRestore, TargetKind: actionsecurity.TargetPublicContent}, &target, stored.IntentHMAC, stored.TicketHMAC, now) {
			t.Fatalf("invalid stored binding %d accepted", index)
		}
	}
}

func TestVerificationConsumeTicketMustBeOneCanonicalHeaderValue(t *testing.T) {
	valid := "av_" + strings.Repeat("A", 43)
	if _, err := parseVerificationConsumeTicket([]string{valid}); err != nil {
		t.Fatal(err)
	}
	for _, values := range [][]string{nil, {}, {valid, valid}, {""}, {"AV_" + strings.Repeat("A", 43)}} {
		if _, err := parseVerificationConsumeTicket(values); err == nil {
			t.Fatalf("accepted ticket headers %#v", values)
		}
	}
}
