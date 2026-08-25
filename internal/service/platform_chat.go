package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/whitelabel"
	"gorm.io/gorm"
)

type PlatformDeps struct {
	Settings   *config.Settings
	DB         *gorm.DB
	Billing    *BillingService
	WhiteLabel *whitelabel.WhiteLabelService
}

// whiteLabelCompletion sends only the platform's supported completion fields
// to the fixed white-label adapter, then projects the response before it can
// be persisted or returned. No provider response is treated as opaque data.
func (p *PlatformChatService) whiteLabelCompletion(ctx context.Context, validated []byte, body whitelabel.ChatCompletionRequest) (map[string]interface{}, error) {
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

func whiteLabelPayload(validated []byte, body whitelabel.ChatCompletionRequest) ([]byte, error) {
	// Keep every already-validated OpenAI parameter (top_p, tools, response
	// format, etc.) while replacing only the model/messages that the platform prepared.
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

type ChatParams struct {
	Model            string
	Messages         []map[string]interface{}
	ConversationGUID *string
	Temperature      *float64
	MaxTokens        *int
	ContextWindow    *int
	WhiteLabelBody   []byte
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

	trimmed := TrimMessages(params.Messages, params.ContextWindow)

	var (
		conv *models.Conversation
		err  error
	)
	if params.ConversationGUID != nil {
		conv, err = conversationByGUID(db, user, *params.ConversationGUID)
	} else {
		conv, err = CreateConversation(db, user, "", params.Model)
	}
	if err != nil {
		return nil, err
	}

	if last := lastUserMessage(trimmed); last != "" {
		if _, err := AddMessage(db, conv, "user", last, "", 0); err != nil {
			return nil, err
		}
	}

	body := whitelabel.ChatCompletionRequest{
		Model:       params.Model,
		Messages:    toGatewayMessages(trimmed),
		Temperature: params.Temperature,
		MaxTokens:   params.MaxTokens,
	}
	data, err := p.whiteLabelCompletion(ctx, params.WhiteLabelBody, body)
	if err != nil {
		return nil, err
	}

	content, tokens := extractCompletion(data)
	if _, err := AddMessage(db, conv, "assistant", content, params.Model, tokens); err != nil {
		return nil, err
	}
	user.TotalTokensUsed += int64(tokens)
	stampUpdate(&user.AuditFields, user.ID)
	if err := db.Save(user).Error; err != nil {
		return nil, err
	}
	if err := db.Create(&models.UsageRecord{UserID: user.ID, RecordType: models.UsageRecordChat, Tokens: tokens, Model: &params.Model, AuditFields: auditFields(&user.ID)}).Error; err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"conversation_guid": fmt.Sprint(conv.Guid),
		"model":             params.Model,
		"content":           content,
		"usage":             map[string]interface{}{"total_tokens": tokens},
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
	for _, model := range modelsList {
		start := time.Now()
		body := whitelabel.ChatCompletionRequest{
			Model:       model,
			Messages:    toGatewayMessages(params.Messages),
			Temperature: params.Temperature,
			MaxTokens:   params.MaxTokens,
		}
		data, err := p.whiteLabelCompletion(ctx, params.WhiteLabelBody, body)
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
	if params.ConversationGUID != nil {
		conv, err = conversationByGUID(db, user, *params.ConversationGUID)
	} else if len(trimmed) > 0 {
		primaryModel := modelsList[0]
		conv, err = CreateConversation(db, user, "", primaryModel)
	}
	if err != nil {
		return nil, err
	}
	if conv != nil {
		if last := lastUserMessage(trimmed); last != "" {
			if _, err := AddMessage(db, conv, "user", last, "", 0); err != nil {
				return nil, err
			}
			if conv.Title == "新对话" {
				conv.Title = truncateTitle(last)
				stampUpdate(&conv.AuditFields, conv.UserID)
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
		if _, err := AddMessage(db, conv, "assistant", "__MULTI_MODEL__"+string(payload), primaryModel, totalTokens); err != nil {
			return nil, err
		}
		user.TotalTokensUsed += int64(totalTokens)
		stampUpdate(&user.AuditFields, user.ID)
		if err := db.Save(user).Error; err != nil {
			return nil, err
		}
		for _, result := range results {
			model := fmt.Sprint(result["model"])
			tokens, _ := result["tokens"].(int)
			if err := db.Create(&models.UsageRecord{UserID: user.ID, RecordType: models.UsageRecordChat, Tokens: tokens, Model: &model, AuditFields: auditFields(&user.ID)}).Error; err != nil {
				return nil, err
			}
		}
	}

	out := map[string]interface{}{
		"results":           results,
		"conversation_guid": conversationGUID(conv),
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

	trimmed := TrimMessages(params.Messages, params.ContextWindow)

	var (
		conv *models.Conversation
		err  error
	)
	if params.ConversationGUID != nil {
		conv, err = conversationByGUID(db, user, *params.ConversationGUID)
	} else {
		conv, err = CreateConversation(db, user, "", params.Model)
	}
	if err != nil {
		return err
	}
	if last := lastUserMessage(trimmed); last != "" {
		if _, err := AddMessage(db, conv, "user", last, "", 0); err != nil {
			return err
		}
		if conv.Title == "新对话" {
			conv.Title = truncateTitle(last)
			stampUpdate(&conv.AuditFields, conv.UserID)
			if err := db.Save(conv).Error; err != nil {
				return err
			}
		}
	}

	meta := map[string]interface{}{
		"type":              "meta",
		"conversation_guid": fmt.Sprint(conv.Guid),
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

	body := whitelabel.ChatCompletionRequest{
		Model:       params.Model,
		Messages:    toGatewayMessages(trimmed),
		Temperature: params.Temperature,
		MaxTokens:   params.MaxTokens,
		Stream:      true,
	}
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
	return p.finishPlatformStream(db, user, conv, params, content.String(), write)
}

// finishPlatformStream keeps the pre-existing conversation and usage
// semantics after either legacy or white-label SSE delivers a final response.
func (p *PlatformChatService) finishPlatformStream(db *gorm.DB, user *models.User, conv *models.Conversation, params ChatParams, content string, write func([]byte) error) error {
	tokens := len(content) / 2
	if tokens < 1 && content != "" {
		tokens = 1
	}
	if _, err := AddMessage(db, conv, "assistant", content, params.Model, tokens); err != nil {
		return err
	}
	user.TotalTokensUsed += int64(tokens)
	stampUpdate(&user.AuditFields, user.ID)
	if err := db.Save(user).Error; err != nil {
		return err
	}
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

	trimmed := TrimMessages(params.Messages, params.ContextWindow)
	return p.compareWhiteLabelStreams(ctx, db, user, modelsList, params, trimmed, requestID, write)
}

// compareWhiteLabelStreams multiplexes independently projected model streams.
// A single upstream failure produces a model_error event but never cancels the
// other selected models; only the final coordinator writes [DONE].
func (p *PlatformChatService) compareWhiteLabelStreams(ctx context.Context, db *gorm.DB, user *models.User, modelsList []string, params ChatParams, trimmed []map[string]interface{}, requestID string, write func([]byte) error) error {
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
			body := whitelabel.ChatCompletionRequest{Model: model, Messages: toGatewayMessages(trimmed), Temperature: params.Temperature, MaxTokens: params.MaxTokens, Stream: true}
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
	_, persistErr := p.persistCompareExchange(db, user, modelsList, params, trimmed, results)
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

func (p *PlatformChatService) persistCompareExchange(db *gorm.DB, user *models.User, modelsList []string, params ChatParams, trimmed []map[string]interface{}, results []map[string]interface{}) (*models.Conversation, error) {
	var conv *models.Conversation
	var err error
	if params.ConversationGUID != nil {
		conv, err = conversationByGUID(db, user, *params.ConversationGUID)
	} else if len(trimmed) > 0 {
		conv, err = CreateConversation(db, user, "", modelsList[0])
	}
	if err != nil || conv == nil {
		return conv, err
	}
	if last := lastUserMessage(trimmed); last != "" {
		if _, err := AddMessage(db, conv, "user", last, "", 0); err != nil {
			return nil, err
		}
		if conv.Title == "新对话" {
			conv.Title = truncateTitle(last)
			stampUpdate(&conv.AuditFields, conv.UserID)
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
	if _, err := AddMessage(db, conv, "assistant", "__MULTI_MODEL__"+string(payload), modelsList[0], tokens); err != nil {
		return nil, err
	}
	user.TotalTokensUsed += int64(tokens)
	stampUpdate(&user.AuditFields, user.ID)
	if err := db.Save(user).Error; err != nil {
		return nil, err
	}
	for _, result := range results {
		model := fmt.Sprint(result["model"])
		tokens, _ := result["tokens"].(int)
		if err := db.Create(&models.UsageRecord{UserID: user.ID, RecordType: models.UsageRecordChat, Tokens: tokens, Model: &model, AuditFields: auditFields(&user.ID)}).Error; err != nil {
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

func toGatewayMessages(msgs []map[string]interface{}) []whitelabel.ChatMessage {
	out := make([]whitelabel.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, whitelabel.ChatMessage{
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

func conversationGUID(conv *models.Conversation) interface{} {
	if conv == nil {
		return nil
	}
	return fmt.Sprint(conv.Guid)
}

func parseConversationGUID(value string) (int64, error) {
	guid, err := strconv.ParseInt(value, 10, 64)
	if err != nil || guid < 1 {
		return 0, errBadRequest("无效对话标识")
	}
	return guid, nil
}

func conversationByGUID(db *gorm.DB, user *models.User, value string) (*models.Conversation, error) {
	guid, err := parseConversationGUID(value)
	if err != nil {
		return nil, err
	}
	return GetConversation(db, user, guid, false)
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
