package app

import (
	"context"
	"os"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/config"
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
