package reqsign

import (
	"testing"
)

func validKey(n int) []byte {
	k := make([]byte, n)
	for i := range k {
		k[i] = byte(i % 256)
	}
	return k
}

func TestNewStaticKeyStore(t *testing.T) {
	key1 := validKey(32)
	key2 := validKey(48)

	store := NewStaticKeyStore(map[string][]byte{
		"k1": key1,
		"k2": key2,
	}, "k1")

	id, secret := store.CurrentKeyID()
	if id != "k1" {
		t.Errorf("CurrentKeyID() id = %q, want %q", id, "k1")
	}
	if len(secret) != 32 {
		t.Errorf("CurrentKeyID() secret len = %d, want 32", len(secret))
	}
}

func TestStaticKeyStore_Key(t *testing.T) {
	key1 := validKey(32)
	key2 := validKey(48)
	store := NewStaticKeyStore(map[string][]byte{
		"k1": key1,
		"k2": key2,
	}, "k1")

	k, ok := store.Key("k1")
	if !ok || len(k) != 32 {
		t.Errorf("Key(k1) = (%v, %v), want (32-byte key, true)", len(k), ok)
	}

	k, ok = store.Key("k2")
	if !ok || len(k) != 48 {
		t.Errorf("Key(k2) = (%v, %v), want (48-byte key, true)", len(k), ok)
	}

	_, ok = store.Key("nonexistent")
	if ok {
		t.Error("Key(nonexistent) should return false")
	}
}

func TestStaticKeyStore_DefensiveCopy(t *testing.T) {
	original := validKey(32)
	keys := map[string][]byte{"k1": original}
	store := NewStaticKeyStore(keys, "k1")

	// Mutate the original — store should be unaffected.
	original[0] = 0xFF
	k, _ := store.Key("k1")
	if k[0] == 0xFF {
		t.Error("StaticKeyStore did not defensively copy the key")
	}
}

func TestNewStaticKeyStore_PanicsEmptyKeys(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty keys map")
		}
	}()
	NewStaticKeyStore(map[string][]byte{}, "k1")
}

func TestNewStaticKeyStore_PanicsMissingCurrentID(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when currentID not in keys")
		}
	}()
	NewStaticKeyStore(map[string][]byte{
		"k1": validKey(32),
	}, "k2")
}

func TestNewStaticKeyStore_PanicsShortKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for key shorter than 32 bytes")
		}
	}()
	NewStaticKeyStore(map[string][]byte{
		"k1": validKey(16),
	}, "k1")
}
