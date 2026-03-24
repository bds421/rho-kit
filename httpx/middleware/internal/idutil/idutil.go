// Package idutil provides shared ID generation and validation helpers
// for the requestid and correlationid middleware packages.
package idutil

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"log/slog"
	"sync/atomic"
	"time"
)

// fallbackCounter provides uniqueness when crypto/rand is unavailable.
// Using atomic.Uint64 (instead of plain uint64 + atomic.AddUint64) makes
// the atomic intent self-documenting and prevents accidental non-atomic reads.
var fallbackCounter atomic.Uint64

// Generate produces a 32-character hex string from 16 random bytes.
// Falls back to time+counter if crypto/rand is unavailable.
func Generate() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback: time-based ID with atomic counter for uniqueness.
		// Not cryptographically random, but sufficient for request tracing.
		// Log the failure so operators can investigate entropy exhaustion.
		slog.Warn("crypto/rand failed, using fallback for ID generation", "error", err)
		binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(b[8:], fallbackCounter.Add(1))
	}
	return hex.EncodeToString(b)
}

// IsValid returns true if id is non-empty, within the given maxLen,
// and contains only printable ASCII characters excluding space (0x21–0x7E).
// Space (0x20) is excluded because spaces in trace IDs cause log-parsing issues.
func IsValid(id string, maxLen int) bool {
	if id == "" || len(id) > maxLen {
		return false
	}
	for _, c := range id {
		if c <= 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}
