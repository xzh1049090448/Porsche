package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/migration"
	"github.com/porsche/ai-gateway-go/internal/models"
	"github.com/porsche/ai-gateway-go/internal/persistence"
)

// TestPayOrderConcurrentSettlement is opt-in: TEST_DATABASE_URL must name a
// dedicated disposable MySQL 8 test database. It applies the embedded schema
// but never targets DATABASE_URL or a deployment database.
func TestPayOrderConcurrentSettlement(t *testing.T) {
	db := openTestMySQL(t)
	generator := persistence.NewSnowflake(0, persistence.SystemClock())
	if err := migration.Up(context.Background(), db, generator.Next, func() int64 { return time.Now().UTC().UnixMilli() }); err != nil {
		t.Fatalf("prepare isolated MySQL schema: %v", err)
	}
	now := time.Now().UTC().UnixMilli()
	phone := "9" + time.Now().UTC().Format("20060102150405")
	user := models.User{AuditFields: models.AuditFields{Guid: generator.Next(), CreatedAt: now, UpdatedAt: now, IsDeleted: 0}, Phone: phone, Status: models.UserStatusActive, PlanType: models.PlanFree, AllowedModels: models.JSONSlice{}}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	order := models.Order{AuditFields: models.AuditFields{Guid: generator.Next(), CreatedAt: now, CreatedBy: &user.ID, UpdatedAt: now, UpdatedBy: &user.ID, IsDeleted: 0}, OrderNo: "test-" + time.Now().UTC().Format("20060102150405.000000000"), UserID: user.ID, PlanType: models.PlanProfessional, Status: models.OrderPending}
	if err := db.Create(&order).Error; err != nil {
		t.Fatal(err)
	}
	billing := NewBillingService(&config.Settings{BillingAllowMockPayment: true})
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			copy := user
			<-start
			_, err := billing.PayOrder(db, &copy, order.Guid)
			errs <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent settlement successes = %d, want 1", successes)
	}
	var storedOrder models.Order
	if err := db.Where("guid = ? AND is_deleted = 0", order.Guid).First(&storedOrder).Error; err != nil {
		t.Fatal(err)
	}
	if storedOrder.Status != models.OrderPaid || storedOrder.PaidAt == nil {
		t.Fatalf("order was not settled exactly once: %#v", storedOrder)
	}
	var storedUser models.User
	if err := db.Where("id = ? AND is_deleted = 0", user.ID).First(&storedUser).Error; err != nil {
		t.Fatal(err)
	}
	if storedUser.PlanType != models.PlanProfessional || storedUser.DailyCallLimit != 999999 {
		t.Fatalf("user entitlement was not applied: %#v", storedUser)
	}
}
