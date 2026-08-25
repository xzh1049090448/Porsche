package service

import (
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

// TestNewModelHealthUsesSystemAuditMetadata pins the invariant for health
// records created by unauthenticated system health checks.
func TestNewModelHealthUsesSystemAuditMetadata(t *testing.T) {
	health := NewModelHealth("model-a", "whitelabel", 1700000000000, 9001)
	if health.Guid != 9001 || health.CreatedAt != 1700000000000 || health.UpdatedAt != 1700000000000 {
		t.Fatalf("missing creation audit metadata: %#v", health.AuditFields)
	}
	if health.CreatedBy != nil || health.UpdatedBy != nil || health.IsDeleted != 0 {
		t.Fatalf("system health record has invalid actors or deletion state: %#v", health.AuditFields)
	}
}

func TestTouchAuditTreatsZeroActorAsSystem(t *testing.T) {
	fields := models.AuditFields{UpdatedBy: ptrInt64(99)}
	TouchAudit(&fields, 0)
	if fields.UpdatedBy != nil {
		t.Fatalf("system audit wrote pseudo-user 0: %#v", fields.UpdatedBy)
	}
}

func ptrInt64(value int64) *int64 { return &value }
