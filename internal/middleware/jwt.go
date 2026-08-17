package middleware

import (
	"fmt"

	"github.com/golang-jwt/jwt/v5"
)

func claimSubject(claims jwt.MapClaims) (string, bool) {
	v, ok := claims["sub"]
	if !ok {
		return "", false
	}
	switch t := v.(type) {
	case string:
		return t, true
	case float64:
		return fmt.Sprintf("%.0f", t), true
	default:
		return fmt.Sprint(t), true
	}
}
