package oauth2

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// generateRandomToken returns a 128-bit cryptographically random token
// encoded as URL-safe base64 without padding. Used for state and nonce.
func generateRandomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// generateCodeVerifier returns a 256-bit cryptographically random
// code verifier per RFC 7636 §4.1.
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// codeChallengeS256 returns the SHA-256 challenge derived from
// verifier per RFC 7636 §4.2 (method=S256).
func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
