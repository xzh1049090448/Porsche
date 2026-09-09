package handler

import (
	"regexp"
	"unicode"

	"github.com/porsche/ai-gateway-go/internal/security"
)

func serviceHashPassword(p string) (string, error) { return security.HashPassword(p) }
func serviceVerifyPassword(plain, hash string) bool { return security.VerifyPassword(plain, hash) }

var idCardRe = regexp.MustCompile(`^\d{15}$|^\d{17}[\dXx]$`)

func isValidIDCard(id string) bool {
	if !idCardRe.MatchString(id) {
		return false
	}
	if len(id) == 18 {
		for _, r := range id[:17] {
			if !unicode.IsDigit(r) {
				return false
			}
		}
		last := id[17]
		return unicode.IsDigit(rune(last)) || last == 'X' || last == 'x'
	}
	return true
}
