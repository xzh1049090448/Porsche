package handler

import (
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func RegisterConversations(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/conversations", middleware.RequireUser(state))
	registerConversationRoutes(g, state)
}

func registerConversationRoutes(g *gin.RouterGroup, state *app.State) {
	g.GET("", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		skip := parseUintQuery(c, "skip", 0)
		limit := parseUintQuery(c, "limit", 20)
		if limit > 100 {
			limit = 100
		}
		items, total, err := service.ListConversations(state.DB, user, skip, limit)
		if err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, err.Error())
			return
		}
		out := make([]map[string]interface{}, 0, len(items))
		for i := range items {
			out = append(out, dto.Conversation(&items[i], false))
		}
		c.JSON(http.StatusOK, gin.H{"items": out, "total": total})
	})

	g.POST("", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body struct {
			Title *string `json:"title"`
			Model *string `json:"model"`
		}
		_ = c.ShouldBindJSON(&body)
		title, model := "", ""
		if body.Title != nil {
			title = *body.Title
		}
		if body.Model != nil {
			model = *body.Model
		}
		conv, err := service.CreateConversation(state.DB, user, title, model)
		if err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, dto.Conversation(conv, false))
	})

	g.GET("/:guid", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("guid"), 10, 64)
		detail, err := service.GetConversationDetail(c.Request.Context(), state.DB, user, int64(id))
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		writeConversationDetail(c, detail)
	})

	g.PUT("/:guid", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("guid"), 10, 64)
		var body struct {
			Title *string `json:"title"`
		}
		_ = c.ShouldBindJSON(&body)
		if body.Title != nil && *body.Title != "" {
			if _, err := service.UpdateConversationTitle(state.DB, user, int64(id), *body.Title); err != nil {
				code, msg := service.StatusFromError(err)
				httpx.AbortJSON(c, code, msg)
				return
			}
		}
		detail, err := service.GetConversationDetail(c.Request.Context(), state.DB, user, int64(id))
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		writeConversationDetail(c, detail)
	})

	g.DELETE("/:guid", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("guid"), 10, 64)
		if err := service.DeleteConversation(state.DB, user, int64(id)); err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "对话已删除"})
	})

	g.GET("/:guid/export/markdown", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("guid"), 10, 64)
		conv, err := service.GetConversation(state.DB, user, int64(id), true)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(service.ExportMarkdown(conv)))
	})
}

func writeConversationDetail(c *gin.Context, detail *service.ConversationDetail) {
	if detail.OmittedGenerationGroupCount > 0 {
		log.Printf(
			"conversation_detail_omitted_generation_groups request_id=%q conversation_guid=%q omitted_count=%d",
			c.Writer.Header().Get("X-Request-ID"),
			strconv.FormatInt(detail.Conversation.Guid, 10),
			detail.OmittedGenerationGroupCount,
		)
	}
	c.JSON(http.StatusOK, dto.ConversationDetail(detail))
}
