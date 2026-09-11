package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

const (
	platformSingleIntegrationPrompt   = "SENSITIVE_BE05_PROMPT_7b1f"
	platformSingleIntegrationReply    = "SENSITIVE_BE05_REPLY_8c2e"
	platformSingleIntegrationLease    = "SENSITIVE_BE05_LEASE_9d3a"
	platformSingleIntegrationUpstream = "SENSITIVE_BE05_UPSTREAM_4e6c"
	platformSingleIntegrationTokens   = int64(7)
)

var platformSingleIntegrationSequence atomic.Uint64

type platformSingleIntegrationFixture struct {
	db          *gorm.DB
	store       *PlatformGenerationStore
	registry    *PlatformGenerationCancellationRegistry
	persistence *PlatformGenerationPersistence
	runner      *PlatformSingleGenerationRunner
	control     *PlatformGenerationControl
	user        models.User
	server      *httptest.Server
}

type platformSingleDurableCounts struct {
	conversations int64
	messages      int64
	usage         int64
	receipts      int64
	results       int64
	dailyCalls    int
	totalTokens   int64
}

func requirePlatformSingleIntegrationFixture(t *testing.T, handler http.HandlerFunc) *platformSingleIntegrationFixture {
	t.Helper()
	requirePlatformGenerationCombinedFixture(t)
	gdb := openPlatformGenerationFinalizationMySQL(t)
	store, client := openTestPlatformGenerationStore(t)
	now := time.Now().UTC().UnixMilli()
	username := fixtureUsername(testSnowflake.Next())
	user := models.User{
		AuditFields:       models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now - 1000, UpdatedAt: now - 1000},
		GroupID:           testDefaultBusinessGroupID(t, gdb),
		Username:          &username,
		Nickname:          &username,
		AllowedModels:     models.JSONSlice{},
		PlanType:          models.PlanFree,
		Status:            models.UserStatusActive,
		Role:              models.UserRoleUser,
		AuthVersion:       1,
		DailyCallLimit:    10,
		DailyCallsUsed:    0,
		TotalTokensUsed:   0,
		DailyCallsResetAt: &now,
	}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create single integration user: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	upstream, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{
		BaseURL: server.URL, APIKey: "fixture-only", AllowedModels: map[string]struct{}{"model-a": {}},
	}, server.Client(), time.Now)
	if err != nil {
		t.Fatalf("construct loopback upstream: %v", err)
	}
	persistenceService, err := NewPlatformGenerationPersistence(store)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewPlatformGenerationCancellationRegistry()
	runner, err := NewPlatformSingleGenerationRunner(gdb, store, persistenceService, registry, upstream, context.Background(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewPlatformGenerationControl(gdb, store, registry)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &platformSingleIntegrationFixture{db: gdb, store: store, registry: registry, persistence: persistenceService, runner: runner, control: control, user: user, server: server}
	t.Cleanup(func() {
		prefix := fmt.Sprintf("%s%d:", platformGenerationPrefix, user.ID)
		keys, scanErr := client.Keys(context.Background(), prefix+"*").Result()
		if scanErr != nil {
			t.Errorf("scan owned generation keys: %v", scanErr)
			return
		}
		if len(keys) > 0 {
			if err := client.Del(context.Background(), keys...).Err(); err != nil {
				t.Errorf("delete owned generation keys: %v", err)
			}
		}
	})
	return fixture
}

func platformSingleIntegrationGenerationID() string {
	value := platformSingleIntegrationSequence.Add(1)
	return fmt.Sprintf("be050000-0000-4000-8000-%012x", value)
}

func platformSingleIntegrationSuccessHandler(release <-chan struct{}, started chan<- struct{}, calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":%q,\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\n", platformSingleIntegrationUpstream, platformSingleIntegrationReply)
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":2,\"model\":%q,\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":%d}}\n\n", platformSingleIntegrationUpstream, platformSingleIntegrationTokens)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}
}

func (f *platformSingleIntegrationFixture) params(conversationGUID *int64) ChatParams {
	params := ChatParams{
		Model:          "model-a",
		Messages:       []map[string]interface{}{{"role": "user", "content": platformSingleIntegrationPrompt}},
		WhiteLabelBody: []byte(`{"model":"model-a","messages":[]}`),
	}
	if conversationGUID != nil {
		value := strconv.FormatInt(*conversationGUID, 10)
		params.ConversationGUID = &value
	}
	return params
}

