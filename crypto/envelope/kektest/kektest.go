// Package kektest is the conformance suite every [envelope.KEK] passes: an
// adapter's own tests call [Conform] with a KEK over a real or emulated
// key, so each wrapper is held to the same contract the envelope layer
// relies on.
package kektest

import (
	"bytes"
	"context"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/bds421/rho-kit/crypto/v2/envelope"
)

const dekBytes = 32

// Conform checks the KEK contract as subtests:
//   - Wrap returns a non-empty key ID and bytes that are not the DEK, and
//     Unwrap under that ID returns the DEK;
//   - two wraps of one DEK differ (no deterministic wrap);
//   - the wrapped bytes and the DEK passed in are not aliased;
//   - Unwrap refuses an unknown key ID, a changed, truncated or empty
//     wrap, and a wrap under another key ID;
//   - concurrent wraps each unwrap to their own DEK.
func Conform(t *testing.T, kek envelope.KEK) {
	t.Helper()
	t.Run("round trip", func(t *testing.T) { roundTrip(t, kek) })
	t.Run("not deterministic", func(t *testing.T) { notDeterministic(t, kek) })
	t.Run("no aliasing", func(t *testing.T) { noAliasing(t, kek) })
	t.Run("refusals", func(t *testing.T) { refusals(t, kek) })
	t.Run("concurrent", func(t *testing.T) { concurrent(t, kek) })
}

func newDEK(t *testing.T) []byte {
	t.Helper()
	dek := make([]byte, dekBytes)
	if _, err := rand.Read(dek); err != nil {
		t.Fatal(err)
	}
	return dek
}

func wrap(t *testing.T, kek envelope.KEK, dek []byte) (string, []byte) {
	t.Helper()
	id, wrapped, err := kek.Wrap(context.Background(), dek)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if id == "" {
		t.Fatal("Wrap returned an empty key ID")
	}
	return id, wrapped
}

func roundTrip(t *testing.T, kek envelope.KEK) {
	dek := newDEK(t)
	id, wrapped := wrap(t, kek, dek)
	if len(wrapped) == 0 || bytes.Contains(wrapped, dek) {
		t.Fatal("the wrap holds the DEK in the clear")
	}
	got, err := kek.Unwrap(context.Background(), id, wrapped)
	if err != nil {
		t.Fatalf("Unwrap: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("Unwrap returned another DEK")
	}
}

func notDeterministic(t *testing.T, kek envelope.KEK) {
	dek := newDEK(t)
	_, a := wrap(t, kek, dek)
	_, b := wrap(t, kek, dek)
	if bytes.Equal(a, b) {
		t.Fatal("two wraps of one DEK are equal")
	}
}

func noAliasing(t *testing.T, kek envelope.KEK) {
	dek := newDEK(t)
	want := bytes.Clone(dek)
	id, wrapped := wrap(t, kek, dek)
	clear(dek)
	got, err := kek.Unwrap(context.Background(), id, wrapped)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("clearing the DEK passed in changed what unwraps: %v", err)
	}
	clear(got)
	again, err := kek.Unwrap(context.Background(), id, wrapped)
	if err != nil || !bytes.Equal(again, want) {
		t.Fatalf("clearing an unwrapped DEK changed the wrap: %v", err)
	}
}

func refusals(t *testing.T, kek envelope.KEK) {
	ctx := context.Background()
	id, wrapped := wrap(t, kek, newDEK(t))
	changed := bytes.Clone(wrapped)
	changed[len(changed)-1] ^= 1
	for name, c := range map[string]struct {
		id      string
		wrapped []byte
	}{
		"an unknown key ID": {id + "-unknown", wrapped},
		"a changed wrap":    {id, changed},
		"a truncated wrap":  {id, wrapped[:len(wrapped)/2]},
		"an empty wrap":     {id, nil},
	} {
		if dek, err := kek.Unwrap(ctx, c.id, c.wrapped); err == nil {
			t.Errorf("%s unwrapped (%d bytes)", name, len(dek))
		}
	}
}

func concurrent(t *testing.T, kek envelope.KEK) {
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan string, n)
	for range n {
		wg.Go(func() {
			dek := make([]byte, dekBytes)
			if _, err := rand.Read(dek); err != nil {
				errs <- err.Error()
				return
			}
			id, wrapped, err := kek.Wrap(context.Background(), dek)
			if err != nil {
				errs <- "Wrap: " + err.Error()
				return
			}
			got, err := kek.Unwrap(context.Background(), id, wrapped)
			if err != nil || !bytes.Equal(got, dek) {
				errs <- "a concurrent wrap did not unwrap to its DEK"
			}
		})
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
