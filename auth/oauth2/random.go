package oauth2

import (
	"crypto/rand"
	"encoding/base64"
)

// generateRandomToken returns a 128-bit cryptographically random token
// encoded as URL-safe base64 without padding. Used for state cookies
// + session IDs. The OIDC nonce is supplied by go-oidc helpers.
func generateRandomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
