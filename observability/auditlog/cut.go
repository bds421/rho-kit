package auditlog

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"time"
)

// ErrCutRefused is returned for a cut that is not signed by the expected
// key, has expired, is malformed, or no longer names the oldest events of
// the chain.
var ErrCutRefused = errors.New("auditlog: the retention cut is not signed, has expired or no longer matches the chain")

// Cut names what one retention sweep may remove: the oldest Events events
// of the remaining chain, whose HMACs in append order hash to Digest, the
// newest of them Watermark. Each was written, by its HMAC-verified time,
// before Before, and the first event kept was written at or after it. The
// newest event of the chain is never cut, so the chain keeps a head to
// anchor on. A zero Events cuts nothing.
//
// The cut is computed from a verified chain ([ComputeCut],
// [Logger.ComputeCut]) and applied by a [RetentionStore], which removes the
// events only while they are still exactly the ones named. Split the two
// between roles by signing the cut ([Cut.Encode], [DecodeCut]): the role
// that applies it then needs neither the chain key nor a way to make a cut
// of its own.
type Cut struct {
	Events    int64
	Watermark []byte
	Digest    []byte
	Before    time.Time
	IssuedAt  time.Time
}

// ComputeCut streams store in append order through the checks of
// [VerifyStore] with keys and v, and returns the cut of the oldest
// contiguous events written before before, as of now. A chain that does not
// verify yields no cut.
func ComputeCut(ctx context.Context, store Store, keys []ChainKey, v Verification, before, now time.Time) (Cut, error) {
	return computeCut(ctx, store, keys, 0, v, before, now)
}

// ComputeCut is [ComputeCut] over the Logger's store with every configured
// chain key.
func (l *Logger) ComputeCut(ctx context.Context, v Verification, before, now time.Time) (Cut, error) {
	keys, sealing, err := l.chainKeysForVerify()
	if err != nil {
		return Cut{}, err
	}
	defer clearKeys(keys)
	return computeCut(ctx, l.store, keys, sealing, v, before, now)
}

func computeCut(ctx context.Context, store Store, keys []ChainKey, sealing int, v Verification, before, now time.Time) (Cut, error) {
	verifier, err := newChainVerifier(keys, sealing, v)
	if err != nil {
		return Cut{}, err
	}
	cut := newCutter(before)
	verifier.observe = cut.add
	if err := store.RangeChain(ctx, verifier.step); err != nil {
		return Cut{}, err
	}
	if _, err := verifier.finish(); err != nil {
		return Cut{}, err
	}
	return cut.result(now), nil
}

// cutter follows the leading run of events written before the cut while the
// chain is verified, keeping the run's digest with and without its newest
// event: when the run is the whole chain, its newest event stays.
type cutter struct {
	before time.Time
	open   bool
	total  int64
	newest time.Time
	digest hash.Hash
	run    cutRun
	short  cutRun
}

type cutRun struct {
	events    int64
	watermark []byte
	digest    []byte
	latest    time.Time
}

func newCutter(before time.Time) *cutter {
	return &cutter{before: before.UTC().Truncate(time.Microsecond), open: true, digest: sha256.New()}
}

func (c *cutter) add(event Event) {
	c.total++
	c.newest = event.Timestamp
	if !c.open || !event.Timestamp.Before(c.before) {
		c.open = false
		return
	}
	c.short = c.run
	writeCutHMAC(c.digest, event.HMAC)
	c.run = cutRun{
		events:    c.run.events + 1,
		watermark: event.HMAC,
		digest:    c.digest.Sum(nil),
		latest:    later(c.run.latest, event.Timestamp),
	}
}

func (c *cutter) result(now time.Time) Cut {
	cut := Cut{Before: c.before, IssuedAt: now.UTC().Truncate(time.Microsecond)}
	run := c.run
	if run.events == c.total {
		// Every event is older than the cut: all but the newest go, and the
		// cut moves back to the newest's time so the first event kept is not
		// older than it. A backdated newest event keeps them all.
		kept := c.newest.UTC().Truncate(time.Microsecond)
		run = c.short
		if !run.latest.Before(kept) {
			return cut
		}
		cut.Before = kept
	}
	if run.events == 0 {
		return Cut{Before: c.before, IssuedAt: cut.IssuedAt}
	}
	cut.Events, cut.Watermark, cut.Digest = run.events, run.watermark, run.digest
	return cut
}

// Holds reports with [ErrCutRefused] unless hmacs, the HMACs of the oldest
// Events+1 events now in the store in append order, are still the events
// the cut names followed by one that stays. Stores call it with those
// events locked, before removing them.
func (c Cut) Holds(hmacs [][]byte) error {
	if c.wellFormed() != nil || c.Events == 0 || int64(len(hmacs)) != c.Events+1 {
		return ErrCutRefused
	}
	h := sha256.New()
	for _, mac := range hmacs[:c.Events] {
		writeCutHMAC(h, mac)
	}
	if !hmac.Equal(hmacs[c.Events-1], c.Watermark) || !hmac.Equal(h.Sum(nil), c.Digest) {
		return ErrCutRefused
	}
	return nil
}

func writeCutHMAC(h hash.Hash, mac []byte) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(mac)))
	h.Write(n[:])
	h.Write(mac)
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// Removable is how a store checks a cut before it applies it:
// [ErrCutRefused] for a cut no [ComputeCut] makes, [ErrRetentionTooShort]
// when its Before is less than minimum before now.
func (c Cut) Removable(minimum time.Duration, now time.Time) error {
	if err := c.wellFormed(); err != nil {
		return err
	}
	if minimum < 0 || now.Sub(c.Before) < minimum {
		return ErrRetentionTooShort
	}
	return nil
}
