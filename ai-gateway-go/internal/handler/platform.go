package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/constants"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func RegisterPlatform(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/platform", middleware.RequireUser(state))

	g.GET("/models", func(c *gin.Context) {
		modelsOut := make([]map[string]interface{}, 0)
		for _, name := range constants.PlatformModelIDs {
			route, ok := state.Models.Get(name)
			if !ok {
				continue
			}
			modelsOut = append(modelsOut, map[string]interface{}{
				"id":             name,
				"provider":       route.Provider,
				"upstream_model": route.UpstreamModel,
			})
		}
		c.JSON(http.StatusOK, gin.H{"models": modelsOut})
	})

	g.POST("/chat/completions", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body platformChatBody
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		params := body.toParams()
		if body.Stream {
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			err := state.Platform.Stream(c.Request.Context(), state.DB, user, params, func(b []byte) error {
				_, werr := c.Writer.Write(b)
				c.Writer.Flush()
				return werr
			})
			if err != nil {
				code, msg := service.StatusFromError(err)
				if code >= 500 {
					code = http.StatusBadRequest
				}
				_, _ = c.Writer.Write([]byte("data: {\"error\":\"" + msg + "\"}\n\n"))
			}
			return
		}
		result, err := state.Platform.Chat(c.Request.Context(), state.DB, user, params)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		uid := user.ID
		_ = state.Audit.Log(state.DB, "chat.complete", &uid, "", nil, httpx.ClientIP(c, state.Settings.TrustProxyHeaders))
		c.JSON(http.StatusOK, result)
	})

	g.POST("/chat/compare", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body platformCompareBody
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		params := body.toParams()
		if body.Stream {
			c.Header("Content-Type", "text/event-stream")
			c.JSON(http.StatusNotImplemented, gin.H{"detail": "compare stream not yet implemented in go gateway"})
			return
		}
		result, err := state.Platform.Compare(c.Request.Context(), state.DB, user, body.Models, params)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, result)
	})
}

type platformChatBody struct {
	Model          string                   `json:"model" binding:"required"`
	Messages       []map[string]interface{} `json:"messages" binding:"required"`
	ConversationID *uint                    `json:"conversation_id"`
	Temperature    *float64                 `json:"temperature"`
	MaxTokens      *int                     `json:"max_tokens"`
	ContextWindow  *int                     `json:"context_window"`
	Stream         bool                     `json:"stream"`
	DatasetEnabled bool                     `json:"dataset_enabled"`
	DatasetIDs     []int                    `json:"dataset_ids"`
}

func (b platformChatBody) toParams() service.ChatParams {
	return service.ChatParams{
		Model:          b.Model,
		Messages:       b.Messages,
		ConversationID: b.ConversationID,
		Temperature:    b.Temperature,
		MaxTokens:      b.MaxTokens,
		ContextWindow:  b.ContextWindow,
		DatasetEnabled: b.DatasetEnabled,
		DatasetIDs:     b.DatasetIDs,
	}
}

type platformCompareBody struct {
	Models         []string                 `json:"models" binding:"required"`
	Messages       []map[string]interface{} `json:"messages" binding:"required"`
	ConversationID *uint                    `json:"conversation_id"`
	Temperature    *float64                 `json:"temperature"`
	MaxTokens      *int                     `json:"max_tokens"`
	ContextWindow  *int                     `json:"context_window"`
	Stream         bool                     `json:"stream"`
	DatasetEnabled bool                     `json:"dataset_enabled"`
	DatasetIDs     []int                    `json:"dataset_ids"`
}

func (b platformCompareBody) toParams() service.ChatParams {
	return service.ChatParams{
		Messages:       b.Messages,
		ConversationID: b.ConversationID,
		Temperature:    b.Temperature,
		MaxTokens:      b.MaxTokens,
		ContextWindow:  b.ContextWindow,
		DatasetEnabled: b.DatasetEnabled,
		DatasetIDs:     b.DatasetIDs,
	}
}
