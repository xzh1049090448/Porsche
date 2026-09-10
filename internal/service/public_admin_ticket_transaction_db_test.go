package service

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"github.com/porsche/ai-gateway-go/internal/security"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type publicTicketDBFixture struct {
	db           *gorm.DB
	actor        models.User
	actorAPI     ActionActor
	password     string
	verification *ActionVerificationService
}

func openPublicTicketDBFixture(t *testing.T) *publicTicketDBFixture {
	t.Helper()
	if strings.TrimSpace(os.Getenv("TEST_DATABASE_URL")) == "" || strings.TrimSpace(os.Getenv("TEST_REDIS_URL")) == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit disposable TEST_DATABASE_URL and TEST_REDIS_URL; .env is never read")
	}
	// Each test owns a migrated child schema derived from the explicitly named
	// disposable parent, so broad fixture cleanup cannot affect another suite.
	base := openTask8MonitorDBFixture(t)
	t.Cleanup(func() { cleanPublicContentDBFixture(t, base.db) })
	redisOptions, err := redis.ParseURL(strings.TrimSpace(os.Getenv("TEST_REDIS_URL")))
	if err != nil {
		t.Fatalf("parse TEST_REDIS_URL: %v", err)
	}
	client := redis.NewClient(redisOptions)
	t.Cleanup(func() { _ = client.Close() })
	if err = client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("connect TEST_REDIS_URL: %v", err)
	}
	password := "Public-Ticket-Test-Password!"
	hash, err := security.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	if err = base.db.Model(&base.actor).Update("password_hash", hash).Error; err != nil {
		t.Fatal(err)
	}
	base.actor.PasswordHash = &hash
	sid, err := security.NewSessionSID()
	if err != nil {
		t.Fatal(err)
	}
	now := persistence.NowMillis()
	session := models.Session{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, UpdatedAt: now}, SID: sid, UserID: base.actor.ID, LoginMethod: models.LoginMethodPassword, SessionVersion: 1, RefreshHMAC: strings.Repeat("b", 64), LastActiveAt: now, ExpiresAt: now + 3_600_000}
	if err = base.db.Create(&session).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = base.db.Where("actor_user_id=?", base.actor.ID).Delete(&models.AdminActionVerification{}).Error
		_ = base.db.Where("id=?", session.ID).Delete(&models.Session{}).Error
	})
	root := make([]byte, 32)
	if _, err = cryptorand.Read(root); err != nil {
		t.Fatal(err)
	}
	crypto, err := actionsecurity.NewCrypto(root)
	clear(root)
	if err != nil {
		t.Fatal(err)
	}
	authRedis, err := NewAuthRedis(client, "public-ticket-test-auth-hmac-material")
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := NewActionSecurityRedis(client, crypto)
	if err != nil {
		t.Fatal(err)
	}
	verification, err := NewActionVerificationService(base.db, limiter, authRedis, crypto)
	if err != nil {
		t.Fatal(err)
	}
	return &publicTicketDBFixture{db: base.db, actor: base.actor, password: password, verification: verification, actorAPI: ActionActor{UserID: base.actor.ID, UserGUID: base.actor.Guid, AuthVersion: base.actor.AuthVersion, SessionSID: sid, SessionVersion: session.SessionVersion}}
}

func (f *publicTicketDBFixture) ticket(t *testing.T, action actionsecurity.Action, target *int64, intent any) (string, PublicAdminTransactionOption) {
	t.Helper()
	issued, err := f.verification.Issue(context.Background(), VerificationIssue{Action: action, Actor: f.actorAPI, TargetGUID: target, Intent: intent, CurrentPassword: []byte(f.password), TrustedIP: fmt.Sprintf("198.18.%d.%d", f.actor.ID%200+1, persistence.NextGUID()%200+1)})
	if err != nil {
		t.Fatalf("issue %v: %v", action, err)
	}
	consume := VerificationConsume{Action: action, Actor: f.actorAPI, TargetGUID: target, Intent: intent, TicketValues: []string{issued.Ticket}}
	return issued.Ticket, WithActionTicketConsume(func(ctx context.Context, tx *gorm.DB) error {
		return f.verification.ConsumeInTx(ctx, tx, consume)
	})
}

