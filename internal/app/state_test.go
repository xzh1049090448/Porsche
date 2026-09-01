package app

import (
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
