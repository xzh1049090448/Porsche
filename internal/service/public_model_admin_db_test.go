package service

import (
	"os"
	"testing"
)

func TestPublicModelRevisionDeleteIdentityDBFixture(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("BLOCKED_FIXTURE: requires explicit TEST_DATABASE_URL; .env is never read")
	}
}
