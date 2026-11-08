package auditlog

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

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

// ErrChainHeadUnknown is returned by [Logger.LogE] and
// [Logger.RecordRotation] when no configured chain key sealed the newest
// event: typically another replica rotated to a key this Logger was not
// given (see [WithNextChainKeys]), otherwise the head was written by
// something else. The event is not appended, since chaining onto that head
// would seal it with a key the chain has already rotated past. Configure
// the head's key, as next or older key, to append again.
var ErrChainHeadUnknown = errors.New("auditlog: the chain head is sealed by no configured chain key")

// ErrAnchorPruned is returned by verification when the anchored event is no
// longer in the chain, a retention watermark is set and no remaining event
// carries a key older than the anchored one: retention most likely removed
// the anchored event together with later ones, and certainly so when
// [Verification.Removed] puts the anchor among the removed events. Every
// remaining event still verified from the watermark on, but the anchor no
// longer proves the tail: without Removed, removing the newest events
// including the anchored one looks the same.
// Verify with the anchor before each retention run so the kept anchor stays
// newer than the retention cut.
var ErrAnchorPruned = errors.New("auditlog: retention removed the anchored event")

var errRemovedWithoutWatermark = errors.New("auditlog: Verification.Removed must not be negative and needs a watermark")

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// HeadReader is implemented by stores that can return the newest event of
// the chain, for operator tooling.
type HeadReader interface {
	Head(ctx context.Context) (Event, bool, error)
}

// Verification anchors a chain verification.
type Verification struct {
	// Watermark is the HMAC of the last event a retention sweep removed
	// ([RetentionRecord.Watermark]); empty requires a genesis head.
	Watermark []byte
	// Removed is how many events retention removed in all, through the
	// watermark ([RetentionRecord.Removed]),
	// kept with the watermark where whoever can write the store cannot
	// change it; it needs a watermark. With it, positions count from the
	// chain's first event ever: [Head.Events] includes the removed events,
	// and an anchor must sit at its position past a watermark too.
	Removed int
	// PrunedBefore is the newest time retention could have removed events
	// written before: the earlier of the cut it recorded with the watermark
	// and now minus the retention period. With it, an anchor that is gone
	// counts as pruned only when its [Head.At] is before PrunedBefore; an
	// anchor without a time, or written since, fails with [ErrChainBroken],
	// so a sweep within the retention period, from a forged or mis-made
	// record, cannot pass as retention. Retention removes only events
	// written before its cut, so past a watermark the first remaining event
	// must not be older than PrunedBefore either. Zero skips the check.
	PrunedBefore time.Time
	// Anchor is the [Head] an earlier verification returned, kept where
	// whoever can write the store cannot change it. The chain must still
	// contain that event, sealed by the key the anchor names and, without a
	// watermark or with Removed, at the same position; no event from it on
	// may carry an older key. So a tail cut or rewritten since fails. When
	// the anchored event is the watermark, retention removed exactly up to
	// it and the anchor stands for the watermark. When retention removed it
	// and later events, verification returns [ErrAnchorPruned]: with
	// Removed only when the anchor's position is among the removed events,
	// otherwise the tail from it was cut and verification fails with
	// [ErrChainBroken]; without Removed the two look the same. The zero
	// Head skips the check.
	Anchor Head
}

type namedKey struct {
	id  string
	key *secret.String
}

// WithChainKeys rotates the chain key. newest seals every new event, with
// its ID bound into the HMAC; older keys only verify. Before its first event
// under newest the Logger writes a rotation marker sealed by newest (see
// [Logger.RecordRotation]). Verification checks each event against the key
// that sealed it and refuses an event sealed by a key older than the latest
// marker, a newer key without its marker, and once a marker exists an event
// without a key ID. Keep a key listed while events it sealed are kept. IDs
// are 1–32 characters of [A-Za-z0-9_-], unique; keys are at least
// [MinChainKeyLen] bytes. [New] panics otherwise.
//
// Replicas sharing a store must not seal with a key older than the chain
// head: give every replica the new key through [WithNextChainKeys] before
// any replica seals with it.
func WithChainKeys(newest ChainKey, older ...ChainKey) Option {
	return func(a *Logger) {
		for _, k := range append([]ChainKey{newest}, older...) {
			mustBeChainKey(k)
		}
		WithChainKey(newest.Key)(a)
		a.chainKeyID = newest.ID
		a.olderChainKeys = a.olderChainKeys[:0]
		for _, k := range older {
			a.olderChainKeys = append(a.olderChainKeys, namedKey{id: k.ID, key: secret.New(k.Key)})
		}
	}
}

