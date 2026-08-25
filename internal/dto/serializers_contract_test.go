package dto

import (
	"encoding/json"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestAuditLogNeverExposesInternalUserID(t *testing.T) {
	userID := int64(42)
	payload, err := json.Marshal(AuditLog(&models.AuditLog{AuditFields: models.AuditFields{Guid: 9001}, UserID: &userID}))
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) == "" || containsJSONField(payload, "user_id") {
		t.Fatalf("audit DTO leaked internal user_id: %s", payload)
	}
}

func containsJSONField(payload []byte, field string) bool {
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		return true
	}
	_, found := object[field]
	return found
}
