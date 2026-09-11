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
	"sync/atomic"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

const platformCompareIntegrationPrompt = "compare integration prompt"

var platformCompareIntegrationSequence atomic.Uint64

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
			started <- request.Model
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
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := f.store.Get(context.Background(), f.user.ID, generationID)
		if err == nil && predicate(snapshot) {
			return snapshot
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for authoritative compare snapshot")
	return PlatformGenerationSnapshot{}
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

func requirePlatformCompareIntegrationCancel(t *testing.T) {
	release, started := make(chan struct{}), make(chan string, 3)
	f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, release, started, nil))
	modelIDs, generationID := []string{"model-a", "model-b", "model-c"}, platformCompareIntegrationGenerationID()
	before := f.counts(t)
	type runOutcome struct {
		result PlatformCompareGenerationRunResult
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, err := f.run(context.Background(), generationID, modelIDs, nil)
		done <- runOutcome{result, err}
	}()
	for range modelIDs {
		<-started
	}
	view, _, cancelErr := f.control.Cancel(context.Background(), f.user.ID, generationID)
	if cancelErr != nil || view.Status != "cancelled" {
		t.Fatalf("cancel compare view=%+v err=%v", view, cancelErr)
	}
	outcome := <-done
	close(release)
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

func requirePlatformCompareIntegrationRenewal(t *testing.T) {
	release, started := make(chan struct{}), make(chan string, 2)
	f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, release, started, nil))
	clockMillis := atomic.Int64{}
	clockMillis.Store(time.Now().UTC().UnixMilli())
	f.runner.deps.now = func() time.Time { return time.UnixMilli(clockMillis.Load()).UTC() }
	f.control.now = func() time.Time { return time.UnixMilli(clockMillis.Load()).UTC() }
	timers := &platformSingleTestTimerFactory{created: make(chan *platformSingleTestTimer, 3)}
	f.runner.deps.newTimer = timers.New
	modelIDs, generationID := []string{"model-a", "model-b"}, platformCompareIntegrationGenerationID()
	before := f.counts(t)
	done := make(chan error, 1)
	go func() { _, err := f.run(context.Background(), generationID, modelIDs, nil); done <- err }()
	<-started
	<-started
	initial := waitPlatformCompareIntegrationSnapshot(t, f, generationID, func(snapshot PlatformGenerationSnapshot) bool {
		return snapshot.State == PlatformGenerationStateRunning
	})
	timer := <-timers.created
	clockMillis.Store(initial.CreatedAtMillis + 20_000)
	timer.ch <- time.UnixMilli(clockMillis.Load())
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
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("renewed compare run: %v", err)
	}
	requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
}

func requirePlatformCompareIntegrationCommitUnknown(t *testing.T) {
	f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, nil, nil, nil))
	original := f.persistence.runLocked
	attempts := atomic.Int32{}
	f.persistence.runLocked = func(ctx context.Context, db *gorm.DB, lockName string, fn func(*gorm.DB) error) error {
		attempts.Add(1)
		if err := original(ctx, db, lockName, fn); err != nil {
			return err
		}
		return errors.New("fixture commit acknowledgement lost")
	}
	modelIDs, generationID := []string{"model-c", "model-a"}, platformCompareIntegrationGenerationID()
	before := f.counts(t)
	result, err := f.run(context.Background(), generationID, modelIDs, nil)
	if err != nil || !result.Started || attempts.Load() != 1 {
		t.Fatalf("commit unknown result=%+v err=%v finalize attempts=%d", result, err, attempts.Load())
	}
	requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
}

