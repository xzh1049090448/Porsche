package db

import (
	"strings"
	"testing"
)

// TestOpenNeverLeaksDatabaseURLCredentials ensures startup errors cannot
// expose DATABASE_URL credentials through logs.
func TestOpenNeverLeaksDatabaseURLCredentials(t *testing.T) {
	_, err := Open("sqlite://secret-user:secret-password@localhost/private", "test")
	if err == nil {
		t.Fatal("Open() accepted a non-MySQL URL")
	}
	if strings.Contains(err.Error(), "secret-user") || strings.Contains(err.Error(), "secret-password") {
		t.Fatalf("Open() leaked credentials: %v", err)
	}
}

func TestMalformedMySQLURLNeverLeaksCredentials(t *testing.T) {
	_, err := Open("mysql://secret-user:secret-password@[::1", "test")
	if err == nil {
		t.Fatal("Open() accepted malformed MySQL URL")
	}
	if strings.Contains(err.Error(), "secret-user") || strings.Contains(err.Error(), "secret-password") {
		t.Fatalf("Open() leaked credentials from malformed URL: %v", err)
	}
}
