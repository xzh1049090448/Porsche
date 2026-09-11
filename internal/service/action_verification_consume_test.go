package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

func TestPublicAdminMutationsConsumeTicketImmediatelyAfterRootLock(t *testing.T) {
	for _, tc := range []struct {
		file   string
		start  string
		before string
	}{
		{"public_model_admin.go", "func (s *PublicModelAdminService) mutateWithOptions", "lockPublicPriceDraftState(tx)"},
		{"public_price_snapshot.go", "func (s *PublicPriceSnapshotService) transact", "lockPublicPriceDraftState(tx)"},
		{"public_content.go", "func (s *PublicContentService) transact", "publicContentRequestPayload("},
	} {
		raw, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		body = body[strings.Index(body, tc.start):]
		root := strings.Index(body, "lockPublicModelRoot(tx, actorID)")
		consume := strings.Index(body, "options.consumeTicket(ctx, tx)")
		before := strings.Index(body, tc.before)
		if root < 0 || consume < 0 || before < 0 || consume <= root || consume >= before {
			t.Errorf("%s must consume after active Root lock and before %s", tc.file, tc.before)
		}
	}
}

func TestPublicAdminTransactionOptionExposesTransactionAwareTicketConsumer(t *testing.T) {
	called := false
	consume := VerifyAndConsumeInTx(func(ctx context.Context, tx *gorm.DB) error {
		called = true
		if ctx == nil || tx == nil {
			t.Fatal("transaction context was not forwarded")
		}
		return nil
	})
	option, err := resolvePublicAdminTransactionOptions([]PublicAdminTransactionOption{WithActionTicketConsume(consume)})
	if err != nil {
		t.Fatal(err)
	}
	if err := option.consume(context.Background(), &gorm.DB{}); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("ticket consumer was not called")
	}
}

func TestPublicAdminTransactionOptionRejectsDuplicateOrNilTicketConsumer(t *testing.T) {
	consume := VerifyAndConsumeInTx(func(context.Context, *gorm.DB) error { return nil })
	for _, options := range [][]PublicAdminTransactionOption{
		{WithActionTicketConsume(nil)},
		{WithActionTicketConsume(consume), WithActionTicketConsume(consume)},
	} {
		if _, err := resolvePublicAdminTransactionOptions(options); err == nil {
			t.Fatal("accepted ambiguous ticket consumer options")
		}
	}
}

func TestStandaloneVerificationConsumePreservesStableContractErrors(t *testing.T) {
	for _, expected := range []error{ErrActionVerificationInactive, ErrActionVerificationForbidden, ErrActionVerificationHidden, ErrActionVerificationConflict} {
		if got := normalizeVerificationConsumeError(expected); !errors.Is(got, expected) {
			t.Fatalf("got %v want %v", got, expected)
		}
	}
	if got := normalizeVerificationConsumeError(errors.New("database failed")); !errors.Is(got, ErrActionVerificationUnavailable) {
		t.Fatalf("database error mapped to %v", got)
	}
}

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