func (f *platformSingleIntegrationFixture) run(generationID string, params ChatParams, write func([]byte) error) (PlatformSingleGenerationRunResult, error) {
	if write == nil {
		write = func([]byte) error { return nil }
	}
	return f.runner.Run(PlatformSingleGenerationInput{
		Context: context.Background(), User: &f.user, GenerationID: generationID,
		RequestID: "be05-integration-request", Params: params, Write: write,
	})
}

func (f *platformSingleIntegrationFixture) durableCounts(t *testing.T) platformSingleDurableCounts {
	t.Helper()
	counts := platformSingleDurableCounts{}
	for model, destination := range map[interface{}]*int64{
		&models.Conversation{}:                  &counts.conversations,
		&models.Message{}:                       &counts.messages,
		&models.UsageRecord{}:                   &counts.usage,
		&models.PlatformChatGenerationReceipt{}: &counts.receipts,
		&models.PlatformChatGenerationResult{}:  &counts.results,
	} {
		if err := f.db.Model(model).Where("is_deleted = 0").Count(destination).Error; err != nil {
			t.Fatalf("count durable model %T: %v", model, err)
		}
	}
	var user models.User
	if err := f.db.First(&user, f.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	counts.dailyCalls, counts.totalTokens = user.DailyCallsUsed, user.TotalTokensUsed
	return counts
}

func (f *platformSingleIntegrationFixture) requireCompleted(t *testing.T, generationID string, existing bool) PlatformGenerationReceiptSnapshot {
	t.Helper()
	receipt, err := LoadPlatformGenerationReceipt(context.Background(), f.db, f.user.ID, generationID)
	if err != nil {
		t.Fatalf("load durable receipt: %v", err)
	}
	if receipt.RequestedExistingConversation != existing || receipt.UserMessage != platformSingleIntegrationPrompt || receipt.SuccessfulModelCount != 1 || receipt.DailyCallsCharged != 1 || receipt.TotalTokens != platformSingleIntegrationTokens || len(receipt.Results) != 1 {
		t.Fatalf("unexpected receipt summary: %#v", receipt)
	}
	result := receipt.Results[0]
	if result.Model != "model-a" || result.State != PlatformGenerationStateCompleted || result.Content != platformSingleIntegrationReply || result.Tokens != platformSingleIntegrationTokens || result.AssistantMessageGUID == "" {
		t.Fatalf("unexpected durable result: %#v", result)
	}
	var conversation models.Conversation
	if err := f.db.Where("guid = ? AND user_id = ? AND is_deleted = 0", receipt.ConversationGUID, f.user.ID).First(&conversation).Error; err != nil {
		t.Fatal(err)
	}
	var messages []models.Message
	if err := f.db.Where("conversation_id = ? AND is_deleted = 0", conversation.ID).Order("id ASC").Find(&messages).Error; err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != models.MessageRoleUser || messages[0].Content != platformSingleIntegrationPrompt || messages[1].Role != models.MessageRoleAssistant || messages[1].Content != platformSingleIntegrationReply || messages[1].Tokens != int(platformSingleIntegrationTokens) || messages[1].Model == nil || *messages[1].Model != "model-a" || strconv.FormatInt(messages[1].Guid, 10) != result.AssistantMessageGUID {
		t.Fatalf("unexpected exact message graph: %#v", messages)
	}
	var usage []models.UsageRecord
	if err := f.db.Where("user_id = ? AND is_deleted = 0", f.user.ID).Find(&usage).Error; err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].RecordType != models.UsageRecordChat || usage[0].Tokens != int(platformSingleIntegrationTokens) || usage[0].Model == nil || *usage[0].Model != "model-a" {
		t.Fatalf("unexpected usage rows: %#v", usage)
	}
	counts := f.durableCounts(t)
	if counts.conversations != 1 || counts.messages != 2 || counts.usage != 1 || counts.receipts != 1 || counts.results != 1 || counts.dailyCalls != 1 || counts.totalTokens != platformSingleIntegrationTokens {
		t.Fatalf("unexpected durable counts: %#v", counts)
	}
	snapshot, err := f.store.Get(context.Background(), f.user.ID, generationID)
	if err != nil || snapshot.State != PlatformGenerationStateCompleted || snapshot.ModelStates["model-a"].AssistantMessageGUID != result.AssistantMessageGUID {
		t.Fatalf("completed Redis snapshot=%#v error=%v", snapshot, err)
	}
	raw, err := f.store.client.Get(context.Background(), f.store.key(f.user.ID, generationID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{platformSingleIntegrationPrompt, platformSingleIntegrationReply, platformSingleIntegrationLease, platformSingleIntegrationUpstream} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("sensitive integration marker reached Redis")
		}
	}
	view, err := f.control.Get(context.Background(), f.user.ID, generationID)
	if err != nil || view.Status != "completed" || view.ConversationGUID == nil || *view.ConversationGUID != strconv.FormatInt(receipt.ConversationGUID, 10) || view.Result == nil || view.Result.Content != platformSingleIntegrationReply || view.Result.Tokens == nil || *view.Result.Tokens != platformSingleIntegrationTokens || view.TotalTokensUsed == nil || *view.TotalTokensUsed != platformSingleIntegrationTokens {
		t.Fatalf("GET hydrated view=%#v error=%v", view, err)
	}
	return receipt
}

