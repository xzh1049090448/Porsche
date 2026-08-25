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
			"plans":        state.Billing.GetPlans(user.PlanType.String()),
			"current_plan": user.PlanType.String(),
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

	g.POST("/orders/:guid/pay", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		id, _ := strconv.ParseUint(c.Param("guid"), 10, 64)
		order, err := state.Billing.PayOrder(state.DB, user, int64(id))
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
		if err := state.DB.Where("user_id = ? AND is_deleted = 0", user.ID).Order("created_at desc").Find(&orders).Error; err != nil {
			httpx.AbortJSON(c, http.StatusInternalServerError, "读取订单失败")
			return
		}
		items := make([]map[string]interface{}, 0, len(orders))
		for i := range orders {
			items = append(items, dto.Order(&orders[i]))
		}
		c.JSON(http.StatusOK, gin.H{"items": items})
	})

	g.POST("/invoice", func(c *gin.Context) {
		user := middleware.CurrentUser(c)
		var body struct {
			OrderGUID int64 `json:"order_guid" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			httpx.AbortJSON(c, http.StatusUnprocessableEntity, err.Error())
			return
		}
		order, err := state.Billing.RequestInvoice(state.DB, user, body.OrderGUID)
		if err != nil {
			code, msg := service.StatusFromError(err)
			httpx.AbortJSON(c, code, msg)
			return
		}
		c.JSON(http.StatusOK, gin.H{"message": "发票申请已提交", "order_no": order.OrderNo})
	})
}