func requirePlatformCompareIntegrationRaces(t *testing.T) {
	t.Run("duplicate_claim_once", func(t *testing.T) {
		release, started := make(chan struct{}), make(chan string, 2)
		calls := atomic.Int32{}
		f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, release, started, &calls))
		modelIDs, generationID := []string{"model-b", "model-a"}, platformCompareIntegrationGenerationID()
		before := f.counts(t)
		firstDone := make(chan error, 1)
		go func() { _, err := f.run(context.Background(), generationID, modelIDs, nil); firstDone <- err }()
		<-started
		<-started
		duplicateWrites := atomic.Int32{}
		duplicate, err := f.run(context.Background(), generationID, modelIDs, func([]byte) error { duplicateWrites.Add(1); return nil })
		if err != nil || duplicate.Started || duplicate.Duplicate == nil || duplicate.Duplicate.State != PlatformGenerationStateRunning || duplicateWrites.Load() != 0 || calls.Load() != int32(len(modelIDs)) {
			t.Fatalf("duplicate side effect result=%+v err=%v writes=%d calls=%d", duplicate, err, duplicateWrites.Load(), calls.Load())
		}
		close(release)
		if err := <-firstDone; err != nil {
			t.Fatalf("first duplicate-race run: %v", err)
		}
		requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(modelIDs, nil), before)
	})

	t.Run("concurrent_quota", func(t *testing.T) {
		calls := atomic.Int32{}
		f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, nil, nil, &calls))
		now := time.Now().UTC().UnixMilli()
		f.configureUser(t, models.PlanFree, 2, 0, now)
		modelIDs := []string{"model-a", "model-b"}
		generationIDs := []string{platformCompareIntegrationGenerationID(), platformCompareIntegrationGenerationID()}
		before := f.counts(t)
		type outcome struct {
			index  int
			result PlatformCompareGenerationRunResult
			err    error
		}
		start := make(chan struct{})
		outcomes := make(chan outcome, 2)
		for index := range generationIDs {
			index := index
			go func() {
				<-start
				result, err := f.run(context.Background(), generationIDs[index], modelIDs, nil)
				outcomes <- outcome{index: index, result: result, err: err}
			}()
		}
		close(start)
		winner := -1
		for range generationIDs {
			got := <-outcomes
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
		requirePlatformCompareIntegrationReceipt(t, f, generationIDs[winner], platformCompareIntegrationExpected(modelIDs, nil), before)
		if calls.Load() != 4 {
			t.Fatalf("quota race upstream calls=%d want 4", calls.Load())
		}
	})

	for _, test := range []struct {
		name       string
		plan       models.PlanType
		limit      int
		used       int
		models     []string
		resetAt    func() int64
		resetDaily bool
	}{
		{name: "limited_models_minus_one", plan: models.PlanFree, limit: 1, models: []string{"model-a", "model-b"}},
		{name: "limited_exact", plan: models.PlanFree, limit: 2, models: []string{"model-a", "model-b"}},
		{name: "limited_more", plan: models.PlanFree, limit: 3, models: []string{"model-a", "model-b"}},
		{name: "daily_reset_boundary", plan: models.PlanFree, limit: 2, used: 2, models: []string{"model-a", "model-b"}, resetAt: func() int64 { return time.Now().UTC().Add(-24 * time.Hour).UnixMilli() }, resetDaily: true},
		{name: "professional_unlimited", plan: models.PlanProfessional, limit: 0, used: 9, models: []string{"model-a", "model-b", "model-c"}},
		{name: "enterprise_unlimited", plan: models.PlanEnterprise, limit: 0, used: 9, models: []string{"model-a", "model-b", "model-c"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			calls := atomic.Int32{}
			f := requirePlatformCompareIntegrationFixture(t, platformCompareIntegrationHandler(nil, nil, nil, &calls))
			resetAt := time.Now().UTC().UnixMilli()
			if test.resetAt != nil {
				resetAt = test.resetAt()
			}
			f.configureUser(t, test.plan, test.limit, test.used, resetAt)
			before, generationID := f.counts(t), platformCompareIntegrationGenerationID()
			result, err := f.run(context.Background(), generationID, test.models, nil)
			wantQuota := test.name == "limited_models_minus_one"
			if wantQuota {
				if !errors.Is(err, ErrPlatformCompareGenerationQuota) || result.Started || calls.Load() != 0 || f.counts(t) != before {
					t.Fatalf("quota preflight result=%+v err=%v calls=%d", result, err, calls.Load())
				}
				return
			}
			if err != nil || !result.Started {
				t.Fatalf("quota capacity result=%+v err=%v", result, err)
			}
			if test.resetDaily {
				before.dailyCalls = 0
			}
			requirePlatformCompareIntegrationReceipt(t, f, generationID, platformCompareIntegrationExpected(test.models, nil), before)
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
	requirePlatformCompareIntegrationCancel(t)
}

func TestPlatformCompareGenerationIntegrationRenewalBeatsConverger(t *testing.T) {
	requirePlatformCompareIntegrationRenewal(t)
}

func TestPlatformCompareGenerationIntegrationCommitUnknownReconciles(t *testing.T) {
	requirePlatformCompareIntegrationCommitUnknown(t)
}

func TestPlatformCompareGenerationIntegrationConcurrentQuotaAndDuplicateRaces(t *testing.T) {
	requirePlatformCompareIntegrationRaces(t)
}
