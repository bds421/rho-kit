package auditlog

import "errors"

// errRotationDue aborts an append whose sealing key needs its rotation
// marker first; errRotationCurrent aborts a marker the head makes redundant.
// Neither leaves the Logger.
var (
	errRotationDue     = errors.New("auditlog: rotation marker due")
	errRotationCurrent = errors.New("auditlog: rotation already recorded")
)

// maxRotationAttempts bounds how often an append writes a marker and retries
// when the head keeps falling back to an older key between the two.
const maxRotationAttempts = 3

// keyRing lists every chain key newest first: the next keys, the sealing
// key at index sealing, then the older keys.
func (l *Logger) keyRing() (ring []namedKey, sealing int) {
	ring = make([]namedKey, 0, len(l.nextChainKeys)+1+len(l.olderChainKeys))
	ring = append(ring, l.nextChainKeys...)
	ring = append(ring, namedKey{id: l.chainKeyID, key: l.chainKey})
	ring = append(ring, l.olderChainKeys...)
	return ring, len(l.nextChainKeys)
}

// sealingKey picks the key that seals the event appended after head. A
// Logger never seals with a key older than the head's: a head sealed by a
// next key is followed with that key, and an unknown head refuses the
// append, so replicas rolling through a rotation cannot break the chain.
func (l *Logger) sealingKey(head Event, marker bool) (namedKey, error) {
	ring, sealing := l.keyRing()
	headKey := -1
	if len(head.HMAC) > 0 {
		var err error
		if headKey, err = sealerOf(ring, sealing, head); err != nil {
			return namedKey{}, err
		}
		if headKey < 0 {
			return namedKey{}, ErrChainHeadUnknown
		}
	}
	switch {
	case headKey >= 0 && headKey < sealing:
		if marker {
			return namedKey{}, errRotationCurrent
		}
		return ring[headKey], nil
	case marker && headKey == sealing:
		return namedKey{}, errRotationCurrent
	case !marker && l.chainKeyID != "" && headKey != sealing:
		return namedKey{}, errRotationDue
	}
	return ring[sealing], nil
}

// sealerOf is the index of the ring key that sealed event, trying first
// before the others since it is the usual sealer; -1 when none did.
func sealerOf(ring []namedKey, first int, event Event) (int, error) {
	canonical := eventWithoutHMAC(event)
	order := make([]int, 0, len(ring))
	order = append(order, first)
	for i := range ring {
		if i != first {
			order = append(order, i)
		}
	}
	for _, i := range order {
		var matched, empty bool
		ring[i].key.Use(func(k []byte) {
			if len(k) == 0 {
				empty = true
				return
			}
			matched, _ = sealedBy(ChainKey{ID: ring[i].id, Key: k}, canonical, event.HMAC)
		})
		if empty {
			return -1, ErrLoggerClosed
		}
		if matched {
			return i, nil
		}
	}
	return -1, nil
}

// sealedBy reports whether mac is k's HMAC over canonical, and whether it
// binds k's ID; the unkeyed form matches too, as [WithChainKey] seals it.
func sealedBy(k ChainKey, canonical Event, mac []byte) (matched, keyed bool) {
	if k.ID != "" && constantTimeEqualHMAC(mac, computeKeyedHMAC(k.Key, k.ID, canonical)) {
		return true, true
	}
	return constantTimeEqualHMAC(mac, computeHMAC(k.Key, canonical)), false
}
