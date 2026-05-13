// asvs: V7.1.1, V7.4.1, V11.1.4
package auditlog

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// HMACSize is the byte length of a chain HMAC. HMAC-SHA256 → 32 bytes.
const HMACSize = sha256.Size

// MinChainKeyLen is the minimum acceptable length of a chain HMAC key.
// 32 bytes matches HMAC-SHA256's block-cipher output and the kit's other
// HMAC consumers (signed requests, cursor signers).
const MinChainKeyLen = 32

// MinCursorKeyLen is the minimum acceptable length of a cursor HMAC key.
const MinCursorKeyLen = 32

// ErrChainBroken is returned by [VerifyChain] when a record's stored HMAC
// does not match the HMAC recomputed from its content and the previous
// record's HMAC. Wrap-aware: callers can errors.Is(err, ErrChainBroken)
// to distinguish tamper detection from I/O failures.
var ErrChainBroken = errors.New("auditlog: chain integrity check failed")

// canonicalEvent serialises an [Event] to a deterministic byte sequence
// suitable for HMAC computation. The output:
//   - excludes the [Event.HMAC] field itself (the HMAC is computed over
//     this canonical form),
//   - includes the previous record's HMAC as a length-prefixed field so
//     two events with identical content but different positions in the
//     chain produce different HMACs,
//   - serialises each field as `uint32 length || bytes` so no field
//     boundary can be confused with another field's payload (length
//     prefixing prevents e.g. an actor "alice\x00action=approve"
//     colliding with actor="alice", action="approve"),
//   - encodes the timestamp as a fixed-width 8-byte UnixNano,
//   - covers the entire wire-relevant content of the event.
//
// This encoding is part of the on-disk contract: changing it invalidates
// every existing chain. Bump a CHANGES.md entry and document a migration
// path if the format ever has to change.
func canonicalEvent(prevHMAC []byte, event Event) []byte {
	// Estimate the buffer size to avoid reallocs on the hot path.
	approx := HMACSize +
		len(event.ID) + len(event.Actor) + len(event.Action) +
		len(event.Resource) + len(event.Status) + len(event.IPAddress) +
		len(event.TraceID) + len(event.Metadata) + len(event.PrevHMAC) +
		11*4 + 8 // length headers + timestamp
	buf := make([]byte, 0, approx)
	buf = appendLenPrefixed(buf, prevHMAC)
	buf = appendLenPrefixed(buf, []byte(event.ID))
	buf = appendUnixNano(buf, event.Timestamp)
	buf = appendLenPrefixed(buf, []byte(event.Actor))
	buf = appendLenPrefixed(buf, []byte(event.Action))
	buf = appendLenPrefixed(buf, []byte(event.Resource))
	buf = appendLenPrefixed(buf, []byte(event.Status))
	buf = appendLenPrefixed(buf, []byte(event.IPAddress))
	buf = appendLenPrefixed(buf, []byte(event.TraceID))
	buf = appendLenPrefixed(buf, event.Metadata)
	buf = appendLenPrefixed(buf, event.PrevHMAC)
	return buf
}

func appendLenPrefixed(dst, payload []byte) []byte {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	dst = append(dst, header[:]...)
	dst = append(dst, payload...)
	return dst
}

func appendUnixNano(dst []byte, t time.Time) []byte {
	var header [8]byte
	binary.BigEndian.PutUint64(header[:], uint64(t.UnixNano()))
	return append(dst, header[:]...)
}

// computeHMAC returns the HMAC-SHA256 of canonicalEvent(prevHMAC, event)
// keyed by chainKey. The returned slice is freshly allocated so callers
// may store it without aliasing.
func computeHMAC(chainKey []byte, prevHMAC []byte, event Event) []byte {
	mac := hmac.New(sha256.New, chainKey)
	mac.Write(canonicalEvent(prevHMAC, event))
	return mac.Sum(nil)
}

// constantTimeEqualHMAC compares two HMAC values in constant time. Returns
// true iff the two slices are equal-length and bytewise identical.
func constantTimeEqualHMAC(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare(a, b) == 1
}

// VerifyChain validates the tamper-evident HMAC chain over the supplied
// events. Events must be supplied in chain order (oldest first — the same
// order they were appended in). For each event i:
//
//  1. The recomputed HMAC over canonical(prevHMAC, event-without-HMAC)
//     must equal event[i].HMAC.
//  2. event[i].PrevHMAC must equal event[i-1].HMAC (for i > 0). The first
//     event's PrevHMAC must be empty or all-zero.
//
// Any deviation yields a wrapped [ErrChainBroken] with the offending
// index identified in the error message; callers can use
// errors.Is(err, ErrChainBroken) for typed handling.
//
// VerifyChain treats an empty slice as a valid (degenerate) chain.
// chainKey must match the key used at [Logger.LogE] time; the comparison
// is constant-time so attackers cannot probe key bytes via timing.
func VerifyChain(events []Event, chainKey []byte) error {
	if len(chainKey) < MinChainKeyLen {
		return fmt.Errorf("%w: chain key must be at least %d bytes", ErrChainBroken, MinChainKeyLen)
	}
	var prev []byte
	for i, event := range events {
		if i == 0 {
			if len(event.PrevHMAC) != 0 && !isZeroBytes(event.PrevHMAC) {
				return fmt.Errorf("%w: event[0] PrevHMAC must be empty or zero", ErrChainBroken)
			}
		} else {
			if !constantTimeEqualHMAC(event.PrevHMAC, prev) {
				return fmt.Errorf("%w: event[%d] PrevHMAC does not match event[%d] HMAC", ErrChainBroken, i, i-1)
			}
		}
		expected := computeHMAC(chainKey, event.PrevHMAC, eventWithoutHMAC(event))
		if !constantTimeEqualHMAC(event.HMAC, expected) {
			return fmt.Errorf("%w: event[%d] HMAC does not match canonical content", ErrChainBroken, i)
		}
		prev = event.HMAC
	}
	return nil
}

// eventWithoutHMAC returns a copy of event with the HMAC field cleared.
// The canonical encoding deliberately excludes the HMAC field itself,
// since that is the value being computed.
func eventWithoutHMAC(event Event) Event {
	event.HMAC = nil
	return event
}

func isZeroBytes(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
