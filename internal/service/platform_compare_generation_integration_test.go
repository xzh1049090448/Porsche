package service

import (
	"bytes"
	"context"
	"encoding/json"
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

const platformCompareIntegrationPrompt = "compare integration prompt"

const platformCompareIntegrationSyncTimeout = 5 * time.Second

var platformCompareIntegrationSequence atomic.Uint64

func receivePlatformCompareIntegration[T any](t *testing.T, channel <-chan T, operation string) T {
	t.Helper()
	timer := time.NewTimer(platformCompareIntegrationSyncTimeout)
	defer timer.Stop()
	select {
	case value, ok := <-channel:
		if !ok {
			t.Fatalf("compare integration %s channel closed unexpectedly", operation)
		}
		return value
	case <-timer.C:
		t.Fatalf("timed out waiting for compare integration %s", operation)
		var zero T
		return zero
	}
}

func sendPlatformCompareIntegration[T any](t *testing.T, channel chan<- T, value T, operation string) {
	t.Helper()
	timer := time.NewTimer(platformCompareIntegrationSyncTimeout)
	defer timer.Stop()
	select {
	case channel <- value:
	case <-timer.C:
		t.Fatalf("timed out sending compare integration %s", operation)
	}
}

func releasePlatformCompareIntegration(t *testing.T, channel chan struct{}) func() {
	t.Helper()
	var once sync.Once
	release := func() { once.Do(func() { close(channel) }) }
	t.Cleanup(release)
	return release
}

type platformCompareIntegrationFixture struct {
	db          *gorm.DB
	store       *PlatformGenerationStore
	registry    *PlatformGenerationCancellationRegistry
	persistence *PlatformGenerationPersistence
	runner      *PlatformCompareGenerationRunner
	control     *PlatformGenerationControl
	user        models.User
}

type platformCompareIntegrationCounts struct {
	conversations int64
	messages      int64
	usage         int64
	receipts      int64
	results       int64
	dailyCalls    int
	totalTokens   int64
}

type platformCompareIntegrationExpectation struct {
	model   string
	content string
	tokens  int64
	code    string
}

func platformCompareIntegrationGenerationID() string {
	return fmt.Sprintf("be060000-0000-4000-8000-%012x", platformCompareIntegrationSequence.Add(1))
}

func platformCompareIntegrationContent(model string) string {
	return "answer-" + model
}

func platformCompareIntegrationTokens(model string) int64 {
	switch model {
	case "model-a":
		return 3
	case "model-b":
		return 5
	default:
		return 7
	}
}

func platformCompareIntegrationHandler(failed map[string]bool, release <-chan struct{}, started chan<- string, calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		var request struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model == "" {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		if started != nil {
			select {
			case started <- request.Model:
			case <-r.Context().Done():
				return
			}
		}
		if release != nil {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		if failed[request.Model] {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		content, tokens := platformCompareIntegrationContent(request.Model), platformCompareIntegrationTokens(request.Model)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture-provider\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q},\"finish_reason\":\"stop\"}]}\n\n", content)
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":2,\"model\":\"fixture-provider\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":%d}}\n\n", tokens)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}
}

