package auditlog

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"
)

// MinRetentionKeyLen is the minimum length of the key that seals the
// [RetentionRecord].
const MinRetentionKeyLen = 32

var (
	// ErrRecordUnsealed is returned for a retention record whose seal does
	// not hold under the retention key: written or changed by something
	// other than a sweep that held the key.
	ErrRecordUnsealed = errors.New("auditlog: the retention record does not carry the retention key's seal")
	// ErrRetentionTooShort refuses a sweep with a retention period below
	// the caller's minimum, or a cut whose Before is less than that minimum
	// before now.
	ErrRetentionTooShort = errors.New("auditlog: the retention period is shorter than its minimum")
)

const recordDomain = "auditlog retention record v1\x00"

// RetentionRecord is what the retention sweeps removed in all: the oldest
// Removed events, the newest of them Watermark, every one written before
// PrunedBefore, the latest sweep at PrunedAt. A [RetentionStore] keeps it
// sealed with a retention key next to the chain and replaces it in the
// transaction that removes a cut. The zero record says nothing was
// removed.
type RetentionRecord struct {
	Watermark    []byte
	Removed      int64
	PrunedBefore time.Time
	PrunedAt     time.Time
}

// IsZero reports whether the record says nothing was removed.
func (r RetentionRecord) IsZero() bool {
	return r.Removed == 0 && len(r.Watermark) == 0
}

// Seal is the record's HMAC under key, which must be at least
// [MinRetentionKeyLen] bytes.
func (r RetentionRecord) Seal(key []byte) ([]byte, error) {
	if len(key) < MinRetentionKeyLen {
		return nil, fmt.Errorf("auditlog: the retention key must be at least %d bytes", MinRetentionKeyLen)
	}
	m := hmac.New(sha256.New, key)
	b := []byte(recordDomain)
	b = binary.BigEndian.AppendUint64(b, uint64(r.Removed))
	b = binary.BigEndian.AppendUint64(b, uint64(r.PrunedBefore.UnixMicro()))
	b = binary.BigEndian.AppendUint64(b, uint64(r.PrunedAt.UnixMicro()))
	m.Write(append(b, r.Watermark...))
	return m.Sum(nil), nil
}

// CheckSeal returns [ErrRecordUnsealed] unless seal is the record's seal
// under key and the record is one a sweep writes.
func (r RetentionRecord) CheckSeal(key, seal []byte) error {
	want, err := r.Seal(key)
	if err != nil {
		return err
	}
	if r.Removed <= 0 || len(r.Watermark) == 0 || !hmac.Equal(seal, want) {
		return ErrRecordUnsealed
	}
	return nil
}

// After is the record once c is removed at now: c's watermark, the events
// added to those removed before, the later of the two cuts.
func (r RetentionRecord) After(c Cut, now time.Time) RetentionRecord {
	return RetentionRecord{
		Watermark:    c.Watermark,
		Removed:      r.Removed + c.Events,
		PrunedBefore: later(r.PrunedBefore, c.Before).UTC(),
		PrunedAt:     now.UTC().Truncate(time.Microsecond),
	}
}

// Verification is what verifying the chain needs from the record: its
// watermark and count, and as PrunedBefore the earlier of its cut and now
// minus the longer of retention and minimum, so an anchor written within
// the retention period never counts as pruned. The zero record verifies
// from the chain's genesis.
func (r RetentionRecord) Verification(retention, minimum time.Duration, now time.Time) Verification {
	if r.IsZero() {
		return Verification{}
	}
	prunedBefore := now.Add(-max(retention, minimum))
	if r.PrunedBefore.Before(prunedBefore) {
		prunedBefore = r.PrunedBefore
	}
	return Verification{Watermark: r.Watermark, Removed: int(r.Removed), PrunedBefore: prunedBefore}
}
