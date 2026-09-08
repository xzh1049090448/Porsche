package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/argon2"
)

const (
	argon2Time    uint32 = 3
	argon2Memory  uint32 = 64 * 1024
	argon2Threads uint8  = 4
	argon2KeyLen  uint32 = 32
	argon2SaltLen        = 16
)

// HashPassword derives a versioned Argon2id password hash. Only the encoded
// result is persistable; callers must never log either input or output.
func HashPassword(password string) (string, error) {
	encoded, err := HashPasswordBytes([]byte(password))
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

// HashPasswordBytes derives the same project encoding without converting the
// caller-owned plaintext into a Go string. The input is never mutated.
func HashPasswordBytes(password []byte) ([]byte, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate password salt: %w", err)
	}
	defer clear(salt)
	derived := argon2.IDKey(password, salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	defer clear(derived)
	encoded := make([]byte, 0, 96)
	encoded = append(encoded, "$argon2id$v=19$m=65536,t=3,p=4$"...)
	encoded = base64.RawStdEncoding.AppendEncode(encoded, salt)
	encoded = append(encoded, '$')
	encoded = base64.RawStdEncoding.AppendEncode(encoded, derived)
	return encoded, nil
}

// VerifyPassword verifies only the project Argon2id encoding. Legacy bcrypt
// values are deliberately not accepted by the username authentication path.
func VerifyPassword(plain, hashed string) bool {
	owned := []byte(plain)
	defer clear(owned)
	return VerifyPasswordBytes(owned, hashed)
}

// VerifyPasswordBytes verifies a caller-owned plaintext slice without making a
// plaintext string copy. The caller remains responsible for clearing its
// buffer on every path.
func VerifyPasswordBytes(plain []byte, hashed string) bool {
	parts := strings.Split(hashed, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil || memory != argon2Memory || iterations != argon2Time || threads != argon2Threads {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < argon2SaltLen {
		clear(salt)
		return false
	}
	defer clear(salt)
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != int(argon2KeyLen) {
		clear(expected)
		return false
	}
	defer clear(expected)
	actual := argon2.IDKey(plain, salt, iterations, memory, threads, uint32(len(expected)))
	defer clear(actual)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func CreateAccessToken(subject string, secret string, expireMinutes int, extra map[string]interface{}) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub": subject,
		"iat": now.Unix(),
		"exp": now.Add(time.Duration(expireMinutes) * time.Minute).Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

func DecodeAccessToken(tokenStr, secret string) (jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	}, jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !token.Valid {
		return nil, err
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, jwt.ErrTokenInvalidClaims
	}
	return claims, nil
}
