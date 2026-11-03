package auditlog

import (
	"context"
	"fmt"
	"regexp"

	"github.com/bds421/rho-kit/core/v2/secret"
)

// ChainKey is a chain HMAC key and the ID it is known by. The ID is bound
// into every HMAC the key seals and named by the rotation marker, so it must
// stay the same for the key's lifetime.
type ChainKey struct {
	ID  string
	Key []byte
}

// Rotation markers are the events a [Logger] appends when it starts sealing
// with a key the chain head was not sealed with. The actor and action are
// reserved: [Logger.LogE] refuses caller events that use the action.
const (
	RotationActor    = "auditlog"
	RotationAction   = "auditlog.chain_key_rotated"
	rotationResource = "chain-keys/"
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// HeadReader is implemented by stores that can return the newest event of
// the chain. With it a [Logger] writes a rotation marker only when the head
// was sealed by another key; without it, once per Logger.
type HeadReader interface {
	Head(ctx context.Context) (Event, bool, error)
}

// Verification anchors a chain verification.
type Verification struct {
	// Watermark is the HMAC of the last event a retention sweep removed
	// (see [RetentionJob]); empty requires a genesis head.
	Watermark []byte
	// Anchor is the [Head] an earlier verification returned, kept where
	// whoever can write the store cannot change it. The chain must still
	// contain that event, sealed by the key the anchor names and, without a
	// watermark, at the same position; no event from it on may carry an
	// older key. So a tail cut or rewritten since fails. The zero Head
	// skips the check.
	Anchor Head
}

// Head is the newest event a successful verification walked; keep all of
// it as the next [Verification.Anchor].
type Head struct {
	// HMAC is the newest event's HMAC: the watermark when no event follows
	// it, and empty for an empty chain.
	HMAC []byte
	// Events is the number of events verified, so the newest event's
	// position.
	Events int
	// KeyID names the chain key that sealed the newest event, or the newest
	// listed key when no event was walked; empty for the unkeyed
	// [WithChainKey] key.
	KeyID string
}

type namedKey struct {
	id  string
	key *secret.String
}

// WithChainKeys rotates the chain key. newest seals every new event, with
// its ID bound into the HMAC; older keys only verify. On its first append
// the Logger writes a rotation marker sealed by newest (see
// [Logger.RecordRotation]). Verification checks each event against the key
// that sealed it and refuses an event sealed by a key older than the latest
// marker, a newer key without its marker, and once a marker exists an event
// without a key ID. Keep a key listed while events it sealed are kept. IDs
// are 1–32 characters of [A-Za-z0-9_-], unique; keys are at least
// [MinChainKeyLen] bytes. [New] panics otherwise.
func WithChainKeys(newest ChainKey, older ...ChainKey) Option {
	return func(a *Logger) {
		seen := map[string]bool{}
		for _, k := range append([]ChainKey{newest}, older...) {
			if !keyIDPattern.MatchString(k.ID) || seen[k.ID] {
				panic(fmt.Sprintf("auditlog: chain key ID %q must be unique and 1-32 characters of [A-Za-z0-9_-]", k.ID))
			}
			if len(k.Key) < MinChainKeyLen {
				panic(fmt.Sprintf("auditlog: every chain key must be at least %d bytes", MinChainKeyLen))
			}
			seen[k.ID] = true
		}
		WithChainKey(newest.Key)(a)
		a.chainKeyID = newest.ID
		a.olderChainKeys = a.olderChainKeys[:0]
		for _, k := range older {
			a.olderChainKeys = append(a.olderChainKeys, namedKey{id: k.ID, key: secret.New(k.Key)})
		}
	}
}

// RecordRotation appends the rotation marker for the newest chain key
// unless the chain head is already sealed by it. LogE calls it before the
// first event; call it at startup so the rotation is in the chain before
// anything else is logged. A Logger built with [WithChainKey] has no key ID
// and records nothing.
func (l *Logger) RecordRotation(ctx context.Context) error {
	if l.chainKeyID == "" || l.rotationRecorded.Load() {
		return nil
	}
	l.rotationMu.Lock()
	defer l.rotationMu.Unlock()
	if l.rotationRecorded.Load() {
		return nil
	}
	current, err := l.headSealedByNewest(ctx)
	if err != nil {
		return err
	}
	if !current {
		marker := Event{
			Actor:    RotationActor,
			Action:   RotationAction,
			Resource: rotationResource + l.chainKeyID,
			Status:   "success",
		}
		if err := l.appendEvent(ctx, marker); err != nil {
			return err
		}
	}
	l.rotationRecorded.Store(true)
	return nil
}

func (l *Logger) headSealedByNewest(ctx context.Context) (bool, error) {
	reader, ok := l.store.(HeadReader)
	if !ok {
		return false, nil
	}
	head, found, err := reader.Head(ctx)
	if err != nil || !found {
		return false, err
	}
	var match bool
	l.chainKey.Use(func(k []byte) {
		match = len(k) > 0 && constantTimeEqualHMAC(head.HMAC, computeKeyedHMAC(k, l.chainKeyID, eventWithoutHMAC(head)))
	})
	return match, nil
}

func isRotation(event Event) bool {
	return event.Actor == RotationActor && event.Action == RotationAction
}

// VerifyChainKeys verifies events in append order across a chain-key
// rotation. keys lists the chain keys newest first, as given to
// [WithChainKeys]; a key with an empty ID is the unkeyed [WithChainKey]
// form. The returned [Head] is the next anchor to keep.
func VerifyChainKeys(events []Event, keys []ChainKey, v Verification) (Head, error) {
	verifier, err := newChainVerifier(keys, v)
	if err != nil {
		return Head{}, err
	}
	for _, event := range events {
		if err := verifier.step(event); err != nil {
			return Head{}, err
		}
	}
	return verifier.finish()
}

type chainVerifier struct {
	keys     []ChainKey
	v        Verification
	floor    int // index of the oldest key the next event may carry
	rotated  bool
	anchored bool
	// anchorKey indexes the anchored key; -1 without an anchor.
	anchorKey int
	prev      []byte
	last      int // index of the key that sealed the newest event
	index     int
}

func newChainVerifier(keys []ChainKey, v Verification) (*chainVerifier, error) {
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no chain key", ErrChainBroken)
	}
	for _, k := range keys {
		if len(k.Key) < MinChainKeyLen {
			return nil, fmt.Errorf("%w: chain key must be at least %d bytes", ErrChainBroken, MinChainKeyLen)
		}
	}
	c := &chainVerifier{keys: keys, v: v, floor: len(keys) - 1, anchorKey: -1, last: -1}
	if len(v.Anchor.HMAC) == 0 {
		return c, nil
	}
	for i, k := range keys {
		if k.ID == v.Anchor.KeyID {
			c.anchorKey = i
			// Retention may have removed the anchored event; every event
			// walked then chains from it and still owes it the anchored key.
			c.anchored = constantTimeEqualHMAC(v.Anchor.HMAC, v.Watermark)
			return c, nil
		}
	}
	return nil, fmt.Errorf("%w: the anchored chain key %q is not listed", ErrChainBroken, v.Anchor.KeyID)
}

