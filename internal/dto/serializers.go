package dto

import (
	"strconv"
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

// FormatMillis renders persisted UTC Unix milliseconds at the API boundary.
func FormatMillis(millis int64) string { return time.UnixMilli(millis).UTC().Format(time.RFC3339Nano) }

func UserProfile(user *models.User) map[string]interface{} {
	return map[string]interface{}{
		"guid":              strconv.FormatInt(user.Guid, 10),
		"nickname":          user.Nickname,
		"is_verified":       user.IsVerified,
		"plan_type":         user.PlanType.String(),
		"total_tokens_used": user.TotalTokensUsed,
		"daily_calls_used":  user.DailyCallsUsed,
		"daily_call_limit":  user.DailyCallLimit,
		"created_at":        FormatMillis(user.CreatedAt),
	}
}

func Message(msg models.Message) map[string]interface{} {
	return map[string]interface{}{
		"guid":       strconv.FormatInt(msg.Guid, 10),
		"role":       msg.Role.String(),
		"content":    msg.Content,
		"model":      msg.Model,
		"tokens":     msg.Tokens,
		"created_at": FormatMillis(msg.CreatedAt),
	}
}

func Conversation(conv *models.Conversation, includeMessages bool) map[string]interface{} {
	out := map[string]interface{}{
		"guid":       strconv.FormatInt(conv.Guid, 10),
		"title":      conv.Title,
		"model":      conv.Model,
		"created_at": FormatMillis(conv.CreatedAt),
		"updated_at": FormatMillis(conv.UpdatedAt),
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

func Order(o *models.Order) map[string]interface{} {
	out := map[string]interface{}{
		"guid":              strconv.FormatInt(o.Guid, 10),
		"order_no":          o.OrderNo,
		"plan_type":         o.PlanType.String(),
		"amount":            o.Amount,
		"status":            o.Status.String(),
		"invoice_requested": o.InvoiceRequested,
		"created_at":        FormatMillis(o.CreatedAt),
		"paid_at":           nil,
	}
	if o.PaidAt != nil {
		out["paid_at"] = FormatMillis(*o.PaidAt)
	}
	return out
}

func AdminUser(user *models.User) map[string]interface{} {
	return map[string]interface{}{
		"guid":              strconv.FormatInt(user.Guid, 10),
		"nickname":          user.Nickname,
		"plan_type":         user.PlanType.String(),
		"status":            user.Status.String(),
		"is_verified":       user.IsVerified,
		"total_tokens_used": user.TotalTokensUsed,
		"created_at":        FormatMillis(user.CreatedAt),
	}
}

func AuditLog(log *models.AuditLog) map[string]interface{} {
	return map[string]interface{}{
		"guid":       strconv.FormatInt(log.Guid, 10),
		"action":     log.Action,
		"resource":   log.Resource,
		"detail":     log.Detail,
		"ip":         log.IP,
		"created_at": FormatMillis(log.CreatedAt),
	}
}

func ModelHealth(h *models.ModelHealth) map[string]interface{} {
	out := map[string]interface{}{
		"guid":            strconv.FormatInt(h.Guid, 10),
		"model_name":      h.ModelName,
		"provider":        h.Provider,
		"is_available":    h.IsAvailable,
		"avg_latency_ms":  h.AvgLatencyMs,
		"error_rate":      h.ErrorRate,
		"last_checked_at": nil,
	}
	if h.LastCheckedAt != nil {
		out["last_checked_at"] = FormatMillis(*h.LastCheckedAt)
	}
	return out
}
