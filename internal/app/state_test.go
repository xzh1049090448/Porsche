package app

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/actionsecurity"
	"github.com/porsche/ai-gateway-go/internal/config"
	"github.com/porsche/ai-gateway-go/internal/service"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestNewStateDoesNotBootstrapRootFromSettings(t *testing.T) {
	settings := &config.Settings{
		RootBootstrapUsername: "root_admin",
		RootBootstrapPassword: "Aa1@0123456789ab",
	}

	state, err := NewState(settings, nil)
	if err != nil {
		t.Fatalf("NewState() error = %v, want no Root bootstrap attempt", err)
	}
	if state == nil {
		t.Fatal("NewState() returned nil state")
	}
}

func TestNewStateActionSecurityConstructorLifecycle(t *testing.T) {
	withoutKey, err := NewState(&config.Settings{}, nil)
	if err != nil {
		t.Fatalf("NewState(without key) error = %v", err)
	}
	if withoutKey.ActionSecurityCrypto != nil {
		t.Fatal("NewState constructed action-security crypto without a root key")
	}
	if withoutKey.ActionVerifications != nil {
		t.Fatal("NewState constructed action verification service without a root key")
	}
	if withoutKey.UserDeleteActions != nil {
		t.Fatal("NewState constructed user delete actions without a root key")
	}

	root := bytes.Repeat([]byte{0x42}, 32)
	if _, err := NewState(&config.Settings{ActionSecurityHMACKey: root}, nil); err == nil {
		t.Fatal("NewState accepted a root key with partial database/Redis dependencies")
	}
	if !bytes.Equal(root, bytes.Repeat([]byte{0x42}, 32)) {
		t.Fatal("NewState mutated configured root key")
	}
}

func TestNewStateAssignsCompleteUserDeleteBundleAndCompatibilityAlias(t *testing.T) {
	root := bytes.Repeat([]byte{0x43}, 32)
	want := &service.UserDeleteActions{
		Verifications: &service.ActionVerificationService{},
		Operations:    &service.ActionOperationService{},
		Outbox:        &service.AdminActionOutboxWriter{},
		NewExecution: func(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
			return nil, service.ErrActionOperationUnavailable
		},
	}
	var gotDB *gorm.DB
	var gotAuthRedis *service.AuthRedis
	var gotCrypto *actionsecurity.Crypto
	client := newStateCloseTrackingRedisClient()
	constructors := defaultStateConstructors()
	constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
		return service.NewAuthRedis(client, "state-test-auth-hmac-key")
	}
	constructors.newUserDeleteActions = func(db *gorm.DB, authRedis *service.AuthRedis, crypto *actionsecurity.Crypto) (*service.UserDeleteActions, error) {
		gotDB, gotAuthRedis, gotCrypto = db, authRedis, crypto
		return want, nil
	}
	db := &gorm.DB{}
	state, err := newState(&config.Settings{RedisURL: "redis://configured", AuthHMACKey: "configured", ActionSecurityHMACKey: root}, db, constructors)
	if err != nil {
		t.Fatal(err)
	}
	if state.UserDeleteActions != want || state.ActionVerifications != want.Verifications || gotDB != db || gotAuthRedis != state.AuthRedis || gotCrypto != state.ActionSecurityCrypto {
		t.Fatalf("incoherent state/bundle wiring: state=%#v", state)
	}
	if client.closes != 0 {
		t.Fatalf("successful state construction closed owned Redis %d times", client.closes)
	}
	if err := state.AuthRedis.Close(); err != nil || client.closes != 1 {
		t.Fatalf("existing state Redis cleanup error/closes = %v/%d, want nil/1", err, client.closes)
	}
	if !bytes.Equal(root, bytes.Repeat([]byte{0x43}, 32)) {
		t.Fatal("NewState mutated configured root key")
	}
}

func TestNewStateUserDeleteBundleFailureExposesNoPartialActionServices(t *testing.T) {
	client := newStateCloseTrackingRedisClient()
	constructors := defaultStateConstructors()
	constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
		return service.NewAuthRedis(client, "state-test-auth-hmac-key")
	}
	constructors.newUserDeleteActions = func(*gorm.DB, *service.AuthRedis, *actionsecurity.Crypto) (*service.UserDeleteActions, error) {
		return &service.UserDeleteActions{Verifications: &service.ActionVerificationService{}}, service.ErrActionVerificationUnavailable
	}
	state, err := newState(&config.Settings{RedisURL: "redis://configured", AuthHMACKey: "configured", ActionSecurityHMACKey: bytes.Repeat([]byte{0x44}, 32)}, &gorm.DB{}, constructors)
	if state != nil || !errors.Is(err, service.ErrActionVerificationUnavailable) || err.Error() != service.ErrActionVerificationUnavailable.Error() {
		t.Fatalf("state/error = %#v/%v, want nil/fixed sanitized unavailable", state, err)
	}
	if client.closes != 1 {
		t.Fatalf("failed state construction closed owned Redis %d times, want 1", client.closes)
	}
}

type stateCloseTrackingRedisClient struct {
	*redis.Client
	closes int
}

func newStateCloseTrackingRedisClient() *stateCloseTrackingRedisClient {
	return &stateCloseTrackingRedisClient{Client: redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})}
}

func (client *stateCloseTrackingRedisClient) Close() error {
	client.closes++
	return nil
}
