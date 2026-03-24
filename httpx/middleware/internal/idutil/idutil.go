// Package idutil provides shared ID generation and validation helpers
// for the requestid and correlationid middleware packages.
package idutil

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

// fallbackCounter provides uniqueness when crypto/rand is unavailable.
var fallbackCounter atomic.Uint64

// Generate produces a UUID v7 string (time-ordered, random).
// Falls back to a UUID-formatted time+counter string if crypto/rand is unavailable.
func Generate() string {
	id, err := uuid.NewV7()
	if err != nil {
		slog.Warn("uuid.NewV7 failed, using fallback for ID generation", "error", err)
		return fallbackGenerate()
	}
	return id.String()
}

// fallbackGenerate produces a UUID-formatted string from time and an atomic counter.
// Not cryptographically random, but sufficient for request tracing when crypto/rand fails.
func fallbackGenerate() string {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint64(b[8:], fallbackCounter.Add(1))
	// Format as UUID: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// IsValid returns true if id is non-empty, within the given maxLen,
// and contains only printable ASCII characters excluding space (0x21-0x7E).
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
