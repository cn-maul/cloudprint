package auth

import (
	"crypto/rand"
	"encoding/base64"
)

func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("crypto/rand unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func TokenHash(token string) string {
	return HashToken(token)
}

func HashToken(token string) string {
	// Agent tokens are high-entropy random values; SHA-256 is sufficient here.
	return sha256Hex(token)
}
