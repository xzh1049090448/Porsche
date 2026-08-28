package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewRefreshSecret returns 64 bytes of cryptographically random refresh
// material encoded without padding for safe cookie transport.
func NewRefreshSecret() (string, error) {
	secret := make([]byte, 64)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate refresh secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(secret), nil
}

// RefreshHMAC returns the database-safe HMAC-SHA256 digest for a refresh
// secret. Callers must never persist or log the plaintext input.
func RefreshHMAC(secret, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte(secret))
	return hex.EncodeToString(mac.Sum(nil))
}

// NewSessionSID returns a UUIDv4-shaped random selector. It identifies a
// session but carries no refresh secret and is never a public resource ID.
func NewSessionSID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16]), nil
}
