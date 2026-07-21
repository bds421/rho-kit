package signedrequest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMiddleware_DoesNotZeroCallerOwnedSecret is the regression pin for
// review-08: verify() must copy the resolver-returned key before zeroing.
// A shared long-lived key slice (as in examples/webhook-receiver) must
// still verify the second request.
func TestMiddleware_DoesNotZeroCallerOwnedSecret(t *testing.T) {
	sharedKey := []byte(secretStr)
	keySnapshot := append([]byte(nil), sharedKey...)

	store := NewMemoryNonceStore(10 * time.Minute)
	resolver := func(_ context.Context, id string) ([]byte, error) {
		require.Equal(t, keyID, id)
		return sharedKey, nil // shared, not a fresh copy
	}
	mw := Middleware(resolver, store)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	body := `{"ok":true}`
	now := time.Now()

	for i := 1; i <= 2; i++ {
		req := signRequest(t, http.MethodPost, "/hook", body, now.Add(time.Duration(i)*time.Second), makeNonce("secret-copy-"+string(rune('0'+i))), nil, nil)
		// Re-sign with the snapshot key so we know the expected MAC even if
		// sharedKey were corrupted (the middleware must not corrupt it).
		_ = req
		// signRequest used secretStr constant which aliases sharedKey content.
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusNoContent, rec.Code, "request %d body=%s", i, rec.Body.String())

		// After each request the shared key must be byte-identical.
		assert.Equal(t, keySnapshot, sharedKey, "shared key must not be zeroed after request %d", i)
	}
}
