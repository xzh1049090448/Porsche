package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

const platformCompareTestGenerationID = "93000000-0000-4000-8000-000000000001"

type platformCompareTestEffects struct {
	claim, persist, admission, register, upstream, receipt, write int
}

type platformCompareTestStore struct{ effects *platformCompareTestEffects }

func (s *platformCompareTestStore) Claim(context.Context, PlatformGenerationClaimInput) (PlatformGenerationClaimResult, error) {
	s.effects.claim++
	return PlatformGenerationClaimResult{}, errors.New("unexpected claim")
}

type platformCompareTestPersistence struct{ effects *platformCompareTestEffects }

func (p *platformCompareTestPersistence) Finalize(context.Context, *gorm.DB, PlatformGenerationPersistenceInput) (PlatformGenerationReceiptSnapshot, error) {
	p.effects.persist++
	return PlatformGenerationReceiptSnapshot{}, errors.New("unexpected persistence")
}

type platformCompareTestRegistry struct{ effects *platformCompareTestEffects }

func (r *platformCompareTestRegistry) BeginAdmission() (func(), error) {
	r.effects.admission++
	return func() {}, nil
}
func (r *platformCompareTestRegistry) Register(int64, string, context.CancelFunc) (string, error) {
	r.effects.register++
	return "unexpected", nil
}
func (*platformCompareTestRegistry) Unregister(int64, string, string) bool { return false }

type platformCompareTestUpstream struct{ effects *platformCompareTestEffects }

func (u *platformCompareTestUpstream) Chat(context.Context, []byte) (*http.Response, *whitelabel.Error) {
	u.effects.upstream++
	return nil, whitelabel.ErrUpstreamUnavailable("unexpected upstream")
}
func (*platformCompareTestUpstream) ConsumeChatCompletionSSEContext(context.Context, io.Reader, string, func(whitelabel.ChatCompletionChunk) error) *whitelabel.Error {
	return whitelabel.ErrUpstreamUnavailable("unexpected upstream consume")
}

func platformCompareTestUser() *models.User {
	return &models.User{
		ID: 17, AuditFields: models.AuditFields{IsDeleted: 0}, Status: models.UserStatusActive,
		PlanType: models.PlanFree, DailyCallLimit: 10, DailyCallsUsed: 1,
	}
}

func platformCompareTestParams() ChatParams {
	maxTokens := 64
	contextWindow := 1
	return ChatParams{
		Messages: []map[string]interface{}{
			{"role": "system", "content": "system", "nested": map[string]interface{}{"items": []interface{}{"original"}}},
			{"role": "user", "content": "hello"},
		},
		MaxTokens:      &maxTokens,
		ContextWindow:  &contextWindow,
		WhiteLabelBody: []byte(`{"model":"ignored","models":["model-a","model-b"],"messages":[{"role":"user","content":"stale"}],"max_tokens":64,"stream":true,"stream_version":"platform-chat-sse.v2","generation_id":"93000000-0000-4000-8000-000000000001"}`),
	}
}

func platformCompareTestInput() PlatformCompareGenerationInput {
	return PlatformCompareGenerationInput{
		Context: context.Background(), User: platformCompareTestUser(), GenerationID: platformCompareTestGenerationID,
		RequestID: "request-compare", Models: []string{"model-b", "model-a"}, Params: platformCompareTestParams(), Write: func([]byte) error { return nil },
	}
}

func platformCompareTestRunner(now time.Time) (*PlatformCompareGenerationRunner, *platformCompareTestEffects) {
	effects := &platformCompareTestEffects{}
	deps := platformCompareGenerationDeps{
		db: &gorm.DB{}, store: &platformCompareTestStore{effects: effects}, persistence: &platformCompareTestPersistence{effects: effects},
		registry: &platformCompareTestRegistry{effects: effects}, upstream: &platformCompareTestUpstream{effects: effects}, rootContext: context.Background(),
		now: func() time.Time { return now }, newGUID: func() int64 { return 8101 },
		loadConversation: func(context.Context, *gorm.DB, int64, int64) error { return nil },
		loadReceipt: func(context.Context, *gorm.DB, int64, string) (PlatformGenerationReceiptSnapshot, error) {
			effects.receipt++
			return PlatformGenerationReceiptSnapshot{}, errors.New("unexpected receipt")
		},
		upstreamTimeout: time.Minute,
	}
	return &PlatformCompareGenerationRunner{deps: deps}, effects
}

