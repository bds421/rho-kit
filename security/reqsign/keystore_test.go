package reqsign

import (
	"sync"
	"testing"
)

func TestNewStaticKeyStore(t *testing.T) {
	key1 := testKey(32, 1)
	key2 := testKey(48, 2)

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
	key1 := testKey(32, 1)
	key2 := testKey(48, 2)
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
	original := testKey(32, 1)
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
		"k1": testKey(32, 1),
	}, "k2")
}

func TestNewStaticKeyStore_PanicsShortKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for key shorter than 32 bytes")
		}
	}()
	NewStaticKeyStore(map[string][]byte{
		"k1": testKey(16, 1),
	}, "k1")
}

func TestStaticKeyStore_ConcurrentAccess(t *testing.T) {
	store := NewStaticKeyStore(map[string][]byte{"k1": testKey(32, 1)}, "k1")
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Key("k1")
			store.CurrentKeyID()
		}()
	}
	wg.Wait()
}
