package service

import (
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"gorm.io/gorm"
)

type BillingService struct {
	settings *config.Settings
}

func NewBillingService(settings *config.Settings) *BillingService {
	return &BillingService{settings: settings}
}

func (b *BillingService) GetPlans(currentPlan string) []map[string]interface{} {
	return []map[string]interface{}{
		{
			"plan_type":        "free",
			"name":             "免费版",
			"price":            0.0,
			"daily_call_limit": 100,
			"description":      "每日100次调用，基础模型与基础数据集",
			"features":         []string{"基础模型", "基础数据集", "每日100次调用"},
		},
		{
			"plan_type":        "professional",
			"name":             "专业版",
			"price":            b.settings.PlanProfessionalPrice,
			"daily_call_limit": nil,
			"description":      "无限次调用，全模型与全数据集",
			"features":         []string{"全模型", "全数据集", "无限次调用", "模型对比"},
		},
		{
			"plan_type":        "enterprise",
			"name":             "企业版",
			"price":            b.settings.PlanEnterprisePrice,
			"daily_call_limit": nil,
			"description":      "定制化需求，专属数据集部署与API授权",
			"features":         []string{"专属数据集部署", "API授权", "定制化服务", "专属客服"},
		},
	}
}

func ResetDailyIfNeeded(user *models.User) {
	now := time.Now().UTC()
	if user.DailyCallsResetAt == nil || user.DailyCallsResetAt.UTC().Format("2006-01-02") != now.Format("2006-01-02") {
		user.DailyCallsUsed = 0
		user.DailyCallsResetAt = &now
	}
}

func (b *BillingService) CheckAndConsumeCall(db *gorm.DB, user *models.User, count int) error {
	if count < 1 {
		return nil
	}
	ResetDailyIfNeeded(user)
	if user.PlanType == models.PlanProfessional || user.PlanType == models.PlanEnterprise {
		user.DailyCallsUsed += count
		return db.Save(user).Error
	}
	if user.DailyCallsUsed+count > user.DailyCallLimit {
		return errTooMany("今日调用次数已达上限，请升级套餐")
	}
	user.DailyCallsUsed += count
	return db.Save(user).Error
}

func (b *BillingService) CreateOrder(db *gorm.DB, user *models.User, planType string) (*models.Order, error) {
	if planType == string(models.PlanFree) {
		return nil, errBadRequest("免费版无需购买")
	}
	var plan models.PlanType
	switch planType {
	case string(models.PlanProfessional):
		plan = models.PlanProfessional
	case string(models.PlanEnterprise):
		plan = models.PlanEnterprise
	default:
		return nil, errBadRequest("无效的套餐类型")
	}
	price := b.settings.PlanProfessionalPrice
	if plan == models.PlanEnterprise {
		price = b.settings.PlanEnterprisePrice
	}
	order := models.Order{
		OrderNo:  newOrderNo(),
		UserID:   user.ID,
		PlanType: plan,
		Amount:   price,
		Status:   models.OrderPending,
	}
	return &order, db.Create(&order).Error
}

func (b *BillingService) PayOrder(db *gorm.DB, user *models.User, orderID uint) (*models.Order, error) {
	if !b.settings.BillingAllowMockPayment {
		return nil, errForbidden("在线支付未开通，请通过支付渠道完成付款后由系统确认")
	}
	var order models.Order
	if err := db.Where("id = ? AND user_id = ?", orderID, user.ID).First(&order).Error; err != nil {
		return nil, errNotFound("订单不存在")
	}
	if order.Status != models.OrderPending {
		return nil, errBadRequest("订单状态不可支付")
	}
	now := time.Now().UTC()
	order.Status = models.OrderPaid
	order.PaidAt = &now
	user.PlanType = order.PlanType
	user.DailyCallLimit = 999999
	if err := db.Save(&order).Error; err != nil {
		return nil, err
	}
	if err := db.Save(user).Error; err != nil {
		return nil, err
	}
	return &order, nil
}

func (b *BillingService) RequestInvoice(db *gorm.DB, user *models.User, orderID uint) (*models.Order, error) {
	var order models.Order
	if err := db.Where("id = ? AND user_id = ?", orderID, user.ID).First(&order).Error; err != nil {
		return nil, errNotFound("订单不存在")
	}
	if order.Status != models.OrderPaid {
		return nil, errBadRequest("仅已支付订单可申请发票")
	}
	order.InvoiceRequested = true
	return &order, db.Save(&order).Error
}

func GetUsageStats(user *models.User) map[string]interface{} {
	ResetDailyIfNeeded(user)
	remaining := 999999
	if user.PlanType == models.PlanFree {
		remaining = user.DailyCallLimit - user.DailyCallsUsed
		if remaining < 0 {
			remaining = 0
		}
	}
	return map[string]interface{}{
		"total_tokens_used":     user.TotalTokensUsed,
		"dataset_calls":         user.DatasetCalls,
		"daily_calls_used":      user.DailyCallsUsed,
		"daily_call_limit":      user.DailyCallLimit,
		"remaining_daily_calls": remaining,
		"plan_type":             string(user.PlanType),
	}
}