func requirePlatformCompareIntegrationFixture(t *testing.T, handler http.HandlerFunc) *platformCompareIntegrationFixture {
	t.Helper()
	requirePlatformGenerationCombinedFixture(t)
	gdb := openPlatformGenerationFinalizationMySQL(t)
	store, client := openTestPlatformGenerationStore(t)
	now := time.Now().UTC().UnixMilli()
	username := fixtureUsername(testSnowflake.Next())
	user := models.User{
		AuditFields: models.AuditFields{Guid: testSnowflake.Next(), CreatedAt: now - 1000, UpdatedAt: now - 1000},
		GroupID:     testDefaultBusinessGroupID(t, gdb), Username: &username, Nickname: &username,
		AllowedModels: models.JSONSlice{}, PlanType: models.PlanFree, Status: models.UserStatusActive,
		Role: models.UserRoleUser, AuthVersion: 1, DailyCallLimit: 20, DailyCallsUsed: 0,
		TotalTokensUsed: 0, DailyCallsResetAt: &now,
	}
	if err := gdb.Create(&user).Error; err != nil {
		t.Fatalf("create compare integration user: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	upstream, err := whitelabel.NewWhiteLabelService(config.WhiteLabelSettings{
		BaseURL: server.URL, APIKey: "fixture-only", AllowedModels: map[string]struct{}{"model-a": {}, "model-b": {}, "model-c": {}},
	}, server.Client(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	persistenceService, err := NewPlatformGenerationPersistence(store)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewPlatformGenerationCancellationRegistry()
	runner, err := NewPlatformCompareGenerationRunner(gdb, store, persistenceService, registry, upstream, context.Background(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	control, err := NewPlatformGenerationControl(gdb, store, registry)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &platformCompareIntegrationFixture{db: gdb, store: store, registry: registry, persistence: persistenceService, runner: runner, control: control, user: user}
	t.Cleanup(func() {
		prefix := fmt.Sprintf("%s%d:", platformGenerationPrefix, user.ID)
		keys, scanErr := client.Keys(context.Background(), prefix+"*").Result()
		if scanErr != nil {
			t.Errorf("scan owned compare generation keys: %v", scanErr)
			return
		}
		if len(keys) > 0 {
			if err := client.Del(context.Background(), keys...).Err(); err != nil {
				t.Errorf("delete owned compare generation keys: %v", err)
			}
		}
	})
	return fixture
}

func (f *platformCompareIntegrationFixture) params() ChatParams {
	maxTokens := 64
	return ChatParams{
		Messages:  []map[string]interface{}{{"role": "user", "content": platformCompareIntegrationPrompt}},
		MaxTokens: &maxTokens, WhiteLabelBody: []byte(`{"model":"model-a","messages":[{"role":"user","content":"placeholder"}],"max_tokens":64}`),
	}
}

func (f *platformCompareIntegrationFixture) run(ctx context.Context, generationID string, modelIDs []string, write func([]byte) error) (PlatformCompareGenerationRunResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if write == nil {
		write = func([]byte) error { return nil }
	}
	return f.runner.Run(PlatformCompareGenerationInput{
		Context: ctx, User: &f.user, GenerationID: generationID, RequestID: "be06-integration-request",
		Models: append([]string(nil), modelIDs...), Params: f.params(), Write: write,
	})
}

func (f *platformCompareIntegrationFixture) counts(t *testing.T) platformCompareIntegrationCounts {
	t.Helper()
	counts := platformCompareIntegrationCounts{}
	conversationIDs := f.db.Model(&models.Conversation{}).Select("id").Where("user_id = ? AND is_deleted = 0", f.user.ID)
	receiptIDs := f.db.Model(&models.PlatformChatGenerationReceipt{}).Select("id").Where("user_id = ? AND is_deleted = 0", f.user.ID)
	queries := []struct {
		model interface{}
		where string
		args  []interface{}
		dest  *int64
	}{
		{model: &models.Conversation{}, where: "user_id = ? AND is_deleted = 0", args: []interface{}{f.user.ID}, dest: &counts.conversations},
		{model: &models.Message{}, where: "conversation_id IN (?) AND is_deleted = 0", args: []interface{}{conversationIDs}, dest: &counts.messages},
		{model: &models.UsageRecord{}, where: "user_id = ? AND is_deleted = 0", args: []interface{}{f.user.ID}, dest: &counts.usage},
		{model: &models.PlatformChatGenerationReceipt{}, where: "user_id = ? AND is_deleted = 0", args: []interface{}{f.user.ID}, dest: &counts.receipts},
		{model: &models.PlatformChatGenerationResult{}, where: "receipt_id IN (?) AND is_deleted = 0", args: []interface{}{receiptIDs}, dest: &counts.results},
	}
	for _, query := range queries {
		if err := f.db.Model(query.model).Where(query.where, query.args...).Count(query.dest).Error; err != nil {
			t.Fatalf("count compare durable rows: %v", err)
		}
	}
	var user models.User
	if err := f.db.First(&user, f.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	counts.dailyCalls, counts.totalTokens = user.DailyCallsUsed, user.TotalTokensUsed
	return counts
}

func (f *platformCompareIntegrationFixture) configureUser(t *testing.T, plan models.PlanType, limit, used int, resetAt int64) {
	t.Helper()
	if err := f.db.Model(&models.User{}).Where("id = ?", f.user.ID).Updates(map[string]interface{}{
		"plan_type": plan, "daily_call_limit": limit, "daily_calls_used": used, "daily_calls_reset_at": resetAt,
	}).Error; err != nil {
		t.Fatalf("configure compare integration user: %v", err)
	}
	f.user.PlanType, f.user.DailyCallLimit, f.user.DailyCallsUsed, f.user.DailyCallsResetAt = plan, limit, used, &resetAt
}

func platformCompareIntegrationExpected(modelIDs []string, failed map[string]bool) []platformCompareIntegrationExpectation {
	expected := make([]platformCompareIntegrationExpectation, len(modelIDs))
	for index, model := range modelIDs {
		expected[index] = platformCompareIntegrationExpectation{model: model}
		if failed[model] {
			expected[index].code = "gateway_upstream_error"
		} else {
			expected[index].content = platformCompareIntegrationContent(model)
			expected[index].tokens = platformCompareIntegrationTokens(model)
		}
	}
	return expected
}

func requirePlatformCompareIntegrationReceipt(t *testing.T, f *platformCompareIntegrationFixture, generationID string, expected []platformCompareIntegrationExpectation, before platformCompareIntegrationCounts) PlatformGenerationReceiptSnapshot {
	t.Helper()
	receipt, err := LoadPlatformGenerationReceipt(context.Background(), f.db, f.user.ID, generationID)
	if err != nil {
		t.Fatalf("load compare receipt: %v", err)
	}
	if receipt.UserID != f.user.ID || receipt.GenerationID != generationID || receipt.Mode != PlatformGenerationModeCompare || receipt.ConversationGUID <= 0 || receipt.RequestedExistingConversation || receipt.UserMessage != platformCompareIntegrationPrompt || receipt.CommittedAtMillis <= 0 || len(receipt.Results) != len(expected) {
		t.Fatalf("invalid compare receipt identity/graph")
	}
	successes, totalTokens := 0, int64(0)
	assistantGUIDs := map[string]string{}
	for index, want := range expected {
		got := receipt.Results[index]
		if got.Model != want.model {
			t.Fatalf("receipt order[%d]=%q want %q", index, got.Model, want.model)
		}
		if want.code == "" {
			successes++
			totalTokens += want.tokens
			if got.State != PlatformGenerationStateCompleted || got.Content != want.content || got.Tokens != want.tokens || got.ErrorCode != "" || got.AssistantMessageGUID == "" {
				t.Fatalf("invalid completed receipt result for %q", want.model)
			}
			assistantGUIDs[want.model] = got.AssistantMessageGUID
		} else if got.State != PlatformGenerationStateFailed || got.Content != "" || got.Tokens != 0 || got.ErrorCode != want.code || got.AssistantMessageGUID != "" {
			t.Fatalf("invalid failed receipt result for %q", want.model)
		}
	}
	if receipt.SuccessfulModelCount != successes || receipt.DailyCallsCharged != successes || receipt.TotalTokens != totalTokens {
		t.Fatalf("receipt accounting = successes %d charge %d tokens %d, want %d/%d/%d", receipt.SuccessfulModelCount, receipt.DailyCallsCharged, receipt.TotalTokens, successes, successes, totalTokens)
	}

	var conversation models.Conversation
	if err := f.db.Where("guid = ? AND user_id = ? AND is_deleted = 0", receipt.ConversationGUID, f.user.ID).First(&conversation).Error; err != nil {
		t.Fatalf("load durable conversation: %v", err)
	}
	var messages []models.Message
	if err := f.db.Where("conversation_id = ? AND is_deleted = 0", conversation.ID).Order("id ASC").Find(&messages).Error; err != nil {
		t.Fatalf("load durable messages: %v", err)
	}
	if len(messages) != 1+successes || messages[0].Content != platformCompareIntegrationPrompt || messages[0].Role != models.MessageRoleUser {
		t.Fatalf("invalid durable message graph")
	}
	messageIndex := 1
	for _, want := range expected {
		if want.code != "" {
			continue
		}
		message := messages[messageIndex]
		if message.Model == nil || *message.Model != want.model || message.Content != want.content || int64(message.Tokens) != want.tokens || strconv.FormatInt(message.Guid, 10) != assistantGUIDs[want.model] {
			t.Fatalf("invalid assistant message order/content for %q", want.model)
		}
		messageIndex++
	}
	var usage []models.UsageRecord
	if err := f.db.Where("user_id = ? AND is_deleted = 0", f.user.ID).Order("id ASC").Find(&usage).Error; err != nil {
		t.Fatalf("load durable usage: %v", err)
	}
	baseUsage := before.usage
	if len(usage) != int(baseUsage)+successes {
		t.Fatalf("usage rows=%d want %d", len(usage), int(before.usage)+successes)
	}
	for index, want := range expected {
		if want.code != "" {
			continue
		}
		row := usage[int(baseUsage)]
		if row.Model == nil || *row.Model != want.model || int64(row.Tokens) != want.tokens {
			t.Fatalf("invalid usage row for result %d", index)
		}
		baseUsage++
	}
	var receiptRow models.PlatformChatGenerationReceipt
	if err := f.db.Where("user_id = ? AND generation_id = ? AND is_deleted = 0", f.user.ID, generationID).First(&receiptRow).Error; err != nil {
		t.Fatalf("load durable receipt row: %v", err)
	}
	var resultRows []models.PlatformChatGenerationResult
	if err := f.db.Where("receipt_id = ? AND is_deleted = 0", receiptRow.ID).Order("model_index ASC").Find(&resultRows).Error; err != nil {
		t.Fatalf("load durable result rows: %v", err)
	}
	if len(resultRows) != len(expected) {
		t.Fatalf("durable result count=%d want %d", len(resultRows), len(expected))
	}
	for index, want := range expected {
		row := resultRows[index]
		if row.ModelIndex != index || row.Model != want.model {
			t.Fatalf("durable result order mismatch at %d", index)
		}
		if want.code == "" {
			if row.Status != models.PlatformGenerationResultCompleted || row.AssistantMessageID == nil || int64(row.Tokens) != want.tokens || row.ErrorCode != nil {
				t.Fatalf("invalid durable completed result for %q", want.model)
			}
		} else if row.Status != models.PlatformGenerationResultFailed || row.AssistantMessageID != nil || row.Tokens != 0 || row.ErrorCode == nil || *row.ErrorCode != want.code {
			t.Fatalf("invalid durable failed result for %q", want.model)
		}
	}
	after := f.counts(t)
	if after.conversations != before.conversations+1 || after.messages != before.messages+int64(1+successes) || after.usage != before.usage+int64(successes) || after.receipts != before.receipts+1 || after.results != before.results+int64(len(expected)) || after.dailyCalls != before.dailyCalls+successes || after.totalTokens != before.totalTokens+totalTokens {
		t.Fatalf("durable count/accounting delta mismatch: before=%+v after=%+v", before, after)
	}
	snapshot, err := f.store.Get(context.Background(), f.user.ID, generationID)
	if err != nil || snapshot.State != PlatformGenerationStateCompleted || len(snapshot.Models) != len(expected) {
		t.Fatalf("invalid authoritative completed snapshot")
	}
	for index, want := range expected {
		if snapshot.Models[index] != want.model {
			t.Fatalf("snapshot model order mismatch")
		}
		state := snapshot.ModelStates[want.model]
		if want.code == "" {
			if state.State != PlatformGenerationStateCompleted || state.AssistantMessageGUID != assistantGUIDs[want.model] || state.ErrorCode != "" {
				t.Fatalf("invalid completed snapshot model %q", want.model)
			}
		} else if state.State != PlatformGenerationStateFailed || state.ErrorCode != want.code || state.AssistantMessageGUID != "" {
			t.Fatalf("invalid failed snapshot model %q", want.model)
		}
	}
	view, err := f.control.Get(context.Background(), f.user.ID, generationID)
	if err != nil || view.Status != "completed" || view.Mode == nil || *view.Mode != "compare" || len(view.Results) != len(expected) || view.TotalTokensUsed == nil || *view.TotalTokensUsed != after.totalTokens {
		t.Fatalf("invalid compare GET projection")
	}
	for index, want := range expected {
		got := view.Results[index]
		if got.Model != want.model {
			t.Fatalf("GET result order mismatch at %d", index)
		}
		if want.code == "" {
			if got.Status != "completed" || got.Content != want.content || got.Tokens == nil || *got.Tokens != want.tokens || got.AssistantMessageGUID != assistantGUIDs[want.model] || got.Code != "" {
				t.Fatalf("invalid completed GET result for %q", want.model)
			}
		} else if got.Status != "failed" || got.Code != want.code || got.Content != "" || got.Tokens != nil || got.AssistantMessageGUID != "" {
			t.Fatalf("invalid failed GET result for %q", want.model)
		}
	}
	raw, err := f.store.client.Get(context.Background(), f.store.key(f.user.ID, generationID)).Bytes()
	if err != nil {
		t.Fatalf("read lifecycle metadata: %v", err)
	}
	if bytes.Contains(raw, []byte(platformCompareIntegrationPrompt)) || bytes.Contains(raw, []byte("answer-model")) || bytes.Contains(raw, []byte("fixture-provider")) {
		t.Fatal("Redis lifecycle metadata contains durable/provider content")
	}
	return receipt
}

func waitPlatformCompareIntegrationSnapshot(t *testing.T, f *platformCompareIntegrationFixture, generationID string, predicate func(PlatformGenerationSnapshot) bool) PlatformGenerationSnapshot {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	timer := time.NewTimer(platformCompareIntegrationSyncTimeout)
	defer timer.Stop()
	for {
		snapshot, err := f.store.Get(context.Background(), f.user.ID, generationID)
		if err == nil && predicate(snapshot) {
			return snapshot
		}
		select {
		case <-ticker.C:
		case <-timer.C:
			t.Fatal("timed out waiting for authoritative compare snapshot")
			return PlatformGenerationSnapshot{}
		}
	}
}

func requirePlatformCompareIntegrationAllModelsSucceed(t *testing.T) {
	for _, modelIDs := range [][]string{{"model-a", "model-b"}, {"model-c", "model-a", "model-b"}} {
		modelIDs := modelIDs
		t.Run(strconv.Itoa(len(modelIDs))+"_models", func(t *testing.T) {
			f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, nil, nil, nil))
			before := f.counts(t)
			var output bytes.Buffer
			generationID := platformCompareIntegrationGenerationID()
			result, err := f.run(context.Background(), generationID, modelIDs, func(frame []byte) error { _, writeErr := output.Write(frame); return writeErr })
			if err != nil || !result.Started || result.Duplicate != nil || !strings.Contains(output.String(), "event: done") {
				t.Fatalf("compare success result=%+v err=%v", result, err)
			}
			requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
		})
	}
}

func requirePlatformCompareIntegrationPartialSuccess(t *testing.T) {
	failed := map[string]bool{"model-a": true}
	f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(failed, nil, nil, nil))
	modelIDs := []string{"model-b", "model-a", "model-c"}
	before, generationID := f.counts(t), platformCompareIntegrationGenerationID()
	var output bytes.Buffer
	result, err := f.run(context.Background(), generationID, modelIDs, func(frame []byte) error { _, writeErr := output.Write(frame); return writeErr })
	if err != nil || !result.Started || strings.Count(output.String(), "event: model_error") != 1 || !strings.Contains(output.String(), "event: done") {
		t.Fatalf("partial compare result=%+v err=%v", result, err)
	}
	requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, failed), before)
}

