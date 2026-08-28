package middleware

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/porsche/ai-gateway-go/internal/models"
)

func TestSessionClaimsRequireAllBoundFields(t *testing.T) {
	valid := jwt.MapClaims{"sub": "9001", "sid": "0f5b20dd-90aa-4b99-aed4-0d8d74eaa8da", "sv": float64(3), "av": float64(7), "role": float64(models.UserRoleAdmin)}
	claims, ok := parseSessionClaims(valid)
	if !ok || claims.UserGUID != 9001 || claims.SessionVersion != 3 || claims.AuthVersion != 7 || claims.Role != models.UserRoleAdmin {
		t.Fatalf("valid claims parsed incorrectly: %#v, %t", claims, ok)
	}

	for _, field := range []string{"sub", "sid", "sv", "av", "role"} {
		copy := jwt.MapClaims{}
		for key, value := range valid {
			copy[key] = value
		}
		delete(copy, field)
		if _, ok := parseSessionClaims(copy); ok {
			t.Fatalf("claims missing %s were accepted", field)
		}
	}
}

func TestMinimumRoleRejectsEscalationAndAllowsRoot(t *testing.T) {
	if hasMinimumRole(models.UserRoleUser, models.UserRoleAdmin) {
		t.Fatal("ordinary user passed admin gate")
	}
	if hasMinimumRole(models.UserRoleAdmin, models.UserRoleRoot) {
		t.Fatal("admin passed root gate")
	}
	if !hasMinimumRole(models.UserRoleRoot, models.UserRoleRoot) || !hasMinimumRole(models.UserRoleRoot, models.UserRoleAdmin) {
		t.Fatal("root did not satisfy minimum role gate")
	}
}
