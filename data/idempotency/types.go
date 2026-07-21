package idempotency

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Store persists and retrieves cached responses keyed by idempotency key.
//
// All methods accept a request fingerprint (typically SHA-256 of the request
// body, or a canonicalised subset of headers + body) so the backend can
// reject reuse of the same idempotency key with a *different* request — the
// standard mitigation against the "client retried with mutated body"
// failure mode that turns idempotent retry into silent data corruption
// (Stripe-style 422 on body mismatch).
//
// Pass nil for fingerprint to disable the comparison. The HTTP middleware
// passes a fingerprint by default for unsafe methods (POST/PUT/PATCH);
// direct callers must opt in to the safety.
type Store interface {
	// Get returns the cached response for the key.
	//
	// Return contract:
	//   - (resp, false, nil)  — cached response found, fingerprint matches
	//                           (or fingerprint argument is nil)
	//   - (nil,  false, nil)  — no cached response
	//   - (nil,  true,  nil)  — cached response exists but its fingerprint
	//                           differs from the supplied one. Caller MUST
	//                           treat this as 422 Unprocessable Entity.
	//   - (nil,  false, err)  — backend error
	Get(ctx context.Context, key string, fingerprint []byte) (*CachedResponse, bool, error)

	// TryLock attempts to acquire a processing lock for the key.
	//
	// Return contract:
	//   - (token, false, true,  nil) — lock acquired; caller MUST pass token
	//                                  to Set / Unlock
	//   - ("",    false, false, nil) — lock held by a concurrent processor with
	//                                  the *same* fingerprint (or fingerprint
	//                                  comparison disabled). Caller should
	//                                  treat this as 409 Conflict.
	//   - ("",    true,  false, nil) — key holds a lock or cached response with
	//                                  a *different* fingerprint. Caller MUST
	//                                  treat this as 422 Unprocessable Entity.
	//   - ("",    false, false, err) — backend error
	//
	// ttl MUST be positive; backends return [ErrInvalidTTL] for ttl <= 0
	// instead of silently disagreeing about the meaning of zero (Redis would
	// otherwise create a permanent lock, MemoryStore would treat it as
	// instantly expired, pgstore would round to zero seconds).
	TryLock(ctx context.Context, key string, fingerprint []byte, ttl time.Duration) (token string, fingerprintMismatch bool, ok bool, err error)

	// Set stores the response, atomically replacing the lock row. The token
	// must be the one returned from the TryLock that started this critical
	// section. Returns [ErrLockLost] if the caller's token no longer matches
	// the current lock owner — a sign the TTL expired mid-handler and another
	// caller has already taken the slot. Returns [ErrInvalidTTL] for ttl <= 0.
	Set(ctx context.Context, key, token string, resp CachedResponse, ttl time.Duration) error

	// Unlock releases the processing lock for the caller's token. No-ops
	// safely if the lock has already expired or been released. Returns nil
	// (NOT ErrLockLost) on token mismatch — Unlock is a best-effort cleanup
	// path (e.g. on handler panic) and should not surface lock-loss to the
	// caller; the cached response was either already written or will not be.
	Unlock(ctx context.Context, key, token string) error
}

// CachedResponse stores the HTTP response data for replay.
type CachedResponse struct {
	StatusCode int                 `json:"status_code"`
	Headers    map[string][]string `json:"headers"`
	Body       []byte              `json:"body"`
}

const (
	// MaxCachedBodyBytes matches the HTTP middleware's capture limit. Direct
	// Store callers get the same safe default instead of persisting unbounded
	// response bodies into Redis, Postgres, or memory.
	MaxCachedBodyBytes = 1 << 20

	// MaxCachedHeaders bounds the number of distinct replayed response headers.
	MaxCachedHeaders = 64

	// MaxCachedHeaderValues bounds repeated values for a single response header.
	MaxCachedHeaderValues = 64

	// MaxCachedHeaderNameBytes caps each response header field name.
	MaxCachedHeaderNameBytes = 128

	// MaxCachedHeaderValueBytes caps each response header value.
	MaxCachedHeaderValueBytes = 8 * 1024

	// MaxCachedHeadersBytes caps the sum of all header name+value
	// bytes so a huge multi-header set cannot pass per-field caps
	// while still being ~32 MiB when serialized.
	MaxCachedHeadersBytes = 64 * 1024
)

