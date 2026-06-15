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

func RegisterBilling(r *gin.Engine, state *app.State) {
	g := r.Group("/api/v1/billing", middleware.RequireUser(state))

	g.GET("/plans", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		c.JSON(http.StatusOK, gin.H{
			"plans":        state.Billing.GetPlans(string(user.PlanType)),
			"current_plan": string(user.PlanType),
		})
	})

	g.POST("/orders", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body struct {
			PlanType string `json:"plan_type" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		order, err := state.Billing.CreateOrder(state.DB, user, body.PlanType)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, dto.Order(order))
	})

	g.POST("/orders/:id/pay", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("id"), 10, 64)
		order, err := state.Billing.PayOrder(state.DB, user, uint(id))
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, dto.Order(order))
	})

	g.GET("/orders", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var orders []models.Order
		state.DB.Where("user_id = ?", user.ID).Order("created_at desc").Find(&orders)
		items := make([]map[string]interface{}, 0, len(orders))
		for i := range orders {
			items = append(items, dto.Order(&orders[i]))
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})

	g.POST("/invoice", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body struct {
			OrderID uint `json:"order_id" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		order, err := state.Billing.RequestInvoice(state.DB, user, body.OrderID)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "发票申请已提交", "order_no": order.OrderNo})
	})
}
