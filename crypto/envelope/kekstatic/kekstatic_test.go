package kekstatic

import (
	"bytes"
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