// ValidateCachedResponse checks that resp can be safely stored and replayed as
// an HTTP response. Backends call this on Set and Get so direct Store callers
// and corrupted backend rows fail closed instead of replaying invalid status
// codes, header names, header values, or unbounded bodies.
func ValidateCachedResponse(resp CachedResponse) error {
	if resp.StatusCode < 100 || resp.StatusCode > 999 {
		return fmt.Errorf("%w: status code must be between 100 and 999", ErrInvalidCachedResponse)
	}
	if len(resp.Body) > MaxCachedBodyBytes {
		return fmt.Errorf("%w: body exceeds maximum length", ErrInvalidCachedResponse)
	}
	if len(resp.Headers) > MaxCachedHeaders {
		return fmt.Errorf("%w: header count exceeds maximum", ErrInvalidCachedResponse)
	}
	totalHeaderBytes := 0
	for name, values := range resp.Headers {
		if err := validateCachedHeaderName(name); err != nil {
			return err
		}
		totalHeaderBytes += len(name)
		if len(values) > MaxCachedHeaderValues {
			return fmt.Errorf("%w: header value count exceeds maximum", ErrInvalidCachedResponse)
		}
		for _, value := range values {
			if err := validateCachedHeaderValue(value); err != nil {
				return err
			}
			totalHeaderBytes += len(value)
		}
	}
	if totalHeaderBytes > MaxCachedHeadersBytes {
		return fmt.Errorf("%w: total header size exceeds maximum", ErrInvalidCachedResponse)
	}
	return nil
}

func validateCachedHeaderName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: header name must not be empty", ErrInvalidCachedResponse)
	}
	if len(name) > MaxCachedHeaderNameBytes {
		return fmt.Errorf("%w: header name exceeds maximum length", ErrInvalidCachedResponse)
	}
	for i := 0; i < len(name); i++ {
		if !isCachedHeaderNameByte(name[i]) {
			return fmt.Errorf("%w: header name contains invalid character", ErrInvalidCachedResponse)
		}
	}
	return nil
}

func validateCachedHeaderValue(value string) error {
	if len(value) > MaxCachedHeaderValueBytes {
		return fmt.Errorf("%w: header value exceeds maximum length", ErrInvalidCachedResponse)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: header value contains invalid UTF-8", ErrInvalidCachedResponse)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: header value contains control character", ErrInvalidCachedResponse)
		}
	}
	return nil
}

func isCachedHeaderNameByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z':
		return true
	case 'A' <= c && c <= 'Z':
		return true
	case '0' <= c && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}

// GenerateToken returns a 32-character hex-encoded random token. Backends use
// this for the owner-token of an acquired lock; the middleware does not
// inspect tokens itself — it just round-trips them between TryLock and
// Set/Unlock.
func GenerateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := io.ReadFull(tokenRandReader, b); err != nil {
		return "", redact.WrapError("idempotency: generate lock token", err)
	}
	return hex.EncodeToString(b), nil
}

// memoryStoreMaxEntries is the size threshold at which TryLock forces a
// lazy lock-eviction pass instead of waiting for the periodic interval.
// Set relies only on the periodic evictInterval + [MemoryStore.Run] so a
// live-heavy working set does not pay a fruitless scan under the write
// lock on every write (review-12). Eviction only reclaims *expired*
// entries, so a working set of live long-TTL entries can still grow past
// this number. Operators with high live-key cardinality should run
// [MemoryStore.Run] and rely on a real backend ([pgstore]/[redisstore]) in
// production, where TTL expiry caps memory at the datastore.
const memoryStoreMaxEntries = 10_000
