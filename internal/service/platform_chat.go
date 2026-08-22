package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/gateway"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/rag"
	"github.com/porsche/ai-gateway-go/internal/registry"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

type PlatformDeps struct {
	Settings   *config.Settings
	DB         *gorm.DB
	Models     *registry.ModelRegistry
	Clients    *registry.ClientRegistry
	Gateway    *gateway.Service
	RAG        *rag.Engine
	Billing    *BillingService
	WhiteLabel *whitelabel.WhiteLabelService
}

// whiteLabelCompletion sends only the platform's supported completion fields
// to the fixed white-label adapter, then projects the response before it can
// be persisted or returned. No provider response is treated as opaque data.
func (p *PlatformChatService) whiteLabelCompletion(ctx context.Context, validated []byte, body gateway.ChatCompletionRequest) (map[string]interface{}, error) {
	if p.deps.WhiteLabel == nil {
		return nil, errBadRequest("模型服务不可用")
	}
	payload, err := whiteLabelPayload(validated, body)
	if err != nil {
		return nil, err
	}
	resp, upstreamErr := p.deps.WhiteLabel.Chat(ctx, payload)
	if upstreamErr != nil {
		return nil, upstreamErr
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, whitelabel.ErrUpstreamUnavailable("chat body read failed")
	}
	completion, completionErr := p.deps.WhiteLabel.ProjectChatCompletion(raw, body.Model)
	if completionErr != nil {
		return nil, completionErr
	}
	projected, err := json.Marshal(completion)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(projected, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func whiteLabelPayload(validated []byte, body gateway.ChatCompletionRequest) ([]byte, error) {
	// Keep every already-validated OpenAI parameter (top_p, tools, response
	// format, etc.) while replacing only the model/messages that RAG prepared.
	fields := map[string]json.RawMessage{}
	if len(validated) > 0 {
		if err := json.Unmarshal(validated, &fields); err != nil {
			return nil, err
		}
	}
	model, _ := json.Marshal(body.Model)
	messages, _ := json.Marshal(body.Messages)
	fields["model"], fields["messages"] = model, messages
	if body.Stream {
		stream, _ := json.Marshal(true)
		fields["stream"] = stream
	}
	return json.Marshal(fields)
}

type PlatformChatService struct {
	deps PlatformDeps
}

func NewPlatformChatService(deps PlatformDeps) *PlatformChatService {
	return &PlatformChatService{deps: deps}
}

func (p *PlatformChatService) platformClient() (registry.ClientConfig, error) {
	client, ok := p.deps.Clients.GetBySecret(p.deps.Settings.PlatformClientSecret)
	if !ok {
		return registry.ClientConfig{}, errBadRequest(
			"Platform internal client not configured: PLATFORM_CLIENT_SECRET 与 clients.yaml 不一致",
		)
	}
	return client, nil
}

func (p *PlatformChatService) validateDatasets(db *gorm.DB, user *models.User, ids []int) ([]models.Dataset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []models.Dataset
	for _, id := range ids {
		var ds models.Dataset
		if err := db.First(&ds, id).Error; err != nil || ds.Status != models.DatasetActive {
			return nil, errBadRequest(fmt.Sprintf("数据集 %d 不可用", id))
		}
		if len(user.AllowedDatasets) > 0 && !containsInt(user.AllowedDatasets, id) {
			return nil, errForbidden(fmt.Sprintf("无权访问数据集 %d", id))
		}
		if len(ds.AccessPlans) > 0 && !containsStr(ds.AccessPlans, string(user.PlanType)) {
			if user.PlanType == models.PlanFree && !containsStr(ds.AccessPlans, "free") {
				return nil, errForbidden(fmt.Sprintf("当前套餐无法访问数据集 %s", ds.Name))
			}
		}
		out = append(out, ds)
	}
	return out, nil
}

type ChatParams struct {
	Model          string
	Messages       []map[string]interface{}
	ConversationID *int
	Temperature    *float64
	MaxTokens      *int
	ContextWindow  *int
	DatasetEnabled bool
	DatasetIDs     []int
	WhiteLabelBody []byte
}

// validateCompareModels applies the platform's deliberately small fan-out
// limit before billing, persistence, or any upstream request is attempted.
func validateCompareModels(modelsList []string) error {
	if len(modelsList) == 0 {
		return errBadRequest("至少选择一个模型")
	}
	if len(modelsList) > 3 {
		return errBadRequest("compare_model_limit_exceeded")
	}
	seen := make(map[string]struct{}, len(modelsList))
	for _, model := range modelsList {
		if strings.TrimSpace(model) == "" {
			return errBadRequest("invalid_request")
		}
		if _, exists := seen[model]; exists {
			return errBadRequest("invalid_request")
		}
		seen[model] = struct{}{}
	}
	return nil
}

func (p *PlatformChatService) Chat(ctx context.Context, db *gorm.DB, user *models.User, params ChatParams) (map[string]interface{}, error) {
	if err := p.deps.Billing.CheckAndConsumeCall(db, user, 1); err != nil {
		return nil, err
	}
	if len(user.AllowedModels) > 0 && !containsStr(user.AllowedModels, params.Model) {
		return nil, errForbidden("当前账号无权使用该模型")
	}

	datasets, err := p.validateDatasets(db, user, params.DatasetIDs)
	if err != nil {
		return nil, err
	}
	if params.DatasetEnabled && len(datasets) == 0 {
		return nil, errBadRequest("启用数据集时必须选择至少一个子数据集")
	}

	trimmed := TrimMessages(params.Messages, params.ContextWindow)
	query := lastUserQuery(trimmed)
	ragMsgs := trimmed
	datasetUsed := false
	if params.DatasetEnabled && len(datasets) > 0 {
		ids := make([]int, len(datasets))
		for i, d := range datasets {
			ids[i] = int(d.ID)
		}
		ragMsgs, datasetUsed = p.deps.RAG.BuildRAGMessages(trimmed, ids, query)
	}

	var conv *models.Conversation
	if params.ConversationID != nil {
		conv, err = GetConversation(db, user, *params.ConversationID, false)
	} else {
		conv, err = CreateConversation(db, user, "", params.Model, params.DatasetEnabled, params.DatasetIDs)
	}
	if err != nil {
		return nil, err
	}

	if last := lastUserMessage(trimmed); last != "" {
		_, _ = AddMessage(db, conv, "user", last, "", false, nil, 0)
	}

	body := gateway.ChatCompletionRequest{
		Model:       params.Model,
		Messages:    toGatewayMessages(ragMsgs),
		Temperature: params.Temperature,
		MaxTokens:   params.MaxTokens,
	}
	var data map[string]interface{}
	if p.deps.WhiteLabel != nil {
		data, err = p.whiteLabelCompletion(ctx, params.WhiteLabelBody, body)
	} else {
		client, clientErr := p.platformClient()
		if clientErr != nil {
			return nil, clientErr
		}
		data, err = p.deps.Gateway.Complete(ctx, client, body)
	}
	if err != nil {
		return nil, err
	}

	content, tokens := extractCompletion(data)
	attr := (*string)(nil)
	if datasetUsed {
		s := rag.DatasetAttribution
		attr = &s
	}
	_, _ = AddMessage(db, conv, "assistant", content, params.Model, datasetUsed, attr, tokens)
	user.TotalTokensUsed += tokens
	if datasetUsed {
		user.DatasetCalls++
	}
	_ = db.Save(user)
	_ = db.Create(&models.UsageRecord{UserID: user.ID, RecordType: "chat", Tokens: tokens, Model: &params.Model})

	return map[string]interface{}{
		"conversation_id":     conv.ID,
		"model":               params.Model,
		"content":             content,
		"dataset_used":        datasetUsed,
		"dataset_attribution": attr,
		"usage":               map[string]interface{}{"total_tokens": tokens},
	}, nil
}

func (p *PlatformChatService) Compare(ctx context.Context, db *gorm.DB, user *models.User, modelsList []string, params ChatParams) (map[string]interface{}, error) {
	if err := validateCompareModels(modelsList); err != nil {
		return nil, err
	}
	count := len(modelsList)
	var err error
	if err := p.deps.Billing.CheckAndConsumeCall(db, user, count); err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	var client registry.ClientConfig
	if p.deps.WhiteLabel == nil {
		client, err = p.platformClient()
		if err != nil {
			return nil, err
		}
	}

	for _, model := range modelsList {
		start := time.Now()
		body := gateway.ChatCompletionRequest{
			Model:       model,
			Messages:    toGatewayMessages(params.Messages),
			Temperature: params.Temperature,
			MaxTokens:   params.MaxTokens,
		}
		var data map[string]interface{}
		if p.deps.WhiteLabel != nil {
			data, err = p.whiteLabelCompletion(ctx, params.WhiteLabelBody, body)
		} else {
			data, err = p.deps.Gateway.Complete(ctx, client, body)
		}
		latency := time.Since(start).Seconds() * 1000
		item := map[string]interface{}{
			"model":      model,
			"latency_ms": latency,
			"tokens":     0,
		}
		if err != nil {
			item["error"] = err.Error()
		} else {
			content, tokens := extractCompletion(data)
			item["content"] = content
			item["tokens"] = tokens
		}
		results = append(results, item)
	}

	// Keep compare conversations readable from /api/v1/conversations/:id, just
	// like the original service: persist the submitted prompt and one aggregate
	// assistant message containing the replies for each model.
	var conv *models.Conversation
	trimmed := TrimMessages(params.Messages, params.ContextWindow)
	if params.ConversationID != nil {
		conv, err = GetConversation(db, user, *params.ConversationID, false)
	} else if len(trimmed) > 0 {
		primaryModel := modelsList[0]
		conv, err = CreateConversation(db, user, "", primaryModel, params.DatasetEnabled, params.DatasetIDs)
	}
	if err != nil {
		return nil, err
	}
	if conv != nil {
		if last := lastUserMessage(trimmed); last != "" {
			if _, err := AddMessage(db, conv, "user", last, "", false, nil, 0); err != nil {
				return nil, err
			}
			if conv.Title == "新对话" {
				conv.Title = truncateTitle(last)
				if err := db.Save(conv).Error; err != nil {
					return nil, err
				}
			}
		}

		replies := make(map[string]string, len(results))
		totalTokens := 0
		for _, result := range results {
			model := fmt.Sprint(result["model"])
			if message, ok := result["error"].(string); ok && message != "" {
				replies[model] = "[错误] " + message
			} else {
				replies[model] = fmt.Sprint(result["content"])
			}
			if tokens, ok := result["tokens"].(int); ok {
				totalTokens += tokens
			}
		}
		payload, err := json.Marshal(replies)
		if err != nil {
			return nil, err
		}
		primaryModel := modelsList[0]
		if _, err := AddMessage(db, conv, "assistant", "__MULTI_MODEL__"+string(payload), primaryModel, false, nil, totalTokens); err != nil {
			return nil, err
		}
		user.TotalTokensUsed += totalTokens
		if err := db.Save(user).Error; err != nil {
			return nil, err
		}
		for _, result := range results {
			model := fmt.Sprint(result["model"])
			tokens, _ := result["tokens"].(int)
			if err := db.Create(&models.UsageRecord{UserID: user.ID, RecordType: "chat", Tokens: tokens, Model: &model}).Error; err != nil {
				return nil, err
			}
		}
	}

	out := map[string]interface{}{
		"results":         results,
		"conversation_id": conversationID(conv),
	}
	if params.DatasetEnabled {
		out["dataset_attribution"] = rag.DatasetAttribution
	}
	return out, nil
}

func (p *PlatformChatService) Stream(ctx context.Context, db *gorm.DB, user *models.User, params ChatParams, write func([]byte) error) error {
	if err := p.deps.Billing.CheckAndConsumeCall(db, user, 1); err != nil {
		return err
	}
	if len(user.AllowedModels) > 0 && !containsStr(user.AllowedModels, params.Model) {
		return errForbidden("当前账号无权使用该模型")
	}

	datasets, err := p.validateDatasets(db, user, params.DatasetIDs)
	if err != nil {
		return err
	}
	if params.DatasetEnabled && len(datasets) == 0 {
		return errBadRequest("启用数据集时必须选择至少一个子数据集")
	}
	trimmed := TrimMessages(params.Messages, params.ContextWindow)
	query := lastUserQuery(trimmed)
	ragMsgs := trimmed
	datasetUsed := false
	if params.DatasetEnabled && len(datasets) > 0 {
		ids := make([]int, len(datasets))
		for i, d := range datasets {
			ids[i] = int(d.ID)
		}
		ragMsgs, datasetUsed = p.deps.RAG.BuildRAGMessages(trimmed, ids, query)
	}

	var conv *models.Conversation
	if params.ConversationID != nil {
		conv, err = GetConversation(db, user, *params.ConversationID, false)
	} else {
		conv, err = CreateConversation(db, user, "", params.Model, params.DatasetEnabled, params.DatasetIDs)
	}
	if err != nil {
		return err
	}
	if last := lastUserMessage(trimmed); last != "" {
		if _, err := AddMessage(db, conv, "user", last, "", false, nil, 0); err != nil {
			return err
		}
		if conv.Title == "新对话" {
			conv.Title = truncateTitle(last)
			if err := db.Save(conv).Error; err != nil {
				return err
			}
		}
	}

	attr := (*string)(nil)
	if datasetUsed {
		s := rag.DatasetAttribution
		attr = &s
	}
	meta := map[string]interface{}{
		"type":                "meta",
		"conversation_id":     conv.ID,
		"dataset_used":        datasetUsed,
		"dataset_attribution": attr,
	}
	metaBytes, _ := json.Marshal(meta)
	metaFrame := []byte(fmt.Sprintf("data: %s\n\n", metaBytes))
	metaSent := false
	emitFrame := func(frame []byte) error {
		if !metaSent {
			if err := write(metaFrame); err != nil {
				return err
			}
			metaSent = true
		}
		return write(frame)
	}

	body := gateway.ChatCompletionRequest{
		Model:       params.Model,
		Messages:    toGatewayMessages(ragMsgs),
		Temperature: params.Temperature,
		MaxTokens:   params.MaxTokens,
		Stream:      true,
	}
	if p.deps.WhiteLabel != nil {
		payload, marshalErr := whiteLabelPayload(params.WhiteLabelBody, body)
		if marshalErr != nil {
			return marshalErr
		}
		resp, upstreamErr := p.deps.WhiteLabel.Chat(ctx, payload)
		if upstreamErr != nil {
			return upstreamErr
		}
		defer resp.Body.Close()
		var content strings.Builder
		streamErr := p.deps.WhiteLabel.ProjectChatCompletionSSE(resp.Body, params.Model, func(frame []byte) error {
			content.WriteString(parseSSEDelta(frame))
			return emitFrame(frame)
		})
		if streamErr != nil {
			return streamErr
		}
		return p.finishPlatformStream(db, user, conv, params, datasetUsed, attr, content.String(), write)
	}
	client, err := p.platformClient()
	if err != nil {
		return err
	}
	resp, err := p.deps.Gateway.Stream(ctx, client, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	var content strings.Builder
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if err := emitFrame(chunk); err != nil {
				return err
			}
			content.WriteString(parseSSEDelta(chunk))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}
	}

	return p.finishPlatformStream(db, user, conv, params, datasetUsed, attr, content.String(), write)
}

// finishPlatformStream keeps the pre-existing conversation and usage
// semantics after either legacy or white-label SSE delivers a final response.
func (p *PlatformChatService) finishPlatformStream(db *gorm.DB, user *models.User, conv *models.Conversation, params ChatParams, datasetUsed bool, attr *string, content string, write func([]byte) error) error {
	tokens := len(content) / 2
	if tokens < 1 && content != "" {
		tokens = 1
	}
	_, _ = AddMessage(db, conv, "assistant", content, params.Model, datasetUsed, attr, tokens)
	user.TotalTokensUsed += tokens
	if datasetUsed {
		user.DatasetCalls++
	}
	_ = db.Save(user)
	done, _ := json.Marshal(map[string]interface{}{"type": "done", "tokens": tokens, "total_tokens_used": user.TotalTokensUsed})
	return write([]byte(fmt.Sprintf("data: %s\n\n", done)))
}

// CompareStream emits model_chunk SSE events while each selected model responds,
// then stores the same conversation history as non-streaming compare requests.
func (p *PlatformChatService) CompareStream(ctx context.Context, db *gorm.DB, user *models.User, modelsList []string, params ChatParams, requestID string, write func([]byte) error) error {
	if err := validateCompareModels(modelsList); err != nil {
		return err
	}
	if err := p.deps.Billing.CheckAndConsumeCall(db, user, len(modelsList)); err != nil {
		return err
	}

	datasets, err := p.validateDatasets(db, user, params.DatasetIDs)
	if err != nil {
		return err
	}
	if params.DatasetEnabled && len(datasets) == 0 {
		return errBadRequest("启用数据集时必须选择至少一个子数据集")
	}
	trimmed := TrimMessages(params.Messages, params.ContextWindow)
	ragMsgs := trimmed
	datasetUsed := false
	if params.DatasetEnabled {
		ids := make([]int, len(datasets))
		for i, dataset := range datasets {
			ids[i] = int(dataset.ID)
		}
		ragMsgs, datasetUsed = p.deps.RAG.BuildRAGMessages(trimmed, ids, lastUserQuery(trimmed))
	}
	if p.deps.WhiteLabel != nil {
		return p.compareWhiteLabelStreams(ctx, db, user, modelsList, params, trimmed, ragMsgs, datasetUsed, requestID, write)
	}

	client, err := p.platformClient()
	if err != nil {
		return err
	}
	results := make([]map[string]interface{}, 0, len(modelsList))
	for _, model := range modelsList {
		result := p.streamCompareModel(ctx, user, client, model, ragMsgs, params, write)
		results = append(results, result)
	}

	conv, err := p.persistCompareExchange(db, user, modelsList, params, trimmed, results, datasetUsed)
	if err != nil {
		return err
	}
	done := map[string]interface{}{
		"type":                "done",
		"conversation_id":     conversationID(conv),
		"dataset_used":        datasetUsed,
		"dataset_attribution": nil,
		"tokens":              totalResultTokens(results),
		"total_tokens_used":   user.TotalTokensUsed,
	}
	if datasetUsed {
		done["dataset_attribution"] = rag.DatasetAttribution
	}
	return writeSSE(write, done)
}

// compareWhiteLabelStreams multiplexes independently projected model streams.
// A single upstream failure produces a model_error event but never cancels the
// other selected models; only the final coordinator writes [DONE].
func (p *PlatformChatService) compareWhiteLabelStreams(ctx context.Context, db *gorm.DB, user *models.User, modelsList []string, params ChatParams, trimmed, ragMsgs []map[string]interface{}, datasetUsed bool, requestID string, write func([]byte) error) error {
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]map[string]interface{}, len(modelsList))
	var writers sync.Mutex
	var workers sync.WaitGroup
	var streamWriteErr error
	hasOutput := false
	pendingFailures := make([]string, 0, len(modelsList))
	emitFailure := func(model string) {
		writers.Lock()
		defer writers.Unlock()
		if streamWriteErr != nil {
			return
		}
		if !hasOutput {
			pendingFailures = append(pendingFailures, model)
			return
		}
		if err := writeCompareError(write, model, requestID); err != nil {
			streamWriteErr = err
			cancel()
		}
	}
	emitChunk := func(model string, frame []byte) error {
		payload := bytes.TrimSpace(bytes.TrimPrefix(frame, []byte("data:")))
		if bytes.Equal(payload, []byte("[DONE]")) {
			return nil
		}
		writers.Lock()
		defer writers.Unlock()
		if streamWriteErr != nil {
			return streamWriteErr
		}
		if !hasOutput {
			hasOutput = true
			for _, failedModel := range pendingFailures {
				if err := writeCompareError(write, failedModel, requestID); err != nil {
					streamWriteErr = err
					cancel()
					return err
				}
			}
			pendingFailures = nil
		}
		if err := writeCompareEvent(write, "chunk", map[string]interface{}{"model": model, "chunk": json.RawMessage(payload)}); err != nil {
			streamWriteErr = err
			cancel()
			return err
		}
		return nil
	}
	for index, model := range modelsList {
		index, model := index, model
		workers.Add(1)
		go func() {
			defer workers.Done()
			result := map[string]interface{}{"model": model, "tokens": 0}
			body := gateway.ChatCompletionRequest{Model: model, Messages: toGatewayMessages(ragMsgs), Temperature: params.Temperature, MaxTokens: params.MaxTokens, Stream: true}
			payload, marshalErr := whiteLabelPayload(params.WhiteLabelBody, body)
			if marshalErr != nil {
				result["error"] = "upstream unavailable"
				results[index] = result
				return
			}
			resp, upstreamErr := p.deps.WhiteLabel.Chat(streamCtx, payload)
			if upstreamErr != nil {
				result["error"] = "upstream unavailable"
				emitFailure(model)
				results[index] = result
				return
			}
			defer resp.Body.Close()
			var content strings.Builder
			streamErr := p.deps.WhiteLabel.ProjectChatCompletionSSE(resp.Body, model, func(frame []byte) error {
				content.WriteString(parseSSEDelta(frame))
				return emitChunk(model, frame)
			})
			if streamErr != nil {
				result["error"] = "upstream unavailable"
				emitFailure(model)
				results[index] = result
				return
			}
			result["content"] = content.String()
			if content.Len() > 0 {
				result["tokens"] = max(1, len([]rune(content.String()))/2)
			}
			writers.Lock()
			if streamWriteErr != nil {
				writers.Unlock()
				results[index] = result
				return
			}
			if !hasOutput {
				hasOutput = true
				for _, failedModel := range pendingFailures {
					if err := writeCompareError(write, failedModel, requestID); err != nil {
						streamWriteErr = err
						cancel()
						writers.Unlock()
						results[index] = result
						return
					}
				}
				pendingFailures = nil
			}
			if err := writeCompareEvent(write, "model_done", map[string]interface{}{"model": model}); err != nil {
				streamWriteErr = err
				cancel()
			}
			writers.Unlock()
			results[index] = result
		}()
	}
	workers.Wait()
	writers.Lock()
	noOutput := !hasOutput
	writeErr := streamWriteErr
	writers.Unlock()
	if writeErr != nil {
		return writeErr
	}
	if noOutput {
		return whitelabel.ErrUpstreamUnavailable("all compare streams failed before first frame")
	}
	_, persistErr := p.persistCompareExchange(db, user, modelsList, params, trimmed, results, datasetUsed)
	if persistErr != nil {
		return persistErr
	}
	return write([]byte("data: [DONE]\n\n"))
}