// WithNextChainKeys lists keys newer than the sealing key, newest first:
// keys another replica may already seal with during a rolling rotation. The
// Logger verifies with them, and once the chain head is sealed by one it
// seals its own events with that key as well, so a replica still on the
// previous configuration follows the rotation instead of breaking the chain.
// Without the key such a replica refuses to append ([ErrChainHeadUnknown]).
// IDs and lengths follow [WithChainKeys]; [New] panics otherwise.
func WithNextChainKeys(next ...ChainKey) Option {
	return func(a *Logger) {
		a.nextChainKeys = a.nextChainKeys[:0]
		for _, k := range next {
			mustBeChainKey(k)
			a.nextChainKeys = append(a.nextChainKeys, namedKey{id: k.ID, key: secret.New(k.Key)})
		}
	}
}

func mustBeChainKey(k ChainKey) {
	if !keyIDPattern.MatchString(k.ID) {
		panic(fmt.Sprintf("auditlog: chain key ID %q must be 1-32 characters of [A-Za-z0-9_-]", k.ID))
	}
	if len(k.Key) < MinChainKeyLen {
		panic(fmt.Sprintf("auditlog: every chain key must be at least %d bytes", MinChainKeyLen))
	}
}

// RecordRotation appends the rotation marker for the sealing chain key
// unless the chain head is already sealed by it or by a newer key from
// [WithNextChainKeys]. LogE writes it when due; call it at startup so the
// rotation is in the chain before anything else is logged. A Logger built
// with [WithChainKey] has no key ID and records nothing.
func (l *Logger) RecordRotation(ctx context.Context) error {
	if l.closed.Load() {
		return ErrLoggerClosed
	}
	if l.chainKeyID == "" {
		return nil
	}
	marker, err := l.prepareEvent(ctx, l.rotationMarker())
	if err != nil {
		return err
	}
	return l.reportAppend(ctx, marker, l.recordRotation(ctx, marker))
}

func (l *Logger) rotationMarker() Event {
	return Event{
		Actor:    RotationActor,
		Action:   RotationAction,
		Resource: rotationResource + l.chainKeyID,
		Status:   StatusSuccess,
	}
}

func (l *Logger) recordRotation(ctx context.Context, marker Event) error {
	err := l.appendSealed(ctx, marker, true)
	if errors.Is(err, errRotationCurrent) {
		return nil
	}
	return err
}

func isRotation(event Event) bool {
	return event.Actor == RotationActor && event.Action == RotationAction
}

// VerifyChainKeys verifies events in append order across a chain-key
// rotation. keys lists the chain keys newest first, as given to
// [WithChainKeys]; a key with an empty ID is the unkeyed [WithChainKey]
// form. The returned [Head] is the next anchor to keep.
func VerifyChainKeys(events []Event, keys []ChainKey, v Verification) (Head, error) {
	verifier, err := newChainVerifier(keys, 0, v)
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
	prevAt    time.Time
	last      int // index of the key that sealed the newest event
	oldest    int // index of the oldest key any walked event carries
	sealing   int // index of the key that seals the next event
	index     int
	// observe, when set, sees each event once it verified.
	observe func(Event)
}

