package handler

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/porsche/ai-gateway-go/internal/app"
	"github.com/porsche/ai-gateway-go/internal/dto"
	"github.com/porsche/ai-gateway-go/internal/httpx"
	"github.com/porsche/ai-gateway-go/internal/middleware"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/service"
)

func RegisterConversations(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/conversations", middleware.RequireUser(state))

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
			Title          *string `json:"title"`
			Model          *string `json:"model"`
			DatasetEnabled bool    `json:"dataset_enabled"`
			DatasetIDs     []int   `json:"dataset_ids"`
		}
		_ = c.ShouldBindJSON(&body)
		title, model := "", ""
		if body.Title != nil {
			title = *body.Title
		}
		if body.Model != nil {
			model = *body.Model
		}
		conv, err := service.CreateConversation(state.DB, user, title, model, body.DatasetEnabled, body.DatasetIDs)
		if err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, err.Error())
			return
		}
		c.JSON(http.StatusOK, dto.Conversation(conv, false))
	})

	g.GET("/:id", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		conv, err := service.GetConversation(state.DB, user, uint(id), true)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, dto.Conversation(conv, true))
	})

	g.PUT("/:id", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		var body struct {
			Title *string `json:"title"`
		}
		_ = c.ShouldBindJSON(&body)
		var conv *models.Conversation
		var err error
		if body.Title != nil && *body.Title != "" {
			conv, err = service.UpdateConversationTitle(state.DB, user, uint(id), *body.Title)
		} else {
			conv, err = service.GetConversation(state.DB, user, uint(id), true)
		}
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, dto.Conversation(conv, true))
	})

	g.DELETE("/:id", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		if err := service.DeleteConversation(state.DB, user, uint(id)); err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "对话已删除"})
	})

	g.GET("/:id/export/markdown", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		conv, err := service.GetConversation(state.DB, user, uint(id), true)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(service.ExportMarkdown(conv)))
	})
}

func RegisterDatasets(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/datasets", middleware.RequireUser(state))
	g.GET("", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var rows []models.Dataset
		state.DB.Where("status = ?", models.DatasetActive).Order("id asc").Find(&rows)
		items := make([]map[string]interface{}, 0)
		plan := string(user.PlanType)
		for i := range rows {
			ds := &rows[i]
			if len(user.AllowedDatasets) > 0 {
				found := false
				for _, id := range user.AllowedDatasets {
					if id == int(ds.ID) {
						found = true
						break
					}
				}
				if !found {
					continue
				}
			}
			if len(ds.AccessPlans) > 0 {
				ok := false
				for _, p := range ds.AccessPlans {
					if p == plan {
						ok = true
						break
					}
				}
				if !ok {
					continue
				}
			}
			items = append(items, dto.Dataset(ds))
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})
}