func requirePlatformCompareIntegrationAllModelsFail(t *testing.T) {
	failed := map[string]bool{"model-a": true, "model-b": true, "model-c": true}
	for _, modelIDs := range [][]string{{"model-a", "model-b"}, {"model-c", "model-a", "model-b"}} {
		modelIDs := modelIDs
		t.Run(strconv.Itoa(len(modelIDs))+"_models", func(t *testing.T) {
			f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(failed, nil, nil, nil))
			before, generationID := f.counts(t), platformCompareIntegrationGenerationID()
			var output bytes.Buffer
			result, err := f.run(context.Background(), generationID, modelIDs, func(frame []byte) error { _, writeErr := output.Write(frame); return writeErr })
			if !result.Started || !errors.Is(err, ErrPlatformCompareGenerationUnavailable) || strings.Contains(output.String(), "event: done") || strings.Count(output.String(), "event: error") != 1 {
				t.Fatalf("all-failed compare result=%+v err=%v", result, err)
			}
			after := f.counts(t)
			if after != before {
				t.Fatalf("all-failed durable mutation: before=%+v after=%+v", before, after)
			}
			snapshot, getErr := f.store.Get(context.Background(), f.user.ID, generationID)
			if getErr != nil || snapshot.State != PlatformGenerationStateFailed || len(snapshot.Models) != len(modelIDs) {
				t.Fatalf("invalid all-failed snapshot")
			}
		})
	}
}