func (f *publicTicketDBFixture) assertTicket(t *testing.T, action actionsecurity.Action, consumed bool) {
	t.Helper()
	var row models.AdminActionVerification
	if err := f.db.Where("actor_user_id=? AND action=?", f.actor.ID, int(action)).Order("id DESC").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if consumed != (row.ConsumedAt != nil && row.IsDeleted == 1) {
		t.Fatalf("ticket action=%v consumed=%v row=%#v", action, consumed, row)
	}
}

func TestPublicPriceTicketAndBusinessCommitRollbackReplayRealDB(t *testing.T) {
	f := openPublicTicketDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	if err := requirePublicPriceSnapshotFixture(f.db, f.actor.ID); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	input := fInputForTicket(t, f)
	model, err := NewPublicModelAdminService(f.db).Create(ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewPublicModelAdminService(f.db).Activate(ctx, f.actor.ID, mustGUID(t, model.GUID), model.Revision); err != nil {
		t.Fatal(err)
	}
	revision := publicPriceDraftRevision(t, f.db)
	request := PublicPriceSnapshotRequest{ActorID: f.actor.ID, ExpectedRevision: revision, IdempotencyKey: "ticket-price-publish"}
	intent := actionsecurity.PublicPricingPublishIntent{ExpectedRevision: revision}
	ticket, option := f.ticket(t, actionsecurity.ActionPublicPricingPublish, nil, intent)
	publishBefore := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID)
	for _, failurePoint := range []string{"replay_lookup", "audit"} {
		broken := NewPublicPriceSnapshotService(f.db)
		broken.fail = func(point string) error {
			if point == failurePoint {
				return errors.New("injected failure")
			}
			return nil
		}
		if _, err := broken.Publish(ctx, request, option); status(err) != 503 {
			t.Fatalf("injected %s publish error=%v", failurePoint, err)
		}
		f.assertTicket(t, actionsecurity.ActionPublicPricingPublish, false)
		if got := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID); got != publishBefore {
			t.Fatalf("injected %s changed business rows before=%v after=%v", failurePoint, publishBefore, got)
		}
	}
	release, err := NewPublicPriceSnapshotService(f.db).Publish(ctx, request, option)
	if err != nil {
		t.Fatalf("retry same ticket: %v", err)
	}
	f.assertTicket(t, actionsecurity.ActionPublicPricingPublish, true)
	publishedCounts := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID)
	if _, err = NewPublicPriceSnapshotService(f.db).Publish(ctx, request, option); !errors.Is(err, ErrActionVerificationForbidden) {
		t.Fatalf("consumed ticket replay=%v ticket=%q", err, ticket)
	}
	_, fresh := f.ticket(t, actionsecurity.ActionPublicPricingPublish, nil, intent)
	replay, err := NewPublicPriceSnapshotService(f.db).Publish(ctx, request, fresh)
	if err != nil || replay.GUID != release.GUID {
		t.Fatalf("fresh ticket replay=%#v err=%v", replay, err)
	}
	if got := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID); got != publishedCounts {
		t.Fatalf("fresh-ticket replay duplicated business rows before=%v after=%v", publishedCounts, got)
	}
	f.assertTicket(t, actionsecurity.ActionPublicPricingPublish, true)

	restoreRevision := publicPriceDraftRevision(t, f.db)
	restoreIntent := actionsecurity.PublicPricingRestoreIntent{ReleaseGUID: mustGUID(t, release.GUID), ExpectedRevision: restoreRevision}
	restoreTicket, restoreOption := f.ticket(t, actionsecurity.ActionPublicPricingRestore, ptrInt64(mustGUID(t, release.GUID)), restoreIntent)
	restoreRequest := PublicPriceSnapshotRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: restoreRevision, SnapshotGUID: release.GUID, IdempotencyKey: "ticket-price-restore"}
	restoreBefore := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID)
	for _, failurePoint := range []string{"replay_lookup", "after_pointer"} {
		brokenRestore := NewPublicPriceSnapshotService(f.db)
		brokenRestore.fail = func(point string) error {
			if point == failurePoint {
				return errors.New("injected restore failure")
			}
			return nil
		}
		if _, err = brokenRestore.Restore(ctx, restoreRequest, restoreOption); status(err) != 503 {
			t.Fatalf("injected %s restore error=%v", failurePoint, err)
		}
		f.assertTicket(t, actionsecurity.ActionPublicPricingRestore, false)
		if got := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID); got != restoreBefore {
			t.Fatalf("failed %s restore changed business rows before=%v after=%v", failurePoint, restoreBefore, got)
		}
	}
	restored, err := NewPublicPriceSnapshotService(f.db).Restore(ctx, restoreRequest, restoreOption)
	if err != nil {
		t.Fatalf("restore retry same ticket: %v", err)
	}
	f.assertTicket(t, actionsecurity.ActionPublicPricingRestore, true)
	restoredCounts := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID)
	restoredDraftRevision := publicPriceDraftRevision(t, f.db)
	if _, err = NewPublicPriceSnapshotService(f.db).Restore(ctx, restoreRequest, restoreOption); !errors.Is(err, ErrActionVerificationForbidden) {
		t.Fatalf("consumed restore ticket replay=%v ticket=%q", err, restoreTicket)
	}
	_, freshRestore := f.ticket(t, actionsecurity.ActionPublicPricingRestore, ptrInt64(mustGUID(t, release.GUID)), restoreIntent)
	restoredReplay, err := NewPublicPriceSnapshotService(f.db).Restore(ctx, restoreRequest, freshRestore)
	if err != nil || restoredReplay.GUID != restored.GUID {
		t.Fatalf("fresh restore ticket replay=%#v err=%v", restoredReplay, err)
	}
	if got := publicPriceSnapshotFixtureCounts(t, f.db, f.actor.ID); got != restoredCounts || publicPriceDraftRevision(t, f.db) != restoredDraftRevision {
		t.Fatalf("restore replay duplicated or mutated state counts=%v want=%v draft=%d want=%d", got, restoredCounts, publicPriceDraftRevision(t, f.db), restoredDraftRevision)
	}
	f.assertTicket(t, actionsecurity.ActionPublicPricingRestore, true)
}