func (c *chainVerifier) step(event Event) error {
	i := c.index
	if i == 0 {
		if len(c.v.Watermark) == 0 {
			if len(event.PrevHMAC) != 0 && !isZeroBytes(event.PrevHMAC) {
				return fmt.Errorf("%w: event[0] PrevHMAC must be empty or zero", ErrChainBroken)
			}
		} else if !constantTimeEqualHMAC(event.PrevHMAC, c.v.Watermark) {
			return fmt.Errorf("%w: event[0] PrevHMAC does not match retention watermark", ErrChainBroken)
		}
	} else if !constantTimeEqualHMAC(event.PrevHMAC, c.prev) {
		return fmt.Errorf("%w: event[%d] PrevHMAC does not match event[%d] HMAC", ErrChainBroken, i, i-1)
	}
	sealer, keyed := c.sealer(event)
	if sealer < 0 {
		return fmt.Errorf("%w: event[%d] HMAC does not match canonical content", ErrChainBroken, i)
	}
	id := c.keys[sealer].ID
	if err := c.checkAnchor(event, sealer); err != nil {
		return err
	}
	switch {
	case sealer > c.floor:
		return fmt.Errorf("%w: event[%d] is sealed by chain key %q, older than the rotation before it", ErrChainBroken, i, id)
	case c.rotated && !keyed:
		return fmt.Errorf("%w: event[%d] does not bind its chain key ID after a rotation", ErrChainBroken, i)
	case isRotation(event):
		if !keyed || event.Resource != rotationResource+id {
			return fmt.Errorf("%w: event[%d] rotation marker is not sealed by the key it names", ErrChainBroken, i)
		}
		c.rotated = true
	case i > 0 && sealer < c.floor:
		return fmt.Errorf("%w: event[%d] is sealed by chain key %q without its rotation marker", ErrChainBroken, i, id)
	}
	c.floor, c.last = sealer, sealer
	c.prev = event.HMAC
	c.index++
	return nil
}

// checkAnchor holds the anchored event to the anchored key and position,
// and every event from it on to that key or a newer one.
func (c *chainVerifier) checkAnchor(event Event, sealer int) error {
	i := c.index
	if c.anchored {
		if sealer > c.anchorKey {
			return fmt.Errorf("%w: event[%d] is sealed by chain key %q, older than the anchored key %q", ErrChainBroken, i, c.keys[sealer].ID, c.v.Anchor.KeyID)
		}
		return nil
	}
	if c.anchorKey < 0 || !constantTimeEqualHMAC(event.HMAC, c.v.Anchor.HMAC) {
		return nil
	}
	if sealer != c.anchorKey {
		return fmt.Errorf("%w: the anchored event[%d] is sealed by chain key %q, not the anchored key %q", ErrChainBroken, i, c.keys[sealer].ID, c.v.Anchor.KeyID)
	}
	if len(c.v.Watermark) == 0 && i != c.v.Anchor.Events-1 {
		return fmt.Errorf("%w: the anchored event is event[%d], not event[%d]", ErrChainBroken, i, c.v.Anchor.Events-1)
	}
	c.anchored = true
	return nil
}

// sealer is the index of the key whose HMAC the event carries, and whether
// that HMAC binds the key ID; -1 when no listed key sealed it.
func (c *chainVerifier) sealer(event Event) (int, bool) {
	canonical := eventWithoutHMAC(event)
	for i, k := range c.keys {
		if k.ID != "" && constantTimeEqualHMAC(event.HMAC, computeKeyedHMAC(k.Key, k.ID, canonical)) {
			return i, true
		}
		if constantTimeEqualHMAC(event.HMAC, computeHMAC(k.Key, canonical)) {
			return i, false
		}
	}
	return -1, false
}

func (c *chainVerifier) finish() (Head, error) {
	if c.anchorKey >= 0 && !c.anchored {
		return Head{}, fmt.Errorf("%w: the anchored head is no longer in the chain: its tail was truncated or rewritten", ErrChainBroken)
	}
	head := Head{HMAC: c.prev, Events: c.index}
	if c.index == 0 {
		// The newest key seals the next event, so an anchor kept from an
		// empty chain still refuses an older one after it.
		head.HMAC, head.KeyID = c.v.Watermark, c.keys[0].ID
	} else {
		head.KeyID = c.keys[c.last].ID
	}
	return head, nil
}
