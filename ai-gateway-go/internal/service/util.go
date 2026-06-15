package service

import (
	"crypto/rand"
	"encoding/hex"
)

func newOrderNo() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ORD" + stringsToUpper(hex.EncodeToString(b))
}

func stringsToUpper(s string) string {
	out := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'f' {
			c -= 32
		}
		out[i] = c
	}
	return string(out)
}