func newChainVerifier(keys []ChainKey, sealing int, v Verification) (*chainVerifier, error) {
	if len(keys) == 0 {
		return nil, ErrNoChainKey
	}
	for _, k := range keys {
		if len(k.Key) < MinChainKeyLen {
			return nil, fmt.Errorf("%w: chain key must be at least %d bytes", ErrNoChainKey, MinChainKeyLen)
		}
	}
	if v.Removed < 0 || (v.Removed > 0 && len(v.Watermark) == 0) {
		return nil, errRemovedWithoutWatermark
	}
	c := &chainVerifier{keys: keys, sealing: sealing, v: v, floor: len(keys) - 1, anchorKey: -1, last: -1, oldest: -1}
	if len(v.Anchor.HMAC) == 0 {
		return c, nil
	}
	for i, k := range keys {
		if k.ID == v.Anchor.KeyID {
			c.anchorKey = i
			// Retention may have removed the anchored event; every event
			// walked then chains from it and still owes it the anchored key.
			c.anchored = constantTimeEqualHMAC(v.Anchor.HMAC, v.Watermark)
			if c.anchored && v.Removed > 0 && v.Anchor.Events != v.Removed {
				return nil, fmt.Errorf("%w: the anchored event is the watermark, event[%d], not event[%d]", ErrChainBroken, v.Removed-1, v.Anchor.Events-1)
			}
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
	if i == 0 && len(c.v.Watermark) > 0 && event.Timestamp.Before(c.v.PrunedBefore) {
		return fmt.Errorf("%w: event[0] was written before the retention cut", ErrChainBroken)
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
	c.oldest = max(c.oldest, sealer)
	c.prev = event.HMAC
	// The latest time so far, not the newest event's own: a backdated event
	// cannot make the head look older than what was written before it.
	if event.Timestamp.After(c.prevAt) {
		c.prevAt = event.Timestamp
	}
	c.index++
	if c.observe != nil {
		c.observe(event)
	}
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
	if at := c.v.Removed + i; (len(c.v.Watermark) == 0 || c.v.Removed > 0) && at != c.v.Anchor.Events-1 {
		return fmt.Errorf("%w: the anchored event is event[%d], not event[%d]", ErrChainBroken, at, c.v.Anchor.Events-1)
	}
	c.anchored = true
	return nil
}

// sealer is the index of the key whose HMAC the event carries, and whether
// that HMAC binds the key ID; -1 when no listed key sealed it.
func (c *chainVerifier) sealer(event Event) (int, bool) {
	canonical := eventWithoutHMAC(event)
	for i, k := range c.keys {
		if matched, keyed := sealedBy(k, canonical, event.HMAC); matched {
			return i, keyed
		}
	}
	return -1, false
}

func (c *chainVerifier) finish() (Head, error) {
	if c.anchorKey >= 0 && !c.anchored {
		// Past a watermark the anchored event may be gone to retention; an
		// event older than the anchored key still proves a rewritten tail,
		// and so does an anchor past the events retention removed.
		removed := c.v.Removed == 0 || c.v.Anchor.Events <= c.v.Removed
		if len(c.v.Watermark) > 0 && c.oldest <= c.anchorKey && removed {
			if !c.v.PrunedBefore.IsZero() && (c.v.Anchor.At.IsZero() || !c.v.Anchor.At.Before(c.v.PrunedBefore)) {
				return Head{}, fmt.Errorf("%w: the anchored event is gone, and no retention can have removed it", ErrChainBroken)
			}
			return Head{}, fmt.Errorf("%w: the anchored event is before the watermark; re-anchor", ErrAnchorPruned)
		}
		return Head{}, fmt.Errorf("%w: the anchored head is no longer in the chain: its tail was truncated or rewritten", ErrChainBroken)
	}
	head := Head{HMAC: c.prev, Events: c.v.Removed + c.index, At: c.prevAt}
	if c.index == 0 {
		// The sealing key seals the next event, so an anchor kept from an
		// empty chain still refuses an older one after it.
		head.HMAC, head.KeyID = c.v.Watermark, c.keys[c.sealing].ID
	} else {
		head.KeyID = c.keys[c.last].ID
	}
	return head, nil
}
