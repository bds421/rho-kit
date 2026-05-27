package oauth2

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// verifyIDToken performs the kit's minimum ID-token check: split-on-dot,
// decode payload, ensure nonce matches and sub is present. Full
// signature verification (algorithm pinning, JWKS rotation) is left to
// callers wanting cryptographic guarantees — they should plug
// security/jwtutil.Provider in front of this client and re-verify the
// id_token before trusting it. The state/nonce CSRF guard plus TLS to
// the token endpoint is the kit's default trust model.
//
// We do NOT do full signature verification here because the token
// arrived directly from a TLS-protected token endpoint that the client
// authenticated against, so transport security covers tamper-resistance
// for this code path. Callers that DON'T trust their network (defence
// in depth) should use security/jwtutil to verify against the issuer's
// JWKS.
func verifyIDToken(idToken, expectedNonce string) error {
	if idToken == "" {
		return fmt.Errorf("%w: empty id_token", ErrCodeExchange)
	}
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return fmt.Errorf("%w: id_token not three parts", ErrCodeExchange)
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("%w: id_token payload decode: %v", ErrCodeExchange, err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return fmt.Errorf("%w: id_token payload unmarshal: %v", ErrCodeExchange, err)
	}
	gotNonce, _ := claims["nonce"].(string)
	if gotNonce != expectedNonce {
		return ErrNonceMismatch
	}
	if sub, _ := claims["sub"].(string); sub == "" {
		return fmt.Errorf("%w: id_token missing sub", ErrCodeExchange)
	}
	return nil
}

// extractSubFromIDToken parses just the sub claim without verifying
// (verifyIDToken was already called).
func extractSubFromIDToken(idToken string) string {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	_ = json.Unmarshal(payload, &claims)
	sub, _ := claims["sub"].(string)
	return sub
}

// extractClaims returns the full claim set from an already-validated
// id_token. Callers should NOT trust these claims without first
// running verifyIDToken (the kit's session-write path does so).
func extractClaims(idToken string) map[string]any {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var claims map[string]any
	_ = json.Unmarshal(payload, &claims)
	return claims
}
