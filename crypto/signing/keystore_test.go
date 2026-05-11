package signing

import (
	"strings"
	"sync"
	"testing"
)

// testKey generates a deterministic byte sequence of the given size for testing.
func testKey(n int, seed int) []byte {
	k := make([]byte, n)
	for i := range k {
		k[i] = byte((i*7 + seed) % 256)
	}
	return k
}

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

func TestNewStaticKeyStoreE_HappyPath(t *testing.T) {
	s, err := NewStaticKeyStoreE(map[string][]byte{"k1": testKey(32, 1)}, "k1")
	if err != nil {
		t.Fatalf("NewStaticKeyStoreE: %v", err)
	}
	if id, _ := s.CurrentKeyID(); id != "k1" {
		t.Errorf("currentID = %q, want k1", id)
	}
}

func TestNewStaticKeyStoreE_ErrorOnEmptyKeys(t *testing.T) {
	_, err := NewStaticKeyStoreE(map[string][]byte{}, "k1")
	if err == nil {
		t.Fatal("expected error for empty keys map")
	}
}

func TestNewStaticKeyStoreE_ErrorOnMissingCurrentID(t *testing.T) {
	_, err := NewStaticKeyStoreE(map[string][]byte{"k1": testKey(32, 1)}, "secret-token")
	if err == nil {
		t.Fatal("expected error for missing currentID")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked currentID: %v", err)
	}
}

func TestNewStaticKeyStoreE_ErrorOnShortKey(t *testing.T) {
	_, err := NewStaticKeyStoreE(map[string][]byte{"secret-token": testKey(16, 1)}, "secret-token")
	if err == nil {
		t.Fatal("expected error for short key")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked key ID: %v", err)
	}
	if strings.Contains(err.Error(), "16") {
		t.Fatalf("error leaked key lengths: %v", err)
	}
}

func TestNewStaticKeyStoreE_ErrorOnInvalidKeyID(t *testing.T) {
	tests := []struct {
		name      string
		keys      map[string][]byte
		currentID string
	}{
		{name: "empty", keys: map[string][]byte{"": testKey(32, 1)}, currentID: ""},
		{name: "leading space", keys: map[string][]byte{" k1": testKey(32, 1)}, currentID: " k1"},
		{name: "trailing space", keys: map[string][]byte{"k1 ": testKey(32, 1)}, currentID: "k1 "},
		{name: "comma", keys: map[string][]byte{"k1,old": testKey(32, 1)}, currentID: "k1,old"},
		{name: "newline", keys: map[string][]byte{"k1\nold": testKey(32, 1)}, currentID: "k1\nold"},
		{name: "invalid utf8", keys: map[string][]byte{string([]byte{'k', 0xff}): testKey(32, 1)}, currentID: string([]byte{'k', 0xff})},
		{name: "too long", keys: map[string][]byte{strings.Repeat("k", maxKeyIDLen+1): testKey(32, 1)}, currentID: strings.Repeat("k", maxKeyIDLen+1)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewStaticKeyStoreE(tt.keys, tt.currentID)
			if err == nil {
				t.Fatal("expected error for invalid key ID")
			}
			if strings.Contains(err.Error(), tt.currentID) && tt.currentID != "" {
				t.Fatalf("error leaked currentID: %v", err)
			}
			if tt.name == "too long" && (strings.Contains(err.Error(), "256") || strings.Contains(err.Error(), "257")) {
				t.Fatalf("error leaked key ID lengths: %v", err)
			}
		})
	}
}

func TestNewStaticKeyStoreE_ErrorOnInvalidNonCurrentKeyID(t *testing.T) {
	_, err := NewStaticKeyStoreE(map[string][]byte{
		"k1":       testKey(32, 1),
		"k2,other": testKey(32, 2),
	}, "k1")
	if err == nil {
		t.Fatal("expected error for invalid non-current key ID")
	}
	if strings.Contains(err.Error(), "k2,other") {
		t.Fatalf("error leaked key ID: %v", err)
	}
}

func TestStaticKeyStore_ZeroValueMethodsFailClosed(t *testing.T) {
	var store StaticKeyStore
	if k, ok := store.Key("k1"); ok || k != nil {
		t.Fatalf("zero-value Key = (%v, %v), want nil false", k, ok)
	}
	if k, ok := store.KeyUnsafe("k1"); ok || k != nil {
		t.Fatalf("zero-value KeyUnsafe = (%v, %v), want nil false", k, ok)
	}
	if id, k := store.CurrentKeyID(); id != "" || k != nil {
		t.Fatalf("zero-value CurrentKeyID = (%q, %v), want empty nil", id, k)
	}
	if id, k := store.CurrentKeyUnsafe(); id != "" || k != nil {
		t.Fatalf("zero-value CurrentKeyUnsafe = (%q, %v), want empty nil", id, k)
	}
}

func TestStaticKeyStore_NilReceiverMethodsFailClosed(t *testing.T) {
	var store *StaticKeyStore
	if k, ok := store.Key("k1"); ok || k != nil {
		t.Fatalf("nil Key = (%v, %v), want nil false", k, ok)
	}
	if k, ok := store.KeyUnsafe("k1"); ok || k != nil {
		t.Fatalf("nil KeyUnsafe = (%v, %v), want nil false", k, ok)
	}
	if id, k := store.CurrentKeyID(); id != "" || k != nil {
		t.Fatalf("nil CurrentKeyID = (%q, %v), want empty nil", id, k)
	}
	if id, k := store.CurrentKeyUnsafe(); id != "" || k != nil {
		t.Fatalf("nil CurrentKeyUnsafe = (%q, %v), want empty nil", id, k)
	}
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