func requirePlatformCompareIntegrationDisconnect(t *testing.T) {
	f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, nil, nil, nil))
	modelIDs := []string{"model-b", "model-a"}
	before, generationID := f.counts(t), platformCompareIntegrationGenerationID()
	writes := atomic.Int32{}
	result, err := f.run(context.Background(), generationID, modelIDs, func([]byte) error { writes.Add(1); return io.ErrClosedPipe })
	if err != nil || !result.Started || writes.Load() != 1 {
		t.Fatalf("detached compare result=%+v err=%v writes=%d", result, err, writes.Load())
	}
	requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
}

func requirePlatformCompareIntegrationCancelBeforeResponse(t *testing.T) {
	release, started := make(chan struct{}), make(chan string, 3)
	releaseRun := releasePlatformCompareIntegration(t, release)
	f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, release, started, nil))
	t.Cleanup(releaseRun)
	modelIDs, generationID := []string{"model-a", "model-b", "model-c"}, platformCompareIntegrationGenerationID()
	before := f.counts(t)
	type runOutcome struct {
		result PlatformCompareGenerationRunResult
		err    error
	}
	done := make(chan runOutcome, 1)
	runnerCtx, cancelRunner := context.WithCancel(context.Background())
	t.Cleanup(cancelRunner)
	go func() {
		result, err := f.run(runnerCtx, generationID, modelIDs, nil)
		select {
		case done <- runOutcome{result, err}:
		case <-runnerCtx.Done():
		}
	}()
	for range modelIDs {
		receivePlatformCompareIntegration(t, started, "upstream arrival")
	}
	view, _, cancelErr := f.control.Cancel(context.Background(), f.user.ID, generationID)
	if cancelErr != nil || view.Status != "cancelled" {
		t.Fatalf("cancel compare view=%+v err=%v", view, cancelErr)
	}
	outcome := receivePlatformCompareIntegration(t, done, "cancelled runner completion")
	releaseRun()
	if !outcome.result.Started || !errors.Is(outcome.err, ErrPlatformCompareGenerationUnavailable) {
		t.Fatalf("cancelled run result=%+v err=%v", outcome.result, outcome.err)
	}
	if after := f.counts(t); after != before {
		t.Fatalf("cancel durable mutation: before=%+v after=%+v", before, after)
	}
	snapshot, err := f.store.Get(context.Background(), f.user.ID, generationID)
	if err != nil || snapshot.State != PlatformGenerationStateCancelled {
		t.Fatalf("cancel snapshot invalid")
	}
}