func TestPublicModelDeleteTicketAndBusinessCommitRealDB(t *testing.T) {
	f := openPublicTicketDBFixture(t)
	ctx := context.Background()
	input := fInputForTicket(t, f)
	model, err := NewPublicModelAdminService(f.db).Create(ctx, f.actor.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	guid := mustGUID(t, model.GUID)
	request := DeletePublicModelRequest{ExpectedRevision: model.Revision, Reason: "ticketed retirement"}
	intent := actionsecurity.PublicModelDeleteIntent{ModelGUID: guid, ExpectedRevision: model.Revision, Reason: request.Reason}
	ticket, option := f.ticket(t, actionsecurity.ActionPublicModelDelete, &guid, intent)
	consumeOption := func(in VerificationConsume) PublicAdminTransactionOption {
		return WithActionTicketConsume(func(ctx context.Context, tx *gorm.DB) error { return f.verification.ConsumeInTx(ctx, tx, in) })
	}
	for _, tc := range []struct {
		name    string
		consume VerificationConsume
	}{
		{name: "wrong_actor", consume: VerificationConsume{Action: actionsecurity.ActionPublicModelDelete, Actor: ActionActor{UserID: f.actorAPI.UserID + 1, UserGUID: f.actorAPI.UserGUID, AuthVersion: f.actorAPI.AuthVersion, SessionSID: f.actorAPI.SessionSID, SessionVersion: f.actorAPI.SessionVersion}, TargetGUID: &guid, Intent: intent, TicketValues: []string{ticket}}},
		{name: "wrong_action", consume: VerificationConsume{Action: actionsecurity.ActionPublicPricingPublish, Actor: f.actorAPI, Intent: actionsecurity.PublicPricingPublishIntent{ExpectedRevision: model.Revision}, TicketValues: []string{ticket}}},
		{name: "wrong_resource", consume: VerificationConsume{Action: actionsecurity.ActionPublicModelDelete, Actor: f.actorAPI, TargetGUID: ptrInt64(guid + 1), Intent: actionsecurity.PublicModelDeleteIntent{ModelGUID: guid + 1, ExpectedRevision: model.Revision, Reason: request.Reason}, TicketValues: []string{ticket}}},
		{name: "wrong_intent", consume: VerificationConsume{Action: actionsecurity.ActionPublicModelDelete, Actor: f.actorAPI, TargetGUID: &guid, Intent: actionsecurity.PublicModelDeleteIntent{ModelGUID: guid, ExpectedRevision: model.Revision + 1, Reason: request.Reason}, TicketValues: []string{ticket}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := NewPublicModelAdminService(f.db).Delete(ctx, f.actor.ID, guid, request, consumeOption(tc.consume)); !errors.Is(got, ErrActionVerificationForbidden) {
				t.Fatalf("binding rejection=%v", got)
			}
			f.assertTicket(t, actionsecurity.ActionPublicModelDelete, false)
		})
	}

	fabricated := WithActionTicketConsume(func(ctx context.Context, tx *gorm.DB) error {
		return f.verification.ConsumeInTx(ctx, tx, VerificationConsume{Action: actionsecurity.ActionPublicModelDelete, Actor: f.actorAPI, TargetGUID: &guid, Intent: intent, TicketValues: []string{"av_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}})
	})
	if err = NewPublicModelAdminService(f.db).Delete(ctx, f.actor.ID, guid, request, fabricated); !errors.Is(err, ErrActionVerificationForbidden) {
		t.Fatalf("fabricated ticket delete=%v", err)
	}
	var unchanged models.PublicModelConfig
	if err = f.db.Where("guid=?", guid).First(&unchanged).Error; err != nil || unchanged.IsDeleted != 0 || unchanged.Revision != model.Revision {
		t.Fatalf("fabricated ticket changed model=%#v err=%v", unchanged, err)
	}
	var draftBefore models.PublicPriceDraftState
	if err = f.db.Where("state_key=?", "pricing").First(&draftBefore).Error; err != nil {
		t.Fatal(err)
	}
	var auditsBefore int64
	if err = f.db.Model(&models.AuditLog{}).Where("user_id=? AND action=?", f.actor.ID, "public_models.delete").Count(&auditsBefore).Error; err != nil {
		t.Fatal(err)
	}
	brokenDelete := NewPublicModelAdminService(f.db)
	brokenDelete.fail = func(point string) error {
		if point == "after_ticket" {
			return errors.New("injected model failure")
		}
		return nil
	}
	if err = brokenDelete.Delete(ctx, f.actor.ID, guid, request, option); status(err) != 503 {
		t.Fatalf("injected model delete=%v", err)
	}
	f.assertTicket(t, actionsecurity.ActionPublicModelDelete, false)
	var rollbackModel models.PublicModelConfig
	var rollbackDraft models.PublicPriceDraftState
	var rollbackAudits int64
	if err = f.db.Where("guid=?", guid).First(&rollbackModel).Error; err != nil {
		t.Fatal(err)
	}
	if err = f.db.Where("state_key=?", "pricing").First(&rollbackDraft).Error; err != nil {
		t.Fatal(err)
	}
	if err = f.db.Model(&models.AuditLog{}).Where("user_id=? AND action=?", f.actor.ID, "public_models.delete").Count(&rollbackAudits).Error; err != nil {
		t.Fatal(err)
	}
	if rollbackModel.IsDeleted != 0 || rollbackModel.Revision != model.Revision || rollbackDraft.Revision != draftBefore.Revision || rollbackAudits != auditsBefore {
		t.Fatalf("failed model delete persisted model=%#v draft=%#v audits=%d", rollbackModel, rollbackDraft, rollbackAudits)
	}
	if err = NewPublicModelAdminService(f.db).Delete(ctx, f.actor.ID, guid, request, option); err != nil {
		t.Fatalf("valid ticket delete=%v", err)
	}
	f.assertTicket(t, actionsecurity.ActionPublicModelDelete, true)
	if err = NewPublicModelAdminService(f.db).Delete(ctx, f.actor.ID, guid, request, option); !errors.Is(err, ErrActionVerificationForbidden) {
		t.Fatalf("consumed ticket delete=%v", err)
	}
	var deleted models.PublicModelConfig
	if err = f.db.Unscoped().Where("guid=?", guid).First(&deleted).Error; err != nil || deleted.IsDeleted != 1 || deleted.Revision != model.Revision+1 {
		t.Fatalf("delete state=%#v err=%v", deleted, err)
	}

	expiringInput := fInputForTicket(t, f)
	expiringModel, err := NewPublicModelAdminService(f.db).Create(ctx, f.actor.ID, expiringInput)
	if err != nil {
		t.Fatal(err)
	}
	expiringGUID := mustGUID(t, expiringModel.GUID)
	expiringRequest := DeletePublicModelRequest{ExpectedRevision: expiringModel.Revision, Reason: "expired ticket"}
	expiringIntent := actionsecurity.PublicModelDeleteIntent{ModelGUID: expiringGUID, ExpectedRevision: expiringModel.Revision, Reason: expiringRequest.Reason}
	_, expiringOption := f.ticket(t, actionsecurity.ActionPublicModelDelete, &expiringGUID, expiringIntent)
	if err = f.db.Model(&models.AdminActionVerification{}).Where("actor_user_id=? AND action=? AND target_guid=? AND consumed_at IS NULL", f.actor.ID, int(actionsecurity.ActionPublicModelDelete), expiringGUID).Update("expires_at", persistence.NowMillis()-1).Error; err != nil {
		t.Fatal(err)
	}
	if err = NewPublicModelAdminService(f.db).Delete(ctx, f.actor.ID, expiringGUID, expiringRequest, expiringOption); !errors.Is(err, ErrActionVerificationForbidden) {
		t.Fatalf("expired ticket delete=%v", err)
	}
	var notDeleted models.PublicModelConfig
	if err = f.db.Where("guid=?", expiringGUID).First(&notDeleted).Error; err != nil || notDeleted.IsDeleted != 0 {
		t.Fatalf("expired ticket changed model=%#v err=%v", notDeleted, err)
	}
}

func fInputForTicket(t *testing.T, f *publicTicketDBFixture) CreatePublicModelRequest {
	t.Helper()
	upstream := "ticket/model-" + fmt.Sprint(persistence.NextGUID())
	actor := f.actor.ID
	now := persistence.NowMillis()
	observation := models.UpstreamModelObservation{AuditFields: models.AuditFields{Guid: persistence.NextGUID(), CreatedAt: now, CreatedBy: &actor, UpdatedAt: now, UpdatedBy: &actor}, UpstreamModelID: upstream, Provider: "provider", CatalogComplete: 1, CatalogFresh: 1, ObservedAt: now, ResponseSummaryHash: strings.Repeat("c", 64)}
	if err := f.db.Create(&observation).Error; err != nil {
		t.Fatal(err)
	}
	inputPrice, outputPrice := "1.00000000", "2.00000000"
	return CreatePublicModelRequest{UpstreamModelID: upstream, ModelKey: "ticket-" + fmt.Sprint(persistence.NextGUID()), DisplayName: "Ticket Model", Provider: "provider", Capabilities: []string{"chat"}, ContextWindow: 8192, InputPriceUSDPerMillionTokens: &inputPrice, OutputPriceUSDPerMillionTokens: &outputPrice}
}

func TestPublicContentTicketAndBusinessCommitRollbackReplayRealDB(t *testing.T) {
	f := openPublicTicketDBFixture(t)
	cleanPublicContentDBFixture(t, f.db)
	seed, err := seedContentPublicationFixture(f.db, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := NewPublicContentService(f.db)
	draft, err := svc.GetDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	home, about, terms, privacy, reviewed := "[model](/pricing/"+seed.modelKey+")", "About", "Terms", "Privacy", true
	draft, err = svc.SaveDraft(ctx, f.actor.ID, PublicContentDraftSaveRequest{ExpectedRevision: draft.Revision, Home: &home, About: &about, Terms: &terms, Privacy: &privacy, LegalReviewed: &reviewed})
	if err != nil {
		t.Fatal(err)
	}
	request := PublicContentPublicationRequest{ActorID: f.actor.ID, ExpectedRevision: draft.Revision, PriceReleaseGUID: seed.priceGUID, IdempotencyKey: "ticket-content-publish"}
	priceGUID := mustGUID(t, seed.priceGUID)
	intent := actionsecurity.PublicContentPublishIntent{PriceReleaseGUID: priceGUID, ExpectedRevision: draft.Revision}
	_, option := f.ticket(t, actionsecurity.ActionPublicContentPublish, &priceGUID, intent)
	publishBefore := contentDBCounts(t, f.db)
	for _, failurePoint := range []string{"replay_lookup", "pointer"} {
		broken := NewPublicContentService(f.db)
		broken.fail = func(point string) error {
			if point == failurePoint {
				return errors.New("injected failure")
			}
			return nil
		}
		if _, err = broken.Publish(ctx, request, option); status(err) != 503 {
			t.Fatalf("injected %s publish error=%v", failurePoint, err)
		}
		f.assertTicket(t, actionsecurity.ActionPublicContentPublish, false)
		if got := contentDBCounts(t, f.db); got != publishBefore {
			t.Fatalf("injected %s changed business rows before=%v after=%v", failurePoint, publishBefore, got)
		}
	}
	release, err := svc.Publish(ctx, request, option)
	if err != nil {
		t.Fatalf("retry same ticket: %v", err)
	}
	f.assertTicket(t, actionsecurity.ActionPublicContentPublish, true)
	publishedCounts := contentDBCounts(t, f.db)
	if _, err = svc.Publish(ctx, request, option); !errors.Is(err, ErrActionVerificationForbidden) {
		t.Fatalf("consumed ticket replay=%v", err)
	}
	_, fresh := f.ticket(t, actionsecurity.ActionPublicContentPublish, &priceGUID, intent)
	replay, err := svc.Publish(ctx, request, fresh)
	if err != nil || replay.GUID != release.GUID {
		t.Fatalf("fresh ticket replay=%#v err=%v", replay, err)
	}
	if got := contentDBCounts(t, f.db); got != publishedCounts {
		t.Fatalf("fresh-ticket replay duplicated business rows before=%v after=%v", publishedCounts, got)
	}
	f.assertTicket(t, actionsecurity.ActionPublicContentPublish, true)

	restoreRevision := draft.Revision
	restoreGUID := mustGUID(t, release.GUID)
	restoreIntent := actionsecurity.PublicContentRestoreIntent{ReleaseGUID: restoreGUID, ExpectedRevision: restoreRevision}
	restoreTicket, restoreOption := f.ticket(t, actionsecurity.ActionPublicContentRestore, &restoreGUID, restoreIntent)
	restoreRequest := PublicContentRestoreRequest{ActorID: f.actor.ID, ExpectedRevision: restoreRevision, ReleaseGUID: release.GUID, IdempotencyKey: "ticket-content-restore"}
	restoreBefore := contentDBCounts(t, f.db)
	draftBeforeRestore, err := svc.GetDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, failurePoint := range []string{"replay_lookup", "after_pointer"} {
		brokenRestore := NewPublicContentService(f.db)
		brokenRestore.fail = func(point string) error {
			if point == failurePoint {
				return errors.New("injected restore failure")
			}
			return nil
		}
		if _, err = brokenRestore.Restore(ctx, restoreRequest, restoreOption); status(err) != 503 {
			t.Fatalf("injected %s restore error=%v", failurePoint, err)
		}
		f.assertTicket(t, actionsecurity.ActionPublicContentRestore, false)
		afterFailure, draftErr := svc.GetDraft(ctx, f.actor.ID)
		if draftErr != nil {
			t.Fatal(draftErr)
		}
		if got := contentDBCounts(t, f.db); got != restoreBefore || afterFailure.Revision != draftBeforeRestore.Revision || afterFailure.Home != draftBeforeRestore.Home {
			t.Fatalf("failed %s restore changed counts=%v want=%v draft=%#v", failurePoint, got, restoreBefore, afterFailure)
		}
	}
	restored, err := svc.Restore(ctx, restoreRequest, restoreOption)
	if err != nil {
		t.Fatalf("restore retry same ticket: %v", err)
	}
	f.assertTicket(t, actionsecurity.ActionPublicContentRestore, true)
	restoredCounts := contentDBCounts(t, f.db)
	restoredDraft, err := svc.GetDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Restore(ctx, restoreRequest, restoreOption); !errors.Is(err, ErrActionVerificationForbidden) {
		t.Fatalf("consumed content restore ticket replay=%v ticket=%q", err, restoreTicket)
	}
	_, freshRestore := f.ticket(t, actionsecurity.ActionPublicContentRestore, &restoreGUID, restoreIntent)
	restoredReplay, err := svc.Restore(ctx, restoreRequest, freshRestore)
	if err != nil || restoredReplay.GUID != restored.GUID {
		t.Fatalf("fresh content restore replay=%#v err=%v", restoredReplay, err)
	}
	afterReplayDraft, err := svc.GetDraft(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := contentDBCounts(t, f.db); got != restoredCounts || afterReplayDraft.Revision != restoredDraft.Revision || afterReplayDraft.Home != restoredDraft.Home {
		t.Fatalf("content restore replay duplicated or rematerialized counts=%v want=%v draft=%#v want=%#v", got, restoredCounts, afterReplayDraft, restoredDraft)
	}
	f.assertTicket(t, actionsecurity.ActionPublicContentRestore, true)
}
