package service

import (
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
			"description":      "每日100次调用与基础模型访问",
			"features":         []string{"基础模型", "每日100次调用"},
		},
		{
			"plan_type":        "professional",
			"name":             "专业版",
			"price":            b.settings.PlanProfessionalPrice,
			"daily_call_limit": nil,
			"description":      "无限次调用与全模型访问",
			"features":         []string{"全模型", "无限次调用", "模型对比"},
		},
		{
			"plan_type":        "enterprise",
			"name":             "企业版",
			"price":            b.settings.PlanEnterprisePrice,
			"daily_call_limit": nil,
			"description":      "定制化模型接入与 API 授权",
			"features":         []string{"API授权", "定制化服务", "专属客服"},
		},
	}
}

func ResetDailyIfNeeded(user *models.User) {
	now := time.Now().UTC()
	if user.DailyCallsResetAt == nil || time.UnixMilli(*user.DailyCallsResetAt).UTC().Format("2006-01-02") != now.Format("2006-01-02") {
		user.DailyCallsUsed = 0
		nowMillis := now.UnixMilli()
		user.DailyCallsResetAt = &nowMillis
	}
}

func (b *BillingService) CheckAndConsumeCall(db *gorm.DB, user *models.User, count int) error {
	if count < 1 {
		return nil
	}
	ResetDailyIfNeeded(user)
	if user.PlanType == models.PlanProfessional || user.PlanType == models.PlanEnterprise {
		user.DailyCallsUsed += count
		stampUpdate(&user.AuditFields, user.ID)
		return db.Save(user).Error
	}
	if user.DailyCallsUsed+count > user.DailyCallLimit {
		return errTooMany("今日调用次数已达上限，请升级套餐")
	}
	user.DailyCallsUsed += count
	stampUpdate(&user.AuditFields, user.ID)
	return db.Save(user).Error
}

func (b *BillingService) CreateOrder(db *gorm.DB, user *models.User, planType string) (*models.Order, error) {
	if planType == models.PlanFree.String() {
		return nil, errBadRequest("免费版无需购买")
	}
	var plan models.PlanType
	switch planType {
	case models.PlanProfessional.String():
		plan = models.PlanProfessional
	case models.PlanEnterprise.String():
		plan = models.PlanEnterprise
	default:
		return nil, errBadRequest("无效的套餐类型")
	}
	price := b.settings.PlanProfessionalPrice
	if plan == models.PlanEnterprise {
		price = b.settings.PlanEnterprisePrice
	}
	order := models.Order{
		OrderNo:     newOrderNo(),
		UserID:      user.ID,
		PlanType:    plan,
		Amount:      price,
		Status:      models.OrderPending,
		AuditFields: auditFields(&user.ID),
	}
	return &order, db.Create(&order).Error
}

func (b *BillingService) PayOrder(db *gorm.DB, user *models.User, orderID int64) (*models.Order, error) {
	if !b.settings.BillingAllowMockPayment {
		return nil, errForbidden("在线支付未开通，请通过支付渠道完成付款后由系统确认")
	}
	var paidOrder models.Order
	var settledUser models.User
	err := db.Transaction(func(tx *gorm.DB) error {
		var order models.Order
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("guid = ? AND user_id = ? AND is_deleted = 0", orderID, user.ID).First(&order).Error; err != nil {
			return errNotFound("订单不存在")
		}
		if order.Status != models.OrderPending {
			return errBadRequest("订单状态不可支付")
		}
		var updatedUser models.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND is_deleted = 0", user.ID).First(&updatedUser).Error; err != nil {
			return errNotFound("用户不存在")
		}
		now := persistence.NowMillis()
		order.Status = models.OrderPaid
		order.PaidAt = &now
		stampUpdate(&order.AuditFields, user.ID)
		updatedUser.PlanType = order.PlanType
		updatedUser.DailyCallLimit = 999999
		stampUpdate(&updatedUser.AuditFields, user.ID)
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		if err := tx.Save(&updatedUser).Error; err != nil {
			return err
		}
		paidOrder = order
		settledUser = updatedUser
		return nil
	})
	if err != nil {
		return nil, err
	}
	*user = settledUser
	return &paidOrder, nil
}

func (b *BillingService) RequestInvoice(db *gorm.DB, user *models.User, orderID int64) (*models.Order, error) {
	var order models.Order
	if err := db.Where("guid = ? AND user_id = ? AND is_deleted = 0", orderID, user.ID).First(&order).Error; err != nil {
		return nil, errNotFound("订单不存在")
	}
	if order.Status != models.OrderPaid {
		return nil, errBadRequest("仅已支付订单可申请发票")
	}
	order.InvoiceRequested = true
	stampUpdate(&order.AuditFields, user.ID)
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
		"daily_calls_used":      user.DailyCallsUsed,
		"daily_call_limit":      user.DailyCallLimit,
		"remaining_daily_calls": remaining,
		"plan_type":             user.PlanType.String(),
	}
}