func writeCompareChunk(write func([]byte) error, model string, frame []byte) error {
	payload := bytes.TrimSpace(bytes.TrimPrefix(frame, []byte("data:")))
	if bytes.Equal(payload, []byte("[DONE]")) {
		return nil
	}
	return writeCompareEvent(write, "chunk", map[string]interface{}{"model": model, "chunk": json.RawMessage(payload)})
}

func writeCompareError(write func([]byte) error, model, requestID string) error {
	public := whitelabel.PublicError(whitelabel.ErrUpstreamUnavailable("compare stream failed"), requestID)
	return writeCompareEvent(write, "model_error", map[string]interface{}{"model": model, "error": public.Error})
}

func writeCompareEvent(write func([]byte) error, name string, payload interface{}) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return write([]byte("event: " + name + "\ndata: " + string(encoded) + "\n\n"))
}

func (p *PlatformChatService) streamCompareModel(ctx context.Context, user *models.User, client registry.ClientConfig, model string, messages []map[string]interface{}, params ChatParams, write func([]byte) error) map[string]interface{} {
	started := time.Now()
	result := map[string]interface{}{"model": model, "tokens": 0}
	if len(user.AllowedModels) > 0 && !containsStr(user.AllowedModels, model) {
		message := "当前账号无权使用该模型"
		result["error"] = message
		_ = writeSSE(write, map[string]interface{}{"type": "model_chunk", "model": model, "delta": "[错误] " + message})
		result["latency_ms"] = time.Since(started).Seconds() * 1000
		return result
	}
	body := gateway.ChatCompletionRequest{Model: model, Messages: toGatewayMessages(messages), Temperature: params.Temperature, MaxTokens: params.MaxTokens, Stream: true}
	resp, err := p.deps.Gateway.Stream(ctx, client, body)
	if err != nil {
		result["error"] = err.Error()
		_ = writeSSE(write, map[string]interface{}{"type": "model_chunk", "model": model, "delta": "[错误] " + err.Error()})
		result["latency_ms"] = time.Since(started).Seconds() * 1000
		return result
	}
	defer resp.Body.Close()

	buf := make([]byte, 4096)
	var content strings.Builder
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			delta := parseSSEDelta(buf[:n])
			if delta != "" {
				content.WriteString(delta)
				if err := writeSSE(write, map[string]interface{}{"type": "model_chunk", "model": model, "delta": delta}); err != nil {
					result["error"] = err.Error()
					break
				}
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			result["error"] = readErr.Error()
			break
		}
	}
	result["latency_ms"] = time.Since(started).Seconds() * 1000
	if err, ok := result["error"].(string); ok && err != "" {
		return result
	}
	result["content"] = content.String()
	if content.Len() > 0 {
		result["tokens"] = max(1, len([]rune(content.String()))/2)
	}
	return result
}

