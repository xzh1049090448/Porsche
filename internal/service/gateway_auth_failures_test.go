package service

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"
)

// TestGatewayTokenAuthenticationStorageFailuresFailClosed injects GORM errors
// into this test's own connection only. It does not alter schema or use a
// shared production connection.
func TestGatewayTokenAuthenticationStorageFailuresFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table string
	}{
		{name: "token read", table: "gateway_api_tokens"},
		{name: "owner read", table: "users"},
		{name: "last used write", table: "gateway_api_tokens"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestMySQL(t)
			user := testUser(t, db, "13800138111")
			if err := db.Create(&user).Error; err != nil {
				t.Fatal(err)
			}
			svc := NewGatewayTokenService(db)
			_, secret, err := svc.Create(&user, GatewayTokenCreateInput{Name: "failure"})
			if err != nil {
				t.Fatal(err)
			}
			var hit atomic.Bool
			hook := "gateway_auth_failure_" + tc.name
			callback := db.Callback().Query()
			stage := "gorm:query"
			if tc.name == "last used write" {
				callback, stage = db.Callback().Update(), "gorm:update"
			}
			if err := callback.Before(stage).Register(hook, func(tx *gorm.DB) {
				if tx.Statement.Table == tc.table && hit.CompareAndSwap(false, true) {
					tx.AddError(errors.New("injected gateway storage failure"))
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer callback.Remove(hook)
			principal, err := svc.AuthenticatePrincipal(secret, "", "", time.Now())
			if !hit.Load() {
				t.Fatal("injection did not hit expected storage operation")
			}
			if principal != nil || !IsGatewayTokenError(err, GatewayTokenUnavailable) {
				t.Fatalf("principal=%v err=%v", principal, err)
			}
		})
	}
}

func TestGatewayTokenDeletedOwnerIsDisabled(t *testing.T) {
	db := openTestMySQL(t)
	user := testUser(t, db, "13800138112")
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	svc := NewGatewayTokenService(db)
	_, secret, err := svc.Create(&user, GatewayTokenCreateInput{Name: "owner-state"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&user).Update("is_deleted", 1).Error; err != nil {
		t.Fatal(err)
	}
	principal, err := svc.AuthenticatePrincipal(secret, "", "", time.Now())
	if principal != nil || !IsGatewayTokenError(err, GatewayTokenDisabled) {
		t.Fatalf("principal=%v err=%v", principal, err)
	}
}
