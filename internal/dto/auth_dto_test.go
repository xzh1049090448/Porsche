package dto

import (
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