func (p *PlatformChatService) persistCompareExchange(db *gorm.DB, user *models.User, modelsList []string, params ChatParams, trimmed []map[string]interface{}, results []map[string]interface{}, datasetUsed bool) (*models.Conversation, error) {
	var conv *models.Conversation
	var err error
	if params.ConversationID != nil {
		conv, err = GetConversation(db, user, *params.ConversationID, false)
	} else if len(trimmed) > 0 {
		conv, err = CreateConversation(db, user, "", modelsList[0], params.DatasetEnabled, params.DatasetIDs)
	}
	if err != nil || conv == nil {
		return conv, err
	}
	if last := lastUserMessage(trimmed); last != "" {
		if _, err := AddMessage(db, conv, "user", last, "", false, nil, 0); err != nil {
			return nil, err
		}
		if conv.Title == "新对话" {
			conv.Title = truncateTitle(last)
			if err := db.Save(conv).Error; err != nil {
				return nil, err
			}
		}
	}
	replies := make(map[string]string, len(results))
	for _, result := range results {
		model := fmt.Sprint(result["model"])
		if message, ok := result["error"].(string); ok && message != "" {
			replies[model] = "[错误] " + message
		} else {
			replies[model] = fmt.Sprint(result["content"])
		}
	}
	payload, err := json.Marshal(replies)
	if err != nil {
		return nil, err
	}
	tokens := totalResultTokens(results)
	attr := (*string)(nil)
	if datasetUsed {
		value := rag.DatasetAttribution
		attr = &value
	}
	if _, err := AddMessage(db, conv, "assistant", "__MULTI_MODEL__"+string(payload), modelsList[0], datasetUsed, attr, tokens); err != nil {
		return nil, err
	}
	user.TotalTokensUsed += tokens
	if datasetUsed {
		user.DatasetCalls++
	}
	if err := db.Save(user).Error; err != nil {
		return nil, err
	}
	for _, result := range results {
		model := fmt.Sprint(result["model"])
		tokens, _ := result["tokens"].(int)
		if err := db.Create(&models.UsageRecord{UserID: user.ID, RecordType: "chat", Tokens: tokens, Model: &model}).Error; err != nil {
			return nil, err
		}
	}
	return conv, nil
}