func waitPlatformSingleSnapshot(t *testing.T, f *platformSingleIntegrationFixture, generationID string, predicate func(PlatformGenerationSnapshot) bool) PlatformGenerationSnapshot {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := f.store.Get(context.Background(), f.user.ID, generationID)
		if err == nil && predicate(snapshot) {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for generation %s state", generationID)
	return PlatformGenerationSnapshot{}
}

func requirePlatformSingleIntegrationSuccess(t *testing.T, existing, disconnect bool) {
	t.Helper()
	f := requirePlatformSingleIntegrationFixture(t, platformSingleIntegrationSuccessHandler(nil, nil, nil))
	var conversationGUID *int64
	if existing {
		guid := testSnowflake.Next()
		conversation := models.Conversation{
			AuditFields: models.AuditFields{Guid: guid, CreatedAt: f.user.UpdatedAt, CreatedBy: &f.user.ID, UpdatedAt: f.user.UpdatedAt, UpdatedBy: &f.user.ID},
			UserID:      f.user.ID, Title: "existing integration conversation",
		}
		if err := f.db.Create(&conversation).Error; err != nil {
			t.Fatal(err)
		}
		conversationGUID = &guid
	}
	generationID := platformSingleIntegrationGenerationID()
	var output bytes.Buffer
	writes := 0
	result, err := f.run(generationID, f.params(conversationGUID), func(frame []byte) error {
		writes++
		if disconnect {
			return io.ErrClosedPipe
		}
		_, _ = output.Write(frame)
		return nil
	})
	if err != nil || !result.Started || result.Duplicate != nil {
		t.Fatalf("Run() result=%+v error=%v", result, err)
	}
	if writes == 0 || (!disconnect && !bytes.Contains(output.Bytes(), []byte("event: done"))) {
		t.Fatalf("unexpected SSE writes=%d output=%q", writes, output.Bytes())
	}
	receipt := f.requireCompleted(t, generationID, existing)
	if conversationGUID != nil && receipt.ConversationGUID != *conversationGUID {
		t.Fatalf("existing conversation GUID=%d want=%d", receipt.ConversationGUID, *conversationGUID)
	}
}

func requirePlatformSingleIntegrationCancel(t *testing.T) {
	t.Helper()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	f := requirePlatformSingleIntegrationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	generationID := platformSingleIntegrationGenerationID()
	before := f.durableCounts(t)
	type outcome struct {
		result PlatformSingleGenerationRunResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := f.run(generationID, f.params(nil), func([]byte) error { return nil })
		done <- outcome{result: result, err: err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not start")
	}
	view, pending, err := f.control.Cancel(context.Background(), f.user.ID, generationID)
	if err != nil || pending || view.Status != "cancelled" {
		t.Fatalf("Cancel() view=%#v pending=%v error=%v", view, pending, err)
	}
	select {
	case got := <-done:
		if !got.result.Started || got.err == nil {
			t.Fatalf("cancelled Run() result=%+v error=%v", got.result, got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled runner did not stop")
	}
	close(release)
	after := f.durableCounts(t)
	if before != after {
		t.Fatalf("cancel durable delta before=%#v after=%#v", before, after)
	}
}

func requirePlatformSingleIntegrationRenewal(t *testing.T) {
	t.Helper()
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseUpstream := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseUpstream)
	started := make(chan struct{}, 1)
	f := requirePlatformSingleIntegrationFixture(t, platformSingleIntegrationSuccessHandler(release, started, nil))
	clock := atomic.Int64{}
	clock.Store(time.Now().UTC().UnixMilli())
	f.runner.deps.now = func() time.Time { return time.UnixMilli(clock.Load()).UTC() }
	f.control.now = func() time.Time { return time.UnixMilli(clock.Load()).UTC() }
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 4)}
	f.runner.deps.newTimer = timers.New
	generationID := platformSingleIntegrationGenerationID()
	done := make(chan error, 1)
	go func() {
		_, err := f.run(generationID, f.params(nil), func([]byte) error { return nil })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not start")
	}
	running := waitPlatformSingleSnapshot(t, f, generationID, func(snapshot PlatformGenerationSnapshot) bool {
		return snapshot.State == PlatformGenerationStateRunning
	})
	var timer *platformSingleTestTimer
	select {
	case timer = <-timers.created:
	case <-time.After(2 * time.Second):
		t.Fatal("renewal timer was not created")
	}
	clock.Store(running.CreatedAtMillis + 20_000)
	timer.ch <- time.UnixMilli(clock.Load())
	renewed := waitPlatformSingleSnapshot(t, f, generationID, func(snapshot PlatformGenerationSnapshot) bool {
		return snapshot.LeaseUntilMillis > running.LeaseUntilMillis
	})
	converger, err := NewPlatformGenerationConverger(f.control)
	if err != nil {
		t.Fatal(err)
	}
	converger.nowMillis = func() int64 { return running.LeaseUntilMillis + 1 }
	if err := converger.RunPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	afterPass, err := f.store.Get(context.Background(), f.user.ID, generationID)
	if err != nil || afterPass.State != PlatformGenerationStateRunning || afterPass.LeaseUntilMillis != renewed.LeaseUntilMillis {
		t.Fatalf("converger overrode renewed authority: %#v error=%v", afterPass, err)
	}
	releaseUpstream()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("renewed runner did not finish")
	}
	f.requireCompleted(t, generationID, false)
}