func requireNoPlatformComparePostPrepareEffects(t *testing.T, effects *platformCompareTestEffects) {
	t.Helper()
	if effects.claim != 0 || effects.persist != 0 || effects.admission != 0 || effects.register != 0 || effects.upstream != 0 || effects.receipt != 0 || effects.write != 0 {
		t.Fatalf("unexpected post-prepare effects: %+v", effects)
	}
}

func TestPlatformCompareGenerationRejectsInvalidDependencies(t *testing.T) {
	db := &gorm.DB{}
	store := &PlatformGenerationStore{}
	persistence := &PlatformGenerationPersistence{}
	registry := &PlatformGenerationCancellationRegistry{}
	upstream := &whitelabel.WhiteLabelService{}
	valid := func(db *gorm.DB, store *PlatformGenerationStore, persistence *PlatformGenerationPersistence, registry *PlatformGenerationCancellationRegistry, upstream *whitelabel.WhiteLabelService, root context.Context, timeout time.Duration) error {
		_, err := NewPlatformCompareGenerationRunner(db, store, persistence, registry, upstream, root, timeout)
		return err
	}
	tests := []struct {
		name string
		run  func() error
	}{
		{"nil db", func() error {
			return valid(nil, store, persistence, registry, upstream, context.Background(), time.Minute)
		}},
		{"nil store", func() error {
			return valid(db, nil, persistence, registry, upstream, context.Background(), time.Minute)
		}},
		{"nil persistence", func() error { return valid(db, store, nil, registry, upstream, context.Background(), time.Minute) }},
		{"nil registry", func() error { return valid(db, store, persistence, nil, upstream, context.Background(), time.Minute) }},
		{"nil upstream", func() error { return valid(db, store, persistence, registry, nil, context.Background(), time.Minute) }},
		{"nil root context", func() error { return valid(db, store, persistence, registry, upstream, nil, time.Minute) }},
		{"zero timeout", func() error { return valid(db, store, persistence, registry, upstream, context.Background(), 0) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(); !errors.Is(err, ErrPlatformCompareGenerationUnavailable) {
				t.Fatalf("constructor error=%v, want unavailable", err)
			}
		})
	}
	runner, err := NewPlatformCompareGenerationRunner(db, store, persistence, registry, upstream, context.Background(), time.Minute)
	if err != nil || runner == nil || runner.deps.loadReceipt == nil {
		t.Fatalf("valid constructor runner=%#v error=%v", runner, err)
	}
	runner.deps.loadReceipt = nil
	if _, err := runner.Run(platformCompareTestInput()); !errors.Is(err, ErrPlatformCompareGenerationUnavailable) {
		t.Fatalf("nil receipt reader Run error=%v, want unavailable", err)
	}
}

