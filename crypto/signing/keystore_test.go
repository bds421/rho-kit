package signing

import (
	"context"
	"errors"
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

func TestMustNewStaticKeyStore(t *testing.T) {
	key1 := testKey(32, 1)
	key2 := testKey(48, 2)

	store := MustNewStaticKeyStore(map[string][]byte{
		"k1": key1,
		"k2": key2,
	}, "k1")

	id, secret, err := store.CurrentKeyID(context.Background())
	if err != nil {
		t.Fatalf("CurrentKeyID: %v", err)
	}
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
	store := MustNewStaticKeyStore(map[string][]byte{
		"k1": key1,
		"k2": key2,
	}, "k1")
	ctx := context.Background()

	k, err := store.Key(ctx, "k1")
	if err != nil || len(k) != 32 {
		t.Errorf("Key(k1) = (len=%d, err=%v), want (32-byte key, nil)", len(k), err)
	}

	k, err = store.Key(ctx, "k2")
	if err != nil || len(k) != 48 {
		t.Errorf("Key(k2) = (len=%d, err=%v), want (48-byte key, nil)", len(k), err)
	}

	_, err = store.Key(ctx, "nonexistent")
	if !errors.Is(err, ErrUnknownKeyID) {
		t.Errorf("Key(nonexistent) = %v, want ErrUnknownKeyID", err)
	}
}

func TestStaticKeyStore_DefensiveCopy(t *testing.T) {
	original := testKey(32, 1)
	keys := map[string][]byte{"k1": original}
	store := MustNewStaticKeyStore(keys, "k1")

	// Mutate the original — store should be unaffected.
	original[0] = 0xFF
	k, _ := store.Key(context.Background(), "k1")
	if k[0] == 0xFF {
		t.Error("StaticKeyStore did not defensively copy the key")
	}
}

func TestMustNewStaticKeyStore_PanicsEmptyKeys(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty keys map")
		}
	}()
	MustNewStaticKeyStore(map[string][]byte{}, "k1")
}

func TestMustNewStaticKeyStore_PanicsMissingCurrentID(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when currentID not in keys")
		}
	}()
	MustNewStaticKeyStore(map[string][]byte{
		"k1": testKey(32, 1),
	}, "k2")
}

func TestMustNewStaticKeyStore_PanicsShortKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for key shorter than 32 bytes")
		}
	}()
	MustNewStaticKeyStore(map[string][]byte{
		"k1": testKey(16, 1),
	}, "k1")
}

func TestNewStaticKeyStore_HappyPath(t *testing.T) {
	s, err := NewStaticKeyStore(map[string][]byte{"k1": testKey(32, 1)}, "k1")
	if err != nil {
		t.Fatalf("NewStaticKeyStore: %v", err)
	}
	id, _, err := s.CurrentKeyID(context.Background())
	if err != nil {
		t.Fatalf("CurrentKeyID: %v", err)
	}
	if id != "k1" {
		t.Errorf("currentID = %q, want k1", id)
	}
}

func TestNewStaticKeyStore_ErrorOnEmptyKeys(t *testing.T) {
	_, err := NewStaticKeyStore(map[string][]byte{}, "k1")
	if err == nil {
		t.Fatal("expected error for empty keys map")
	}
}

func TestNewStaticKeyStore_ErrorOnMissingCurrentID(t *testing.T) {
	_, err := NewStaticKeyStore(map[string][]byte{"k1": testKey(32, 1)}, "secret-token")
	if err == nil {
		t.Fatal("expected error for missing currentID")
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error leaked currentID: %v", err)
	}
}

