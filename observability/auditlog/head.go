package auditlog

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Head is the newest event a successful verification walked; keep all of
// it as the next [Verification.Anchor].
type Head struct {
	// HMAC is the newest event's HMAC: the watermark when no event follows
	// it, and empty for an empty chain.
	HMAC []byte
	// Events is the number of events verified, so the newest event's
	// position; with [Verification.Removed] it includes the removed events.
	Events int
	// KeyID names the chain key that sealed the newest event, or the key
	// that seals the next one when no event was walked; empty for the
	// unkeyed [WithChainKey] key.
	KeyID string
	// At is when the newest event was written, as its HMAC covers it; zero
	// when no event was walked. As an anchor it lets
	// [Verification.PrunedBefore] tell whether retention can have removed
	// the anchored event.
	At time.Time
}

// ErrInvalidHead is returned by [Head.UnmarshalText] for text that is not
// a head [Head.MarshalText] wrote.
var ErrInvalidHead = errors.New("auditlog: not a chain head")

// MarshalText writes the head as one word, events:keyID:hmac:at, the HMAC in
// hex and at in Unix microseconds (empty only when no event was walked), for
// operators to keep off the host and pass back.
func (h Head) MarshalText() ([]byte, error) {
	at := ""
	if !h.At.IsZero() {
		at = strconv.FormatInt(h.At.UnixMicro(), 10)
	}
	return []byte(strconv.Itoa(h.Events) + ":" + h.KeyID + ":" + hex.EncodeToString(h.HMAC) + ":" + at), nil
}

// UnmarshalText reads a head [Head.MarshalText] wrote.
func (h *Head) UnmarshalText(text []byte) error {
	parts := strings.Split(string(text), ":")
	if len(parts) != 4 {
		return ErrInvalidHead
	}
	events, err := strconv.Atoi(parts[0])
	if err != nil || events < 0 || strconv.Itoa(events) != parts[0] {
		return ErrInvalidHead
	}
	if parts[1] != "" && !keyIDPattern.MatchString(parts[1]) {
		return ErrInvalidHead
	}
	var at time.Time
	if parts[3] != "" {
		micros, err := strconv.ParseInt(parts[3], 10, 64)
		if err != nil || strconv.FormatInt(micros, 10) != parts[3] {
			return ErrInvalidHead
		}
		at = time.UnixMicro(micros).UTC()
	}
	mac, err := hex.DecodeString(parts[2])
	if err != nil || (events > 0 && len(mac) == 0) {
		return ErrInvalidHead
	}
	*h = Head{HMAC: mac, Events: events, KeyID: parts[1], At: at}
	return nil
}

// VerifyStore streams store in append order through the checks of
// [VerifyChainKeys] and returns the head to keep: verification for tooling
// that holds the chain keys but neither appends nor lists, so needs no
// [Logger] and no cursor key.
func VerifyStore(ctx context.Context, store Store, keys []ChainKey, v Verification) (Head, error) {
	return verifyStore(ctx, store, keys, 0, v)
}

func verifyStore(ctx context.Context, store Store, keys []ChainKey, sealing int, v Verification) (Head, error) {
	verifier, err := newChainVerifier(keys, sealing, v)
	if err != nil {
		return Head{}, err
	}
	if err := store.RangeChain(ctx, verifier.step); err != nil {
		return Head{}, err
	}
	return verifier.finish()
}