func TestPlatformCompareGenerationPreflightRejectsBeforeClaim(t *testing.T) {
	invalidUTF8 := string([]byte{0xff})
	maxContext := int(^uint(0) >> 1)
	overMessage := strings.Repeat("x", platformGenerationMessageTextMaxBytes+1)
	tests := []struct {
		name   string
		mutate func(*PlatformCompareGenerationInput)
		want   error
	}{
		{"nil context", func(in *PlatformCompareGenerationInput) { in.Context = nil }, ErrPlatformCompareGenerationInvalid},
		{"cancelled context", func(in *PlatformCompareGenerationInput) {
			ctx, cancel := context.WithCancel(in.Context)
			cancel()
			in.Context = ctx
		}, ErrPlatformCompareGenerationUnavailable},
		{"nil user", func(in *PlatformCompareGenerationInput) { in.User = nil }, ErrPlatformCompareGenerationInvalid},
		{"nil write", func(in *PlatformCompareGenerationInput) { in.Write = nil }, ErrPlatformCompareGenerationInvalid},
		{"uppercase generation", func(in *PlatformCompareGenerationInput) { in.GenerationID = "93000000-0000-4000-8000-00000000000A" }, ErrPlatformCompareGenerationInvalid},
		{"last assistant", func(in *PlatformCompareGenerationInput) { in.Params.Messages[1]["role"] = "assistant" }, ErrPlatformCompareGenerationInvalid},
		{"empty final message", func(in *PlatformCompareGenerationInput) { in.Params.Messages[1]["content"] = "" }, ErrPlatformCompareGenerationInvalid},
		{"invalid utf8", func(in *PlatformCompareGenerationInput) { in.Params.Messages[1]["content"] = invalidUTF8 }, ErrPlatformCompareGenerationInvalid},
		{"oversize final message", func(in *PlatformCompareGenerationInput) { in.Params.Messages[1]["content"] = overMessage }, ErrPlatformCompareGenerationInvalid},
		{"unsupported nested value", func(in *PlatformCompareGenerationInput) { in.Params.Messages[0]["nested"] = make(chan int) }, ErrPlatformCompareGenerationInvalid},
		{"nil params max tokens", func(in *PlatformCompareGenerationInput) { in.Params.MaxTokens = nil }, ErrPlatformCompareGenerationInvalid},
		{"zero params max tokens", func(in *PlatformCompareGenerationInput) { zero := 0; in.Params.MaxTokens = &zero }, ErrPlatformCompareGenerationInvalid},
		{"invalid max tokens", func(in *PlatformCompareGenerationInput) {
			in.Params.WhiteLabelBody = []byte(`{"model":"x","messages":[{"role":"user","content":"hello"}],"max_tokens":0}`)
		}, ErrPlatformCompareGenerationInvalid},
		{"unsupported payload field", func(in *PlatformCompareGenerationInput) {
			in.Params.WhiteLabelBody = []byte(`{"model":"x","messages":[{"role":"user","content":"hello"}],"max_tokens":64,"secret_extension":true}`)
		}, ErrPlatformCompareGenerationInvalid},
		{"oversize request", func(in *PlatformCompareGenerationInput) {
			in.Params.WhiteLabelBody = make([]byte, whitelabel.MaxRequestBodyBytes+1)
		}, ErrPlatformCompareGenerationInvalid},
		{"explicit usage disabled", func(in *PlatformCompareGenerationInput) {
			in.Params.WhiteLabelBody = []byte(`{"model":"x","messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream_options":{"include_usage":false}}`)
		}, ErrPlatformCompareGenerationInvalid},
		{"context trim leaves assistant", func(in *PlatformCompareGenerationInput) {
			one := 1
			in.Params.ContextWindow = &one
			in.Params.Messages = []map[string]interface{}{{"role": "user", "content": "x"}, {"role": "user", "content": "y"}, {"role": "assistant", "content": "z"}}
		}, ErrPlatformCompareGenerationInvalid},
		{"context overflow remains safe", func(in *PlatformCompareGenerationInput) {
			in.Params.ContextWindow = &maxContext
			in.Params.Messages[1]["role"] = "assistant"
		}, ErrPlatformCompareGenerationInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
			input := platformCompareTestInput()
			input.Write = func([]byte) error { effects.write++; return nil }
			test.mutate(&input)
			result, err := runner.Run(input)
			if !errors.Is(err, test.want) || result.Started || result.Duplicate != nil {
				t.Fatalf("Run result=%+v error=%v, want=%v", result, err, test.want)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}

func TestPlatformCompareGenerationRequiresTwoOrThreeDistinctModels(t *testing.T) {
	tests := []struct {
		name   string
		models []string
		valid  bool
	}{
		{"none", nil, false}, {"one", []string{"a"}, false}, {"two", []string{"b", "a"}, true}, {"three", []string{"c", "a", "b"}, true},
		{"four", []string{"a", "b", "c", "d"}, false}, {"duplicate", []string{"a", "a"}, false}, {"blank", []string{"a", ""}, false},
		{"invalid utf8", []string{"a", string([]byte{0xff})}, false}, {"too long", []string{"a", strings.Repeat("m", platformSSEV2MaxModelIdentifierBytes+1)}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
			input := platformCompareTestInput()
			input.Models = test.models
			run, _, err := runner.prepare(input)
			if test.valid {
				if err != nil || !reflect.DeepEqual(run.models, test.models) || run.encoder == nil {
					t.Fatalf("prepare run=%+v error=%v", run, err)
				}
			} else if !errors.Is(err, ErrPlatformCompareGenerationInvalid) {
				t.Fatalf("prepare error=%v, want invalid", err)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}

func TestPlatformCompareGenerationCopiesRequestBeforeClaim(t *testing.T) {
	runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
	input := platformCompareTestInput()
	run, claimAt, err := runner.prepare(input)
	if err != nil || claimAt != 10_000 || !reflect.DeepEqual(run.models, []string{"model-b", "model-a"}) || run.encoder == nil {
		t.Fatalf("prepare run=%+v claimAt=%d error=%v", run, claimAt, err)
	}
	input.Models[0] = "mutated"
	input.Params.Messages[0]["role"] = "mutated"
	input.Params.Messages[0]["nested"].(map[string]interface{})["items"].([]interface{})[0] = "mutated"
	input.Params.WhiteLabelBody[0] = '['
	*input.Params.MaxTokens = 1
	*input.Params.ContextWindow = 99
	input.User.ID = 999
	preparedBody := string(run.params.WhiteLabelBody)
	if !reflect.DeepEqual(run.models, []string{"model-b", "model-a"}) || run.params.Messages[0]["role"] != "system" || run.params.Messages[0]["nested"].(map[string]interface{})["items"].([]interface{})[0] != "original" || len(preparedBody) == 0 || preparedBody[0] != '{' || strings.Contains(preparedBody, "stream_version") || strings.Contains(preparedBody, "generation_id") || strings.Contains(preparedBody, `"models"`) || *run.params.MaxTokens != 64 || *run.params.ContextWindow != 1 || run.userID != 17 {
		t.Fatalf("prepared run changed after caller mutation: %+v", run)
	}
	for _, model := range []string{"model-b", "model-a"} {
		payload := string(run.payloads[model])
		if !strings.Contains(payload, `"model":"`+model+`"`) || strings.Contains(payload, "stream_version") || strings.Contains(payload, "generation_id") || strings.Contains(payload, `"models"`) {
			t.Fatalf("unsafe payload for %s: %s", model, payload)
		}
	}
	requireNoPlatformComparePostPrepareEffects(t, effects)
}

func TestPlatformCompareGenerationValidatesConversationOwnershipAndQuota(t *testing.T) {
	now := time.UnixMilli(10_000).UTC()
	t.Run("owned conversation", func(t *testing.T) {
		runner, _ := platformCompareTestRunner(now)
		var gotUserID, gotGUID int64
		runner.deps.newGUID = func() int64 { t.Fatal("new GUID called for existing conversation"); return 0 }
		runner.deps.loadConversation = func(_ context.Context, _ *gorm.DB, userID, guid int64) error {
			gotUserID, gotGUID = userID, guid
			return nil
		}
		input := platformCompareTestInput()
		guid := "8201"
		input.Params.ConversationGUID = &guid
		run, _, err := runner.prepare(input)
		if err != nil || gotUserID != input.User.ID || gotGUID != 8201 || run.existingConversationGUID == nil || *run.existingConversationGUID != 8201 || run.reservedConversationGUID != nil {
			t.Fatalf("run=%+v user=%d guid=%d error=%v", run, gotUserID, gotGUID, err)
		}
	})
	for _, test := range []struct {
		name    string
		loadErr error
		want    error
	}{{"not owned", gorm.ErrRecordNotFound, ErrPlatformCompareGenerationInvalid}, {"database unavailable", errors.New("db down"), ErrPlatformCompareGenerationUnavailable}} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(now)
			runner.deps.loadConversation = func(context.Context, *gorm.DB, int64, int64) error { return test.loadErr }
			input := platformCompareTestInput()
			guid := "8201"
			input.Params.ConversationGUID = &guid
			if _, _, err := runner.prepare(input); !errors.Is(err, test.want) {
				t.Fatalf("prepare error=%v, want=%v", err, test.want)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
	t.Run("malformed conversation guid", func(t *testing.T) {
		runner, effects := platformCompareTestRunner(now)
		input := platformCompareTestInput()
		guid := "not-a-guid"
		input.Params.ConversationGUID = &guid
		if _, _, err := runner.prepare(input); !errors.Is(err, ErrPlatformCompareGenerationInvalid) {
			t.Fatalf("prepare error=%v, want invalid", err)
		}
		requireNoPlatformComparePostPrepareEffects(t, effects)
	})
	for _, mutate := range []func(*models.User){
		func(user *models.User) { user.ID = 0 },
		func(user *models.User) { user.Status = models.UserStatusDisabled },
		func(user *models.User) { user.IsDeleted = 1 },
	} {
		runner, effects := platformCompareTestRunner(now)
		input := platformCompareTestInput()
		mutate(input.User)
		if _, _, err := runner.prepare(input); !errors.Is(err, ErrPlatformCompareGenerationQuota) {
			t.Fatalf("inactive/deleted prepare error=%v, want quota", err)
		}
		requireNoPlatformComparePostPrepareEffects(t, effects)
	}
}

func TestPlatformCompareGenerationQuotaRequiresCapacityForEveryRequestedModel(t *testing.T) {
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	reset := now.UnixMilli()
	for _, test := range []struct {
		name   string
		models []string
		plan   models.PlanType
		limit  int
		used   int
		valid  bool
	}{
		{"two exact capacity", []string{"a", "b"}, models.PlanFree, 3, 1, true}, {"two short", []string{"a", "b"}, models.PlanFree, 3, 2, false},
		{"three exact capacity", []string{"a", "b", "c"}, models.PlanFree, 4, 1, true}, {"three short", []string{"a", "b", "c"}, models.PlanFree, 4, 2, false},
		{"other daily limited plan", []string{"a", "b"}, models.PlanType(99), 2, 1, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(now)
			input := platformCompareTestInput()
			input.Models = test.models
			input.User.PlanType, input.User.DailyCallLimit, input.User.DailyCallsUsed, input.User.DailyCallsResetAt = test.plan, test.limit, test.used, &reset
			_, _, err := runner.prepare(input)
			if test.valid && err != nil {
				t.Fatalf("prepare error=%v", err)
			}
			if !test.valid && !errors.Is(err, ErrPlatformCompareGenerationQuota) {
				t.Fatalf("prepare error=%v, want quota", err)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}

func TestPlatformCompareGenerationQuotaAppliesDailyReset(t *testing.T) {
	now := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		reset *int64
		valid bool
	}{
		{"nil reset", nil, true},
		{"one millisecond before day", func() *int64 { value := now.Add(-time.Millisecond).UnixMilli(); return &value }(), true},
		{"day boundary", func() *int64 { value := now.UnixMilli(); return &value }(), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner, effects := platformCompareTestRunner(now)
			input := platformCompareTestInput()
			input.User.DailyCallLimit, input.User.DailyCallsUsed, input.User.DailyCallsResetAt = 2, 2, test.reset
			_, _, err := runner.prepare(input)
			if test.valid && err != nil {
				t.Fatalf("prepare error=%v", err)
			}
			if !test.valid && !errors.Is(err, ErrPlatformCompareGenerationQuota) {
				t.Fatalf("prepare error=%v, want quota", err)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}

func TestPlatformCompareGenerationProfessionalAndEnterpriseQuotaRemainUnlimited(t *testing.T) {
	for _, plan := range []models.PlanType{models.PlanProfessional, models.PlanEnterprise} {
		t.Run(plan.String(), func(t *testing.T) {
			runner, effects := platformCompareTestRunner(time.UnixMilli(10_000).UTC())
			input := platformCompareTestInput()
			input.Models = []string{"a", "b", "c"}
			input.User.PlanType, input.User.DailyCallLimit, input.User.DailyCallsUsed = plan, -1, -1
			if _, _, err := runner.prepare(input); err != nil {
				t.Fatalf("unlimited plan prepare error=%v", err)
			}
			requireNoPlatformComparePostPrepareEffects(t, effects)
		})
	}
}