func requirePlatformSingleIntegrationCommitUnknown(t *testing.T) {
	t.Helper()
	f := requirePlatformSingleIntegrationFixture(t, platformSingleIntegrationSuccessHandler(nil, nil, nil))
	originalRunLocked := f.persistence.runLocked
	var attempts atomic.Int32
	f.persistence.runLocked = func(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
		attempts.Add(1)
		if err := originalRunLocked(ctx, db, lockName, fn); err != nil {
			return err
		}
		return errors.New("commit acknowledgement unavailable")
	}
	generationID := platformSingleIntegrationGenerationID()
	result, err := f.run(generationID, f.params(nil), func([]byte) error { return nil })
	if err != nil || !result.Started || attempts.Load() != 1 {
		t.Fatalf("commit-unknown Run() result=%+v attempts=%d error=%v", result, attempts.Load(), err)
	}
	f.requireCompleted(t, generationID, false)
}

func requirePlatformSingleIntegrationRaces(t *testing.T) {
	t.Helper()
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var upstreamCalls atomic.Int32
	f := requirePlatformSingleIntegrationFixture(t, platformSingleIntegrationSuccessHandler(release, started, &upstreamCalls))
	f.user.DailyCallLimit = 2
	if err := f.db.Model(&models.User{}).Where("id = ?", f.user.ID).Update("daily_call_limit", 2).Error; err != nil {
		t.Fatal(err)
	}
	duplicateID := platformSingleIntegrationGenerationID()
	firstDone := make(chan error, 1)
	go func() {
		_, err := f.run(duplicateID, f.params(nil), func([]byte) error { return nil })
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first duplicate-race upstream did not start")
	}
	duplicate, err := f.run(duplicateID, f.params(nil), func([]byte) error { return nil })
	if err != nil || duplicate.Started || duplicate.Duplicate == nil || duplicate.Duplicate.State != PlatformGenerationStateRunning {
		t.Fatalf("duplicate race result=%+v error=%v", duplicate, err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("duplicate race upstream calls=%d, want 1", upstreamCalls.Load())
	}
	f.requireCompleted(t, duplicateID, false)

	before := f.durableCounts(t)
	ids := []string{platformSingleIntegrationGenerationID(), platformSingleIntegrationGenerationID()}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for index := range ids {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, errs[index] = f.run(ids[index], f.params(nil), func([]byte) error { return nil })
		}(index)
	}
	wg.Wait()
	successes, failures := 0, 0
	for _, runErr := range errs {
		if runErr == nil {
			successes++
		} else {
			failures++
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("quota race successes/failures=%d/%d errors=%v", successes, failures, errs)
	}
	after := f.durableCounts(t)
	if after.conversations-before.conversations != 1 || after.messages-before.messages != 2 || after.usage-before.usage != 1 || after.receipts-before.receipts != 1 || after.results-before.results != 1 || after.dailyCalls-before.dailyCalls != 1 || after.totalTokens-before.totalTokens != platformSingleIntegrationTokens {
		t.Fatalf("quota race durable delta before=%#v after=%#v", before, after)
	}
}

func requirePlatformSingleIntegrationFailureMatrix(t *testing.T) {
	t.Helper()
	tests := []struct {
		name    string
		timeout time.Duration
		handler http.HandlerFunc
	}{
		{name: "non-2xx", handler: func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unavailable", http.StatusBadGateway) }},
		{name: "malformed chunk", handler: func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "data: {\"id\":\n\n") }},
		{name: "premature eof", handler: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"model-a\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"},\"finish_reason\":null}]}\n\n")
		}},
		{name: "timeout", timeout: 40 * time.Millisecond, handler: func(_ http.ResponseWriter, r *http.Request) {
			timer := time.NewTimer(200 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
			case <-timer.C:
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := requirePlatformSingleIntegrationFixture(t, test.handler)
			if test.timeout > 0 {
				f.runner.deps.upstreamTimeout = test.timeout
			}
			before := f.durableCounts(t)
			generationID := platformSingleIntegrationGenerationID()
			result, err := f.run(generationID, f.params(nil), func([]byte) error { return nil })
			if !result.Started || err == nil {
				t.Fatalf("failure Run() result=%+v error=%v", result, err)
			}
			after := f.durableCounts(t)
			if before != after {
				t.Fatalf("failure durable delta before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestPlatformSingleGenerationIntegrationNewConversationSuccess(t *testing.T) {
	requirePlatformSingleIntegrationSuccess(t, false, false)
}

func TestPlatformSingleGenerationIntegrationExistingConversationSuccess(t *testing.T) {
	requirePlatformSingleIntegrationSuccess(t, true, false)
}

func TestPlatformSingleGenerationIntegrationDisconnectThenGET(t *testing.T) {
	requirePlatformSingleIntegrationSuccess(t, false, true)
}

func TestPlatformSingleGenerationIntegrationCancelNoPersistence(t *testing.T) {
	requirePlatformSingleIntegrationCancel(t)
}

func TestPlatformSingleGenerationIntegrationRenewalBeatsConverger(t *testing.T) {
	requirePlatformSingleIntegrationRenewal(t)
}

func TestPlatformSingleGenerationIntegrationCommitUnknownReconciles(t *testing.T) {
	requirePlatformSingleIntegrationCommitUnknown(t)
}

func TestPlatformSingleGenerationIntegrationConcurrentQuotaAndDuplicateRaces(t *testing.T) {
	requirePlatformSingleIntegrationRaces(t)
}

func TestPlatformSingleGenerationIntegrationFailureMatrixHasNoDurableContent(t *testing.T) {
	requirePlatformSingleIntegrationFailureMatrix(t)
}
