package kekstatic

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNewRejectsInvalidMasterKeyLengthWithStableError(t *testing.T) {
	_, err := NewKEK("k1", []byte("short"))
	if err == nil {
		t.Fatal("expected invalid master key length")
	}
	if strings.Contains(err.Error(), "5") {
		t.Fatalf("error leaked supplied key length: %v", err)
	}
}

func TestAddKeyRejectsInvalidMasterKeyLengthWithStableError(t *testing.T) {
	k, err := NewKEK("k1", make([]byte, 32))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = k.AddKey("k2", []byte("short"))
	if err == nil {
		t.Fatal("expected invalid master key length")
	}
	if strings.Contains(err.Error(), "5") {
		t.Fatalf("error leaked supplied key length: %v", err)
	}
}

func TestRemoveKeyZeroesRetiredMasterKey(t *testing.T) {
	k, err := NewKEK("active", bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := k.AddKey("retired", bytes.Repeat([]byte{2}, 32)); err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	k.mu.RLock()
	stored := k.keys["retired"]
	k.mu.RUnlock()
	if len(stored) != 32 {
		t.Fatalf("stored key length = %d, want 32", len(stored))
	}

	if err := k.RemoveKey("retired"); err != nil {
		t.Fatalf("RemoveKey: %v", err)
	}
	if !bytes.Equal(stored, make([]byte, 32)) {
		t.Fatalf("retired key bytes were not zeroed")
	}
}

// TestCloseIsTerminal pins the H-007 finding: after Close, every public
// mutator and Wrap/Unwrap must fail closed with ErrKEKClosed. Without
// this guard a caller could AddKey + Rotate + Wrap after Close and
// resurrect the keyset as a fresh in-memory KEK — defeating the
// shutdown zeroize contract.
func TestCloseIsTerminal(t *testing.T) {
	k, err := NewKEK("active", bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := k.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Idempotent.
	if err := k.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	cases := []struct {
		name string
		op   func() error
	}{
		{"AddKey", func() error { return k.AddKey("new", bytes.Repeat([]byte{3}, 32)) }},
		{"Rotate", func() error { return k.Rotate("active") }},
		{"RemoveKey", func() error { return k.RemoveKey("active") }},
		{"Wrap", func() error {
			_, _, err := k.Wrap(context.Background(), []byte("dek"))
			return err
		}},
		{"Unwrap", func() error {
			_, err := k.Unwrap(context.Background(), "active", []byte("blob"))
			return err
		}},
	}

	for _, tc := range cases {
		err := tc.op()
		if !errors.Is(err, ErrKEKClosed) {
			t.Fatalf("%s after Close: err = %v, want ErrKEKClosed", tc.name, err)
		}
	}

	// Bypass attempt the audit highlighted: AddKey + Rotate + Wrap must
	// not silently revive the KEK.
	if err := k.AddKey("revive", bytes.Repeat([]byte{4}, 32)); !errors.Is(err, ErrKEKClosed) {
		t.Fatalf("AddKey resurrection: err = %v, want ErrKEKClosed", err)
	}
	if _, _, err := k.Wrap(context.Background(), []byte("dek")); !errors.Is(err, ErrKEKClosed) {
		t.Fatalf("Wrap after resurrection attempt: err = %v, want ErrKEKClosed", err)
	}
}
