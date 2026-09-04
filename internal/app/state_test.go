package app

import (
	"bytes"
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

func TestNewStateActionSecurityCryptoLifecycle(t *testing.T) {
	withoutKey, err := NewState(&config.Settings{}, nil)
	if err != nil {
		t.Fatalf("NewState(without key) error = %v", err)
	}
	if withoutKey.ActionSecurityCrypto != nil {
		t.Fatal("NewState constructed action-security crypto without a root key")
	}

	root := bytes.Repeat([]byte{0x42}, 32)
	withKey, err := NewState(&config.Settings{ActionSecurityHMACKey: root}, nil)
	if err != nil {
		t.Fatalf("NewState(with key) error = %v", err)
	}
	if withKey.ActionSecurityCrypto == nil {
		t.Fatal("NewState did not construct action-security crypto")
	}
	if !bytes.Equal(root, bytes.Repeat([]byte{0x42}, 32)) {
		t.Fatal("NewState mutated configured root key")
	}
}
