package dto

import (
	"encoding/json"
	"testing"

	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestUserDTOsNeverExposeAuthenticationSecrets(t *testing.T) {
	password := "$argon2id$secret"
	phone := "13800138000"
	user := &models.User{PasswordHash: &password, Phone: &phone}
	for name, value := range map[string]map[string]interface{}{"profile": UserProfile(user), "admin": AdminUser(user)} {
		for _, forbidden := range []string{"id", "password_hash", "refresh_token", "authorization", "phone"} {
			if _, found := value[forbidden]; found {
				t.Fatalf("%s DTO leaked %s", name, forbidden)
			}
		}
	}
}

func TestLegacyAdminUserShapeExcludesAuthVersion(t *testing.T) {
	user := &models.User{AuthVersion: 7}
	encoded, err := json.Marshal(AdminUser(user))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatal(err)
	}
	want := []string{"created_at", "guid", "is_verified", "nickname", "plan_type", "status", "total_tokens_used"}
	if len(body) != len(want) {
		t.Fatalf("legacy shape changed: %s", encoded)
	}
	for _, key := range want {
		if _, ok := body[key]; !ok {
			t.Fatalf("legacy shape missing %q: %s", key, encoded)
		}
	}
	if _, ok := body["auth_version"]; ok {
		t.Fatalf("legacy shape exposed auth_version: %s", encoded)
	}
}
