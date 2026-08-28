package middleware

import (
	"math"
	"strconv"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/porsche/ai-gateway-go/internal/models"
)

// SessionClaims is the complete, server-verifiable Access JWT identity. It
// intentionally contains the public user GUID rather than users.id.
type SessionClaims struct {
	UserGUID       int64
	SID            string
	SessionVersion int
	AuthVersion    int
	Role           models.UserRole
}

func parseSessionClaims(claims jwt.MapClaims) (SessionClaims, bool) {
	sub, ok := claims["sub"].(string)
	if !ok {
		return SessionClaims{}, false
	}
	guid, err := strconv.ParseInt(sub, 10, 64)
	if err != nil || guid <= 0 {
		return SessionClaims{}, false
	}
	sid, ok := claims["sid"].(string)
	if !ok || !validSID(sid) {
		return SessionClaims{}, false
	}
	sv, ok := integerClaim(claims["sv"])
	if !ok || sv <= 0 {
		return SessionClaims{}, false
	}
	av, ok := integerClaim(claims["av"])
	if !ok || av <= 0 {
		return SessionClaims{}, false
	}
	roleValue, ok := integerClaim(claims["role"])
	role := models.UserRole(roleValue)
	if !ok || !validRole(role) {
		return SessionClaims{}, false
	}
	return SessionClaims{UserGUID: guid, SID: sid, SessionVersion: sv, AuthVersion: av, Role: role}, true
}

func integerClaim(value any) (int, bool) {
	parsed, ok := value.(float64)
	if !ok || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 1 || parsed > float64(math.MaxInt) || math.Trunc(parsed) != parsed {
		return 0, false
	}
	return int(parsed), true
}

func validRole(role models.UserRole) bool {
	return role == models.UserRoleUser || role == models.UserRoleAdmin || role == models.UserRoleRoot
}

func hasMinimumRole(role, minimum models.UserRole) bool {
	return validRole(role) && validRole(minimum) && role >= minimum
}

func validSID(sid string) bool {
	if len(sid) != 36 {
		return false
	}
	for i, character := range sid {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