func totalResultTokens(results []map[string]interface{}) int {
	total := 0
	for _, result := range results {
		if tokens, ok := result["tokens"].(int); ok {
			total += tokens
		}
	}
	return total
}

func writeSSE(write func([]byte) error, event map[string]interface{}) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return write([]byte(fmt.Sprintf("data: %s\n\n", payload)))
}

func toGatewayMessages(msgs []map[string]interface{}) []gateway.ChatMessage {
	out := make([]gateway.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, gateway.ChatMessage{
			Role:    fmt.Sprint(m["role"]),
			Content: m["content"],
		})
	}
	return out
}

func extractCompletion(data map[string]interface{}) (string, int) {
	content := ""
	tokens := 0
	if choices, ok := data["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				content, _ = msg["content"].(string)
			}
		}
	}
	if usage, ok := data["usage"].(map[string]interface{}); ok {
		if t, ok := usage["total_tokens"].(float64); ok {
			tokens = int(t)
		}
	}
	return content, tokens
}

func lastUserQuery(msgs []map[string]interface{}) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if fmt.Sprint(msgs[i]["role"]) == "user" {
			return fmt.Sprint(msgs[i]["content"])
		}
	}
	return ""
}

func lastUserMessage(msgs []map[string]interface{}) string { return lastUserQuery(msgs) }

func truncateTitle(content string) string {
	title := []rune(strings.TrimSpace(content))
	if len(title) > 24 {
		title = title[:24]
	}
	if len(title) == 0 {
		return "新对话"
	}
	return string(title)
}

func conversationID(conv *models.Conversation) interface{} {
	if conv == nil {
		return nil
	}
	return conv.ID
}

func parseSSEDelta(chunk []byte) string {
	var parts []string
	for _, line := range strings.Split(string(chunk), "\n") {
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var data map[string]interface{}
		if json.Unmarshal([]byte(payload), &data) != nil {
			continue
		}
		if choices, ok := data["choices"].([]interface{}); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]interface{}); ok {
				if delta, ok := choice["delta"].(map[string]interface{}); ok {
					if c, ok := delta["content"].(string); ok {
						parts = append(parts, c)
					}
				}
			}
		}
	}
	return strings.Join(parts, "")
}

func containsStr(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func containsInt(list []int, v int) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