func TestNewStaticKeyStore_ErrorOnShortKey(t *testing.T) {
	_, err := NewStaticKeyStore(map[string][]byte{"secret-token": testKey(16, 1)}, "secret-token")
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

func TestNewStaticKeyStore_ErrorOnInvalidKeyID(t *testing.T) {
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
			_, err := NewStaticKeyStore(tt.keys, tt.currentID)
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

func TestNewStaticKeyStore_ErrorOnInvalidNonCurrentKeyID(t *testing.T) {
	_, err := NewStaticKeyStore(map[string][]byte{
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
	ctx := context.Background()
	if k, err := store.Key(ctx, "k1"); !errors.Is(err, ErrUnknownKeyID) || k != nil {
		t.Fatalf("zero-value Key = (%v, %v), want nil ErrUnknownKeyID", k, err)
	}
	if id, k, err := store.CurrentKeyID(ctx); id != "" || k != nil || err != nil {
		t.Fatalf("zero-value CurrentKeyID = (%q, %v, %v), want empty nil nil", id, k, err)
	}
}

func TestStaticKeyStore_NilReceiverMethodsFailClosed(t *testing.T) {
	var store *StaticKeyStore
	ctx := context.Background()
	if k, err := store.Key(ctx, "k1"); !errors.Is(err, ErrUnknownKeyID) || k != nil {
		t.Fatalf("nil Key = (%v, %v), want nil ErrUnknownKeyID", k, err)
	}
	if id, k, err := store.CurrentKeyID(ctx); id != "" || k != nil || err != nil {
		t.Fatalf("nil CurrentKeyID = (%q, %v, %v), want empty nil nil", id, k, err)
	}
}

func TestStaticKeyStore_ConcurrentAccess(t *testing.T) {
	store := MustNewStaticKeyStore(map[string][]byte{"k1": testKey(32, 1)}, "k1")
	var wg sync.WaitGroup
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = store.Key(ctx, "k1")
			_, _, _ = store.CurrentKeyID(ctx)
		}()
	}
	wg.Wait()
}

func TestStaticKeyStore_Close_ZeroesKeysAndFailsClosed(t *testing.T) {
	store := MustNewStaticKeyStore(map[string][]byte{
		"k1": testKey(32, 1),
		"k2": testKey(32, 2),
	}, "k1")
	ctx := context.Background()

	// Sanity: keys present before Close.
	if k, err := store.Key(ctx, "k1"); err != nil || len(k) == 0 {
		t.Fatal("expected k1 present before Close")
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// After Close, lookups must report ErrUnknownKeyID.
	if k, err := store.Key(ctx, "k1"); !errors.Is(err, ErrUnknownKeyID) || k != nil {
		t.Fatalf("Key after Close = (%v, %v), want nil ErrUnknownKeyID", k, err)
	}
	if id, k, err := store.CurrentKeyID(ctx); id != "" || k != nil || err != nil {
		t.Fatalf("CurrentKeyID after Close = (%q, %v, %v), want empty nil nil", id, k, err)
	}
	// Idempotent.
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestStaticKeyStore_Close_NilReceiverIsSafe(t *testing.T) {
	var store *StaticKeyStore
	if err := store.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
}

// TestStaticKeyStore_KeyRaceWithCloseNeverReturnsNilNil exercises the
// window between the presence check and the secret read in Key /
// CurrentKeyID. A concurrent Close that zeroes the wrapped secret
// between those two steps must never produce a success-shaped result
// with a nil secret: Key must return ErrUnknownKeyID, and CurrentKeyID
// must report the cleared key by returning an empty id.
func TestStaticKeyStore_KeyRaceWithCloseNeverReturnsNilNil(t *testing.T) {
	ctx := context.Background()
	for iter := 0; iter < 5000; iter++ {
		store := MustNewStaticKeyStore(map[string][]byte{"k1": testKey(32, 1)}, "k1")

		var wg sync.WaitGroup
		start := make(chan struct{})

		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_ = store.Close()
		}()

		readers := 8
		for r := 0; r < readers; r++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				// Spin so a reader is mid-flight when Close lands,
				// hitting the window between the presence check and
				// the secret read.
				for i := 0; i < 64; i++ {
					if k, err := store.Key(ctx, "k1"); err == nil && k == nil {
						t.Errorf("Key returned (nil, nil): success-shaped result with no key")
						return
					}
					if id, k, err := store.CurrentKeyID(ctx); err == nil && id != "" && k == nil {
						t.Errorf("CurrentKeyID returned (%q, nil, nil): id present but no secret", id)
						return
					}
				}
			}()
		}

		close(start)
		wg.Wait()
		if t.Failed() {
			return
		}
	}
}
