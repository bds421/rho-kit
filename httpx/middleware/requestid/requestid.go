package requestid

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/httpx"
)

// fallbackCounter provides uniqueness when crypto/rand is unavailable.
// Using atomic.Uint64 (instead of plain uint64 + atomic.AddUint64) makes
// the atomic intent self-documenting and prevents accidental non-atomic reads.
var fallbackCounter atomic.Uint64

// maxRequestIDLen is the maximum length for an incoming X-Request-Id header.
const maxRequestIDLen = 128

// WithRequestID ensures every request has a unique identifier.
// It uses the incoming X-Request-Id header if present and valid, otherwise
// generates one. The ID is set on the response header and stored in the context.
func WithRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !isValidRequestID(id) {
			id = generateID()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := httpx.SetRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isValidRequestID returns true if id is non-empty, within length limits,
// and contains only printable ASCII characters.
func isValidRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for _, c := range id {
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}

// generateID produces a 32-character hex string from 16 random bytes.
// Falls back to time+counter if crypto/rand is unavailable.
func generateID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: time-based ID with atomic counter for uniqueness.
		// Not cryptographically random, but sufficient for request tracing.
		// Log the failure so operators can investigate entropy exhaustion.
		slog.Warn("crypto/rand failed, using fallback for request ID", "error", err)
		binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(b[8:], fallbackCounter.Add(1))
	}
	return hex.EncodeToString(b)
}
