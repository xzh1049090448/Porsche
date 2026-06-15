package dto

import (
	"time"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func FormatTime(t time.Time) string {
	if t.IsZero() {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	if t.Location() == time.UTC {
		return t.Format(time.RFC3339Nano)
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func UserProfile(user *models.User) map[string]interface{} {
	return map[string]interface{}{
		"id":                user.ID,
		"phone":             user.Phone,
		"nickname":          user.Nickname,
		"is_verified":       user.IsVerified,
		"plan_type":         string(user.PlanType),
		"total_tokens_used": user.TotalTokensUsed,
		"dataset_calls":     user.DatasetCalls,
		"daily_calls_used":  user.DailyCallsUsed,
		"daily_call_limit":  user.DailyCallLimit,
		"created_at":        FormatTime(user.CreatedAt),
	}
}

func Message(msg models.Message) map[string]interface{} {
	return map[string]interface{}{
		"id":                  msg.ID,
		"role":                msg.Role,
		"content":             msg.Content,
		"model":               msg.Model,
		"dataset_used":        msg.DatasetUsed,
		"dataset_attribution": msg.DatasetAttribution,
		"tokens":              msg.Tokens,
		"created_at":          FormatTime(msg.CreatedAt),
	}
}

func Conversation(conv *models.Conversation, includeMessages bool) map[string]interface{} {
	out := map[string]interface{}{
		"id":              conv.ID,
		"title":           conv.Title,
		"model":           conv.Model,
		"dataset_enabled": conv.DatasetEnabled,
		"dataset_ids":     conv.DatasetIDs,
		"created_at":      FormatTime(conv.CreatedAt),
		"updated_at":      FormatTime(conv.UpdatedAt),
	}
	if includeMessages {
		msgs := make([]map[string]interface{}, 0, len(conv.Messages))
		for _, m := range conv.Messages {
			msgs = append(msgs, Message(m))
		}
		out["messages"] = msgs
	}
	return out
}

func Dataset(ds *models.Dataset) map[string]interface{} {
	return map[string]interface{}{
		"id":              ds.ID,
		"name":            ds.Name,
		"slug":            ds.Slug,
		"category":        string(ds.Category),
		"description":     ds.Description,
		"status":          string(ds.Status),
		"current_version": ds.CurrentVersion,
		"token_count":     ds.TokenCount,
		"vector_status":   string(ds.VectorStatus),
		"access_plans":    ds.AccessPlans,
		"created_at":      FormatTime(ds.CreatedAt),
	}
}

func Order(o *models.Order) map[string]interface{} {
	out := map[string]interface{}{
		"id":                o.ID,
		"order_no":          o.OrderNo,
		"plan_type":         string(o.PlanType),
		"amount":            o.Amount,
		"status":            string(o.Status),
		"invoice_requested": o.InvoiceRequested,
		"created_at":        FormatTime(o.CreatedAt),
		"paid_at":           nil,
	}
	if o.PaidAt != nil {
		out["paid_at"] = FormatTime(*o.PaidAt)
	}
	return out
}

func AdminUser(user *models.User) map[string]interface{} {
	return map[string]interface{}{
		"id":                user.ID,
		"phone":             user.Phone,
		"nickname":          user.Nickname,
		"plan_type":         string(user.PlanType),
		"status":            string(user.Status),
		"is_verified":       user.IsVerified,
		"total_tokens_used": user.TotalTokensUsed,
		"dataset_calls":     user.DatasetCalls,
		"created_at":        FormatTime(user.CreatedAt),
	}
}

func AuditLog(log *models.AuditLog) map[string]interface{} {
	return map[string]interface{}{
		"id":         log.ID,
		"user_id":    log.UserID,
		"action":     log.Action,
		"resource":   log.Resource,
		"detail":     log.Detail,
		"ip":         log.IP,
		"created_at": FormatTime(log.CreatedAt),
	}
}

func ModelHealth(h *models.ModelHealth) map[string]interface{} {
	out := map[string]interface{}{
		"model_name":     h.ModelName,
		"provider":       h.Provider,
		"is_available":   h.IsAvailable,
		"avg_latency_ms": h.AvgLatencyMs,
		"error_rate":     h.ErrorRate,
		"last_checked_at": nil,
	}
	if h.LastCheckedAt != nil {
		out["last_checked_at"] = FormatTime(*h.LastCheckedAt)
	}
	return out
}
