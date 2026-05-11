package contextutil

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/google/uuid"
)

// fallbackCounter provides uniqueness when crypto/rand is unavailable.
var fallbackCounter atomic.Uint64

// NewID produces a UUID v7 string (time-ordered, random).
//
// Falls back to a UUID-formatted time+counter string if crypto/rand is
// unavailable. The fallback is sufficient for request tracing — uniqueness
// is preserved by the atomic counter, even though it isn't cryptographically
// random — but should NOT be relied on for tokens, secrets, or any value
// where unguessability matters. Use [NewSecureID] when crypto/rand failure
// must surface as an error rather than silently degrade.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		slog.Warn("uuid.NewV7 failed, using fallback for ID generation", redact.Error(err))
		return fallbackGenerate()
	}
	return id.String()
}

// NewSecureID produces a UUID v7 string and surfaces the error from
// crypto/rand instead of falling back to a time+counter ID. Use this when
// the value will be exposed externally as a token or anywhere the
// unguessability of the random bits is load-bearing — process-restart
// collisions on the time+counter fallback are within reach of any peer
// that can probe both processes' first IDs.
func NewSecureID() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("contextutil: crypto/rand unavailable: %w", err)
	}
	return id.String(), nil
}

// fallbackGenerate produces a UUID-formatted string from time and an atomic counter.
// Not cryptographically random, but sufficient for request tracing when crypto/rand fails.
func fallbackGenerate() string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint64(b[8:], fallbackCounter.Add(1))
	b[6] = (b[6] & 0x0F) | 0x70 // version 7
	b[8] = (b[8] & 0x3F) | 0x80 // variant RFC 4122
	// Format as UUID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
