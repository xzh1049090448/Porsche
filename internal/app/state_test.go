package app

import (
	"bytes"
	"context"
	"errors"
	"os"
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

func TestNewStateLeavesGenerationStoreNilWithoutRedis(t *testing.T) {
	state, err := NewState(&config.Settings{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.PlatformGenerations != nil {
		t.Fatal("generation store enabled without Redis")
	}
}

func TestNewStateFailsClosedForConfiguredInvalidRedis(t *testing.T) {
	state, err := NewState(&config.Settings{RedisURL: "not-a-redis-url", AuthHMACKey: "test-auth-hmac-key-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ"}, nil)
	if err == nil || state != nil {
		t.Fatalf("NewState() state=%#v error=%v, want fail-closed Redis construction", state, err)
	}
}

func TestNewStateWiresGenerationStoreWithTestRedis(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("BLOCKED_FIXTURE: requires TEST_REDIS_URL")
	}
	settings := &config.Settings{RedisURL: url, AuthHMACKey: "test-auth-hmac-key-0123456789-ABCDEFGHIJKLMNOPQRSTUVWXYZ"}
	state, err := NewState(settings, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.AuthRedis == nil || state.PlatformGenerations == nil {
		t.Fatalf("stores not independently initialized: %#v", state)
	}
	if err := state.PlatformGenerations.CheckAvailable(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = state.PlatformGenerations.Close()
	_ = state.AuthRedis.Close()
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
	if withoutKey.UserManagementActions != nil {
		t.Fatal("NewState constructed user management actions without a root key")
	}

	root := bytes.Repeat([]byte{0x42}, 32)
	if _, err := NewState(&config.Settings{ActionSecurityHMACKey: root}, nil); err == nil {
		t.Fatal("NewState accepted a root key with partial database/Redis dependencies")
	}
	if !bytes.Equal(root, bytes.Repeat([]byte{0x42}, 32)) {
		t.Fatal("NewState mutated configured root key")
	}
}

func TestNewStateAssignsCompleteCreateAccountActionsAndDeleteCompatibilityView(t *testing.T) {
	root := bytes.Repeat([]byte{0x43}, 32)
	want := &service.UserManagementActions{
		Verifications: &service.ActionVerificationService{},
		Operations:    &service.ActionOperationService{},
		DeleteOutbox:  &service.AdminActionOutboxWriter{},
		CreateOutbox:  &service.CreateAccountOutboxWriter{},
		NewDeleteExecution: func(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) {
			return nil, service.ErrActionOperationUnavailable
		},
		NewCreateExecution: func(actionsecurity.Action, actionsecurity.CreateAccountIntent, []byte, service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error) {
			return nil, service.ErrActionOperationUnavailable
		},
	}
	var gotDB *gorm.DB
	var gotAuthRedis *service.AuthRedis
	var gotCrypto *actionsecurity.Crypto
	client := newStateCloseTrackingRedisClient()
	generationClient := newStateCloseTrackingRedisClient()
	constructors := defaultStateConstructors()
	constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
		return service.NewAuthRedis(client, "state-test-auth-hmac-key")
	}
	constructors.newPlatformGenerationStoreFromURL = func(context.Context, string) (*service.PlatformGenerationStore, error) {
		return service.NewPlatformGenerationStore(generationClient)
	}
	constructors.newUserManagementActions = func(db *gorm.DB, authRedis *service.AuthRedis, crypto *actionsecurity.Crypto) (*service.UserManagementActions, error) {
		gotDB, gotAuthRedis, gotCrypto = db, authRedis, crypto
		return want, nil
	}
	db := &gorm.DB{}
	state, err := newState(&config.Settings{RedisURL: "redis://configured", AuthHMACKey: "configured", ActionSecurityHMACKey: root}, db, constructors)
	if err != nil {
		t.Fatal(err)
	}
	if state.UserManagementActions != want || state.ActionVerifications != want.Verifications || gotDB != db || gotAuthRedis != state.AuthRedis || gotCrypto != state.ActionSecurityCrypto {
		t.Fatalf("incoherent state/bundle wiring: state=%#v", state)
	}
	if state.UserDeleteActions == nil || state.UserDeleteActions.Verifications != want.Verifications || state.UserDeleteActions.Operations != want.Operations ||
		state.UserDeleteActions.Outbox != want.DeleteOutbox || state.UserDeleteActions.NewExecution == nil {
		t.Fatalf("delete compatibility view did not share the complete bundle: %#v", state.UserDeleteActions)
	}
	if client.closes != 0 {
		t.Fatalf("successful state construction closed owned Redis %d times", client.closes)
	}
	if generationClient.closes != 0 {
		t.Fatalf("successful state construction closed owned generation Redis %d times", generationClient.closes)
	}
	if err := state.AuthRedis.Close(); err != nil || client.closes != 1 {
		t.Fatalf("existing state Redis cleanup error/closes = %v/%d, want nil/1", err, client.closes)
	}
	if err := state.PlatformGenerations.Close(); err != nil || generationClient.closes != 1 {
		t.Fatalf("existing generation Redis cleanup error/closes = %v/%d, want nil/1", err, generationClient.closes)
	}
	if !bytes.Equal(root, bytes.Repeat([]byte{0x43}, 32)) {
		t.Fatal("NewState mutated configured root key")
	}
}

func TestNewStateCreateAccountActionsFailureExposesNoCreateRouteDependency(t *testing.T) {
	complete := func() *service.UserManagementActions {
		return &service.UserManagementActions{
			Verifications: &service.ActionVerificationService{}, Operations: &service.ActionOperationService{},
			DeleteOutbox: &service.AdminActionOutboxWriter{}, CreateOutbox: &service.CreateAccountOutboxWriter{},
			NewDeleteExecution: func(actionsecurity.DeleteUserIntent) (*service.DeleteUserExecution, error) { return nil, nil },
			NewCreateExecution: func(actionsecurity.Action, actionsecurity.CreateAccountIntent, []byte, service.CreateAccountRequestMetadata) (*service.CreateAccountExecution, error) {
				return nil, nil
			},
		}
	}
	tests := []struct {
		name   string
		mutate func(*service.UserManagementActions)
		err    error
	}{
		{name: "constructor error", err: service.ErrActionVerificationUnavailable},
		{name: "verification service", mutate: func(bundle *service.UserManagementActions) { bundle.Verifications = nil }},
		{name: "operation service", mutate: func(bundle *service.UserManagementActions) { bundle.Operations = nil }},
		{name: "delete writer", mutate: func(bundle *service.UserManagementActions) { bundle.DeleteOutbox = nil }},
		{name: "create writer", mutate: func(bundle *service.UserManagementActions) { bundle.CreateOutbox = nil }},
		{name: "delete factory", mutate: func(bundle *service.UserManagementActions) { bundle.NewDeleteExecution = nil }},
		{name: "create factory", mutate: func(bundle *service.UserManagementActions) { bundle.NewCreateExecution = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newStateCloseTrackingRedisClient()
			generationClient := newStateCloseTrackingRedisClient()
			constructors := defaultStateConstructors()
			constructors.newAuthRedisFromURL = func(context.Context, string, string) (*service.AuthRedis, error) {
				return service.NewAuthRedis(client, "state-test-auth-hmac-key")
			}
			constructors.newPlatformGenerationStoreFromURL = func(context.Context, string) (*service.PlatformGenerationStore, error) {
				return service.NewPlatformGenerationStore(generationClient)
			}
			constructors.newUserManagementActions = func(*gorm.DB, *service.AuthRedis, *actionsecurity.Crypto) (*service.UserManagementActions, error) {
				bundle := complete()
				if test.mutate != nil {
					test.mutate(bundle)
				}
				return bundle, test.err
			}
			state, err := newState(&config.Settings{RedisURL: "redis://configured", AuthHMACKey: "configured", ActionSecurityHMACKey: bytes.Repeat([]byte{0x44}, 32)}, &gorm.DB{}, constructors)
			if state != nil || !errors.Is(err, service.ErrActionVerificationUnavailable) || err.Error() != service.ErrActionVerificationUnavailable.Error() {
				t.Fatalf("state/error = %#v/%v, want nil/fixed sanitized unavailable", state, err)
			}
			if client.closes != 1 {
				t.Fatalf("failed state construction closed owned Redis %d times, want 1", client.closes)
			}
			if generationClient.closes != 1 {
				t.Fatalf("failed state construction closed owned generation Redis %d times, want 1", generationClient.closes)
			}
		})
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
