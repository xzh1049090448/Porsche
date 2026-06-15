package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/gateway"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/rag"
	"github.com/porsche/ai-gateway-go/internal/registry"
	"gorm.io/gorm"
)

type PlatformDeps struct {
	Settings *config.Settings
	DB       *gorm.DB
	Models   *registry.ModelRegistry
	Clients  *registry.ClientRegistry
	Gateway  *gateway.Service
	RAG      *rag.Engine
	Billing  *BillingService
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
	ConversationID *uint
	Temperature    *float64
	MaxTokens      *int
	ContextWindow  *int
	DatasetEnabled bool
	DatasetIDs     []int
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

	client, err := p.platformClient()
	if err != nil {
		return nil, err
	}

	body := gateway.ChatCompletionRequest{
		Model:       params.Model,
		Messages:    toGatewayMessages(ragMsgs),
		Temperature: params.Temperature,
		MaxTokens:   params.MaxTokens,
	}
	data, err := p.deps.Gateway.Complete(ctx, client, body)
	if err != nil {
		return nil, errBadRequest(err.Error())
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
	count := len(modelsList)
	if err := p.deps.Billing.CheckAndConsumeCall(db, user, count); err != nil {
		return nil, err
	}

	var results []map[string]interface{}
	client, err := p.platformClient()
	if err != nil {
		return nil, err
	}

	for _, model := range modelsList {
		start := time.Now()
		body := gateway.ChatCompletionRequest{
			Model:       model,
			Messages:    toGatewayMessages(params.Messages),
			Temperature: params.Temperature,
			MaxTokens:   params.MaxTokens,
		}
		data, err := p.deps.Gateway.Complete(ctx, client, body)
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

	out := map[string]interface{}{
		"results":         results,
		"conversation_id": params.ConversationID,
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
	if err := write([]byte(fmt.Sprintf("data: %s\n\n", metaBytes))); err != nil {
		return err
	}

	client, err := p.platformClient()
	if err != nil {
		return err
	}
	body := gateway.ChatCompletionRequest{
		Model:       params.Model,
		Messages:    toGatewayMessages(ragMsgs),
		Temperature: params.Temperature,
		MaxTokens:   params.MaxTokens,
		Stream:      true,
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
			_ = write(chunk)
			content.WriteString(parseSSEDelta(chunk))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			break
		}
	}

	tokens := len(content.String()) / 2
	if tokens < 1 && content.Len() > 0 {
		tokens = 1
	}
	_, _ = AddMessage(db, conv, "assistant", content.String(), params.Model, datasetUsed, attr, tokens)
	user.TotalTokensUsed += tokens
	if datasetUsed {
		user.DatasetCalls++
	}
	_ = db.Save(user)
	done, _ := json.Marshal(map[string]interface{}{
		"type":              "done",
		"tokens":            tokens,
		"total_tokens_used": user.TotalTokensUsed,
	})
	return write([]byte(fmt.Sprintf("data: %s\n\n", done)))
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