func requirePlatformCompareIntegrationCancelDuringConsumption(t *testing.T) {
	started := make(chan string, 2)
	handlerStop := make(chan struct{})
	stopHandler := releasePlatformCompareIntegration(t, handlerStop)
	handler := func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Model string `json:"model"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model == "" {
			http.Error(w, "invalid", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"safe\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fixture-provider\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", "partial-"+request.Model)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case started <- request.Model:
		case <-r.Context().Done():
			return
		case <-handlerStop:
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-handlerStop:
			return
		}
	}
	f := requirePlatformCompareIntegrationFixture(t, handler)
	t.Cleanup(stopHandler)
	modelIDs, generationID := []string{"model-a", "model-b"}, platformCompareIntegrationGenerationID()
	before := f.counts(t)
	type runOutcome struct {
		result PlatformCompareGenerationRunResult
		err    error
	}
	done := make(chan runOutcome, 1)
	runnerCtx, cancelRunner := context.WithCancel(context.Background())
	t.Cleanup(cancelRunner)
	go func() {
		result, err := f.run(runnerCtx, generationID, modelIDs, nil)
		select {
		case done <- runOutcome{result: result, err: err}:
		case <-runnerCtx.Done():
		}
	}()
	receivePlatformCompareIntegration(t, started, "owned response body")
	receivePlatformCompareIntegration(t, started, "owned response body")
	waitPlatformCompareIntegrationSnapshot(t, f, generationID, func(snapshot PlatformGenerationSnapshot) bool {
		return snapshot.State == PlatformGenerationStateRunning && snapshot.ModelStates["model-a"].Seq == 1 && snapshot.ModelStates["model-b"].Seq == 1
	})
	view, _, cancelErr := f.control.Cancel(context.Background(), f.user.ID, generationID)
	if cancelErr != nil || view.Status != "cancelled" {
		t.Fatalf("cancel owned-body compare view=%+v err=%v", view, cancelErr)
	}
	outcome := receivePlatformCompareIntegration(t, done, "owned-body cancelled runner completion")
	if !outcome.result.Started || !errors.Is(outcome.err, ErrPlatformCompareGenerationUnavailable) {
		t.Fatalf("cancel owned-body run result=%+v err=%v", outcome.result, outcome.err)
	}
	if after := f.counts(t); after != before {
		t.Fatalf("owned-body cancel durable mutation: before=%+v after=%+v", before, after)
	}
	snapshot, err := f.store.Get(context.Background(), f.user.ID, generationID)
	if err != nil || snapshot.State != PlatformGenerationStateCancelled {
		t.Fatalf("owned-body cancel snapshot invalid")
	}
}

func requirePlatformCompareIntegrationRenewal(t *testing.T) {
	release, started := make(chan struct{}), make(chan string, 2)
	releaseRun := releasePlatformCompareIntegration(t, release)
	f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, release, started, nil))
	t.Cleanup(releaseRun)
	clockMillis := atomic.Int64{}
	clockMillis.Store(time.Now().UTC().UnixMilli())
	f.runner.deps.now = func() time.Time { return time.UnixMilli(clockMillis.Load()).UTC() }
	f.control.now = func() time.Time { return time.UnixMilli(clockMillis.Load()).UTC() }
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 3)}
	f.runner.deps.newTimer = timers.New
	modelIDs, generationID := []string{"model-a", "model-b"}, platformCompareIntegrationGenerationID()
	before := f.counts(t)
	done := make(chan error, 1)
	runnerCtx, cancelRunner := context.WithCancel(context.Background())
	t.Cleanup(cancelRunner)
	go func() {
		_, err := f.run(runnerCtx, generationID, modelIDs, nil)
		select {
		case done <- err:
		case <-runnerCtx.Done():
		}
	}()
	receivePlatformCompareIntegration(t, started, "renewal worker arrival")
	receivePlatformCompareIntegration(t, started, "renewal worker arrival")
	initial := waitPlatformCompareIntegrationSnapshot(t, f, generationID, func(snapshot PlatformGenerationSnapshot) bool {
		return snapshot.State == PlatformGenerationStateRunning
	})
	timer := receivePlatformCompareIntegration(t, timers.created, "renewal timer creation")
	clockMillis.Store(initial.CreatedAtMillis + 20_000)
	sendPlatformCompareIntegration(t, timer.ch, time.UnixMilli(clockMillis.Load()), "renewal tick")
	renewed := waitPlatformCompareIntegrationSnapshot(t, f, generationID, func(snapshot PlatformGenerationSnapshot) bool {
		return snapshot.LeaseUntilMillis > initial.LeaseUntilMillis
	})
	converger, err := NewPlatformGenerationConverger(f.control)
	if err != nil {
		t.Fatal(err)
	}
	converger.nowMillis = func() int64 { return initial.LeaseUntilMillis + 1 }
	converger.elapsedNow = time.Now
	if err := converger.RunPass(context.Background()); err != nil {
		t.Fatalf("converger pass: %v", err)
	}
	afterPass, err := f.store.Get(context.Background(), f.user.ID, generationID)
	if err != nil || afterPass.State != PlatformGenerationStateRunning || afterPass.LeaseUntilMillis != renewed.LeaseUntilMillis {
		t.Fatalf("converger defeated renewed lease")
	}
	releaseRun()
	if err := receivePlatformCompareIntegration(t, done, "renewed runner completion"); err != nil {
		t.Fatalf("renewed compare run: %v", err)
	}
	requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
}

type platformCompareIntegrationCountingStore struct {
	platformCompareGenerationStore
	completeCalls  atomic.Int32
	reconcileCalls atomic.Int32
}

type platformCompareIntegrationUnknownPersistence struct {
	delegate platformSingleGenerationPersistence
	calls    atomic.Int32
}

func (p *platformCompareIntegrationUnknownPersistence) Finalize(ctx context.Context, db *gorm.DB, input PlatformGenerationPersistenceInput) (PlatformGenerationReceiptSnapshot, error) {
	p.calls.Add(1)
	if _, err := p.delegate.Finalize(ctx, db, input); err != nil {
		return PlatformGenerationReceiptSnapshot{}, err
	}
	return PlatformGenerationReceiptSnapshot{}, errors.New("fixture finalize acknowledgement lost")
}

func (s *platformCompareIntegrationCountingStore) Complete(ctx context.Context, userID int64, generationID string, guids map[string]string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	s.completeCalls.Add(1)
	return s.platformCompareGenerationStore.Complete(ctx, userID, generationID, guids, nowMillis)
}

func (s *platformCompareIntegrationCountingStore) ReconcileComplete(ctx context.Context, userID int64, generationID string, guids map[string]string, nowMillis int64) (PlatformGenerationSnapshot, error) {
	s.reconcileCalls.Add(1)
	return s.platformCompareGenerationStore.ReconcileComplete(ctx, userID, generationID, guids, nowMillis)
}

func requirePlatformCompareIntegrationCommitUnknownWithRecoveryCounts(t *testing.T) {
	f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, nil, nil, nil))
	countingStore := &platformCompareIntegrationCountingStore{platformCompareGenerationStore: f.runner.deps.store}
	f.runner.deps.store = countingStore
	unknownPersistence := &platformCompareIntegrationUnknownPersistence{delegate: f.runner.deps.persistence}
	f.runner.deps.persistence = unknownPersistence
	originalLoadReceipt := f.runner.deps.loadReceipt
	loadReceiptCalls := atomic.Int32{}
	f.runner.deps.loadReceipt = func(ctx context.Context, db *gorm.DB, userID int64, generationID string) (PlatformGenerationReceiptSnapshot, error) {
		loadReceiptCalls.Add(1)
		return originalLoadReceipt(ctx, db, userID, generationID)
	}
	modelIDs, generationID := []string{"model-c", "model-a"}, platformCompareIntegrationGenerationID()
	before := f.counts(t)
	result, err := f.run(context.Background(), generationID, modelIDs, nil)
	if err != nil || !result.Started || unknownPersistence.calls.Load() != 1 || loadReceiptCalls.Load() != 1 || countingStore.reconcileCalls.Load() != 1 || countingStore.completeCalls.Load() != 0 {
		t.Fatalf("commit unknown result=%+v err=%v finalize=%d loadReceipt=%d reconcile=%d complete=%d", result, err, unknownPersistence.calls.Load(), loadReceiptCalls.Load(), countingStore.reconcileCalls.Load(), countingStore.completeCalls.Load())
	}
	requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
}

func requirePlatformCompareIntegrationRaces(t *testing.T) {
	t.Run("duplicate_claim_once", func(t *testing.T) {
		release, started := make(chan struct{}), make(chan string, 2)
		releaseRun := releasePlatformCompareIntegration(t, release)
		calls := atomic.Int32{}
		f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, release, started, &calls))
		t.Cleanup(releaseRun)
		modelIDs, generationID := []string{"model-b", "model-a"}, platformCompareIntegrationGenerationID()
		before := f.counts(t)
		firstDone := make(chan error, 1)
		runnerCtx, cancelRunner := context.WithCancel(context.Background())
		t.Cleanup(cancelRunner)
		go func() {
			_, err := f.run(runnerCtx, generationID, modelIDs, nil)
			select {
			case firstDone <- err:
			case <-runnerCtx.Done():
			}
		}()
		receivePlatformCompareIntegration(t, started, "duplicate worker arrival")
		receivePlatformCompareIntegration(t, started, "duplicate worker arrival")
		duplicateWrites := atomic.Int32{}
		duplicate, err := f.run(context.Background(), generationID, modelIDs, func([]byte) error { duplicateWrites.Add(1); return nil })
		if err != nil || duplicate.Started || duplicate.Duplicate == nil || duplicate.Duplicate.State != PlatformGenerationStateRunning || duplicateWrites.Load() != 0 || calls.Load() != int32(len(modelIDs)) {
			t.Fatalf("duplicate side effect result=%+v err=%v writes=%d calls=%d", duplicate, err, duplicateWrites.Load(), calls.Load())
		}
		releaseRun()
		if err := receivePlatformCompareIntegration(t, firstDone, "duplicate winner completion"); err != nil {
			t.Fatalf("first duplicate-race run: %v", err)
		}
		requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
	})

	t.Run("concurrent_quota", func(t *testing.T) {
		release, started := make(chan struct{}), make(chan string, 5)
		releaseRun := releasePlatformCompareIntegration(t, release)
		calls := atomic.Int32{}
		f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, release, started, &calls))
		t.Cleanup(releaseRun)
		now := time.Now().UTC().UnixMilli()
		f.configureUser(t, models.PlanFree, 3, 0, now)
		modelSets := [][]string{{"model-a", "model-b"}, {"model-c", "model-a", "model-b"}}
		generationIDs := []string{platformCompareIntegrationGenerationID(), platformCompareIntegrationGenerationID()}
		before := f.counts(t)
		type outcome struct {
			index  int
			result PlatformCompareGenerationRunResult
			err    error
		}
		outcomes := make(chan outcome, 2)
		for index := range generationIDs {
			index := index
			runnerCtx, cancelRunner := context.WithCancel(context.Background())
			t.Cleanup(cancelRunner)
			go func() {
				result, err := f.run(runnerCtx, generationIDs[index], modelSets[index], nil)
				select {
				case outcomes <- outcome{index: index, result: result, err: err}:
				case <-runnerCtx.Done():
				}
			}()
		}
		for totalWorkers := 0; totalWorkers < 5; totalWorkers++ {
			receivePlatformCompareIntegration(t, started, "quota competitor worker arrival")
		}
		if calls.Load() != 5 {
			t.Fatalf("quota competitors did not both reach upstream: calls=%d", calls.Load())
		}
		releaseRun()
		winner := -1
		for range generationIDs {
			got := receivePlatformCompareIntegration(t, outcomes, "quota competitor completion")
			if got.err == nil {
				if winner != -1 || !got.result.Started {
					t.Fatalf("unexpected second/non-started quota winner")
				}
				winner = got.index
			} else if !got.result.Started || !errors.Is(got.err, ErrPlatformCompareGenerationUnavailable) {
				t.Fatalf("unexpected quota loser result=%+v err=%v", got.result, got.err)
			}
		}
		if winner == -1 {
			t.Fatal("concurrent quota race had no winner")
		}
		requirePlatformCompareIntegrationReceipt(t, f, generationIDs[winner], platformCompareIntegrationExpected(modelSets[winner], nil), before)
	})
}

func requirePlatformCompareIntegrationQuotaCapacityMatrix(t *testing.T) {
	for _, modelIDs := range [][]string{{"model-a", "model-b"}, {"model-c", "model-a", "model-b"}} {
		for _, capacity := range []struct {
			name  string
			limit int
			deny  bool
		}{
			{name: "models_minus_one", limit: len(modelIDs) - 1, deny: true},
			{name: "exact", limit: len(modelIDs)},
			{name: "more", limit: len(modelIDs) + 1},
		} {
			modelIDs, capacity := append([]string(nil), modelIDs...), capacity
			t.Run(fmt.Sprintf("%d_models_%s", len(modelIDs), capacity.name), func(t *testing.T) {
				calls := atomic.Int32{}
				f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, nil, nil, &calls))
				now := time.Now().UTC().UnixMilli()
				f.configureUser(t, models.PlanFree, capacity.limit, 0, now)
				before, generationID := f.counts(t), platformCompareIntegrationGenerationID()
				result, err := f.run(context.Background(), generationID, modelIDs, nil)
				if capacity.deny {
					if !errors.Is(err, ErrPlatformCompareGenerationQuota) || result.Started || calls.Load() != 0 || f.counts(t) != before {
						t.Fatalf("quota matrix reject result=%+v err=%v calls=%d", result, err, calls.Load())
					}
					return
				}
				if err != nil || !result.Started || calls.Load() != int32(len(modelIDs)) {
					t.Fatalf("quota matrix accept result=%+v err=%v calls=%d", result, err, calls.Load())
				}
				requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
			})
		}
	}
	for _, plan := range []models.PlanType{models.PlanProfessional, models.PlanEnterprise} {
		plan := plan
		t.Run("unlimited_"+plan.String(), func(t *testing.T) {
			f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, nil, nil, nil))
			now := time.Now().UTC().UnixMilli()
			f.configureUser(t, plan, 0, 9, now)
			modelIDs, generationID := []string{"model-a", "model-b", "model-c"}, platformCompareIntegrationGenerationID()
			before := f.counts(t)
			result, err := f.run(context.Background(), generationID, modelIDs, nil)
			if err != nil || !result.Started {
				t.Fatalf("unlimited plan result=%+v err=%v", result, err)
			}
			requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
		})
	}
}

func requirePlatformCompareIntegrationDailyResetBoundary(t *testing.T) {
	boundary := time.Date(2026, time.December, 2, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		now  time.Time
		deny bool
	}{
		{name: "immediate_before", now: boundary.Add(-time.Millisecond), deny: true},
		{name: "immediate_after", now: boundary},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			calls := atomic.Int32{}
			f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, nil, nil, &calls))
			resetAt := boundary.Add(-time.Millisecond).UnixMilli()
			f.configureUser(t, models.PlanFree, 2, 2, resetAt)
			f.runner.deps.now = func() time.Time { return test.now }
			f.control.now = func() time.Time { return test.now }
			modelIDs, generationID := []string{"model-a", "model-b"}, platformCompareIntegrationGenerationID()
			before := f.counts(t)
			result, err := f.run(context.Background(), generationID, modelIDs, nil)
			if test.deny {
				if !errors.Is(err, ErrPlatformCompareGenerationQuota) || result.Started || calls.Load() != 0 || f.counts(t) != before {
					t.Fatalf("reset-before result=%+v err=%v calls=%d", result, err, calls.Load())
				}
				return
			}
			if err != nil || !result.Started {
				t.Fatalf("reset-after result=%+v err=%v", result, err)
			}
			before.dailyCalls = 0
			requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
		})
	}
}

func TestPlatformCompareGenerationIntegrationAllModelsSucceed(t *testing.T) {
	requirePlatformCompareIntegrationAllModelsSucceed(t)
}

func TestPlatformCompareGenerationIntegrationPartialSuccess(t *testing.T) {
	requirePlatformCompareIntegrationPartialSuccess(t)
}

func TestPlatformCompareGenerationIntegrationAllModelsFailWithoutDurableMutation(t *testing.T) {
	requirePlatformCompareIntegrationAllModelsFail(t)
}

func TestPlatformCompareGenerationIntegrationDisconnectThenGET(t *testing.T) {
	requirePlatformCompareIntegrationDisconnect(t)
}

func TestPlatformCompareGenerationIntegrationCancelNoPersistence(t *testing.T) {
	requirePlatformCompareIntegrationCancelBeforeResponse(t)
	requirePlatformCompareIntegrationCancelDuringConsumption(t)
}

func TestPlatformCompareGenerationIntegrationRenewalBeatsConverger(t *testing.T) {
	requirePlatformCompareIntegrationRenewal(t)
}

func TestPlatformCompareGenerationIntegrationCommitUnknownReconciles(t *testing.T) {
	requirePlatformCompareIntegrationCommitUnknownWithRecoveryCounts(t)
}

func TestPlatformCompareGenerationIntegrationConcurrentQuotaAndDuplicateRaces(t *testing.T) {
	requirePlatformCompareIntegrationRaces(t)
	requirePlatformCompareIntegrationQuotaCapacityMatrix(t)
	requirePlatformCompareIntegrationDailyResetBoundary(t)
}
