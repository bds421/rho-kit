package idempotency

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func TestValidateKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want error
	}{
		{name: "empty", key: "", want: ErrKeyEmpty},
		{name: "too long", key: strings.Repeat("a", MaxKeyLen+1), want: ErrKeyTooLong},
		{name: "null byte", key: "bad\x00key", want: ErrKeyInvalidChars},
		{name: "newline", key: "bad\nkey", want: ErrKeyInvalidChars},
		{name: "carriage return", key: "bad\rkey", want: ErrKeyInvalidChars},
		{name: "space", key: "bad key", want: ErrKeyInvalidChars},
		{name: "tab", key: "bad\tkey", want: ErrKeyInvalidChars},
		{name: "invalid utf8", key: string([]byte{0xff, 0xfe}), want: ErrKeyInvalidChars},
		{name: "valid max length", key: strings.Repeat("a", MaxKeyLen), want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateKey(tt.key)
			if tt.want == nil {
				if err != nil {
					t.Fatalf("ValidateKey(%q): %v", tt.key, err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("ValidateKey(%q) = %v, want %v", tt.key, err, tt.want)
			}
			if tt.name == "too long" && (strings.Contains(err.Error(), "256") || strings.Contains(err.Error(), "257")) {
				t.Fatalf("ValidateKey leaked key lengths: %v", err)
			}
		})
	}
}

func TestValidateCachedResponse(t *testing.T) {
	valid := CachedResponse{
		StatusCode: 201,
		Headers:    map[string][]string{"Content-Type": {"application/json"}},
		Body:       []byte(`{"ok":true}`),
	}
	if err := ValidateCachedResponse(valid); err != nil {
		t.Fatalf("valid response rejected: %v", err)
	}

	tests := map[string]CachedResponse{
		"zero status":       {StatusCode: 0},
		"oversize body":     {StatusCode: 200, Body: []byte(strings.Repeat("x", MaxCachedBodyBytes+1))},
		"empty header name": {StatusCode: 200, Headers: map[string][]string{"": {"v"}}},
		"bad header name":   {StatusCode: 200, Headers: map[string][]string{"Bad Header": {"v"}}},
		"bad header value":  {StatusCode: 200, Headers: map[string][]string{"X-Test": {"bad\r\nvalue"}}},
		"oversize value":    {StatusCode: 200, Headers: map[string][]string{"X-Test": {strings.Repeat("x", MaxCachedHeaderValueBytes+1)}}},
	}
	for name, resp := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateCachedResponse(resp)
			if !errors.Is(err, ErrInvalidCachedResponse) {
				t.Fatalf("ValidateCachedResponse = %v, want ErrInvalidCachedResponse", err)
			}
			if strings.Contains(name, "oversize") {
				for _, leaked := range []string{"64", "65", "128", "129", "8192", "8193", "1048576", "1048577"} {
					if strings.Contains(err.Error(), leaked) {
						t.Fatalf("ValidateCachedResponse leaked length %s: %v", leaked, err)
					}
				}
			}
		})
	}
}

func TestValidateCachedResponse_DoesNotReflectHeaderMetadata(t *testing.T) {
	tests := map[string]CachedResponse{
		"invalid name": {
			StatusCode: 200,
			Headers:    map[string][]string{"Bad Header secret-token": {"v"}},
		},
		"oversize value": {
			StatusCode: 200,
			Headers:    map[string][]string{"X-Secret-Token": {strings.Repeat("x", MaxCachedHeaderValueBytes+1)}},
		},
	}
	for name, resp := range tests {
		t.Run(name, func(t *testing.T) {
			err := ValidateCachedResponse(resp)
			if !errors.Is(err, ErrInvalidCachedResponse) {
				t.Fatalf("ValidateCachedResponse = %v, want ErrInvalidCachedResponse", err)
			}
			if strings.Contains(strings.ToLower(err.Error()), "secret-token") {
				t.Fatalf("ValidateCachedResponse error reflected header metadata: %v", err)
			}
		})
	}
}

func TestGenerateTokenReturnsErrorOnRandomFailure(t *testing.T) {
	prev := tokenRandReader
	tokenRandReader = failingReader{}
	t.Cleanup(func() { tokenRandReader = prev })

	token, err := GenerateToken()
	if err == nil {
		t.Fatal("expected random failure")
	}
	if token != "" {
		t.Fatalf("token = %q, want empty on error", token)
	}
	if !strings.Contains(err.Error(), "generate lock token") {
		t.Fatalf("error = %v, want generate lock token context", err)
	}
}

func TestMemoryStore_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()

	for name, store := range map[string]*MemoryStore{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			resp, mismatch, err := store.Get(ctx, "k", nil)
			if resp != nil || mismatch || !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("Get = resp=%v mismatch=%v err=%v, want ErrInvalidStore", resp, mismatch, err)
			}

			token, mismatch, ok, err := store.TryLock(ctx, "k", nil, time.Minute)
			if token != "" || mismatch || ok || !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("TryLock = token=%q mismatch=%v ok=%v err=%v, want ErrInvalidStore", token, mismatch, ok, err)
			}

			if err := store.Set(ctx, "k", "t", CachedResponse{}, time.Minute); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("Set = %v, want ErrInvalidStore", err)
			}

			if err := store.Unlock(ctx, "k", "t"); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("Unlock = %v, want ErrInvalidStore", err)
			}

			if err := store.Run(ctx); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("Run = %v, want ErrInvalidStore", err)
			}
		})
	}
}

func TestMemoryStore_RunRejectsNilContext(t *testing.T) {
	store := NewMemoryStore()
	var ctx context.Context
	err := store.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "non-nil context") {
		t.Fatalf("expected nil context error, got %v", err)
	}
}

func TestMemoryStore_RunRejectsSecondStart(t *testing.T) {
	store := NewMemoryStore()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- store.Run(ctx) }()
	waitForMemoryStoreRunStarted(t, store)

	err := store.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected already started error, got %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}
}

func TestMemoryStore_RunRejectsRestartAfterCancel(t *testing.T) {
	store := NewMemoryStore()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- store.Run(ctx) }()
	waitForMemoryStoreRunStarted(t, store)

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v", err)
	}

	err := store.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "already started") {
		t.Fatalf("expected already started error, got %v", err)
	}
}

func waitForMemoryStoreRunStarted(t *testing.T, store *MemoryStore) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.runMu.Lock()
		started := store.started
		store.runMu.Unlock()
		if started {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("MemoryStore.Run did not start")
}

func TestMemoryStore_RejectsInvalidKey(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()

	resp, mismatch, err := store.Get(ctx, "", nil)
	if resp != nil || mismatch || !errors.Is(err, ErrKeyEmpty) {
		t.Fatalf("Get empty key = resp=%v mismatch=%v err=%v, want ErrKeyEmpty", resp, mismatch, err)
	}

	token, mismatch, ok, err := store.TryLock(ctx, "", nil, time.Minute)
	if token != "" || mismatch || ok || !errors.Is(err, ErrKeyEmpty) {
		t.Fatalf("TryLock empty key = token=%q mismatch=%v ok=%v err=%v, want ErrKeyEmpty", token, mismatch, ok, err)
	}

	if err := store.Set(ctx, "", "t", CachedResponse{}, time.Minute); !errors.Is(err, ErrKeyEmpty) {
		t.Fatalf("Set empty key = %v, want ErrKeyEmpty", err)
	}

	if err := store.Unlock(ctx, "", "t"); !errors.Is(err, ErrKeyEmpty) {
		t.Fatalf("Unlock empty key = %v, want ErrKeyEmpty", err)
	}
}

func TestMemoryStore_TryLock_RejectsZeroTTL(t *testing.T) {
	store := NewMemoryStore()
	_, _, _, err := store.TryLock(context.Background(), "k", []byte("fp"), 0)
	if !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("ttl=0: got %v, want ErrInvalidTTL", err)
	}
}

func TestMemoryStore_TryLock_RejectsNegativeTTL(t *testing.T) {
	store := NewMemoryStore()
	_, _, _, err := store.TryLock(context.Background(), "k", []byte("fp"), -1*time.Second)
	if !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("ttl=-1s: got %v, want ErrInvalidTTL", err)
	}
}

func TestMemoryStore_Set_RejectsNonPositiveTTL(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	token, _, ok, err := store.TryLock(ctx, "k", []byte("fp"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("setup: TryLock failed: ok=%v err=%v", ok, err)
	}

	resp := CachedResponse{StatusCode: 200, Body: []byte("ok")}

	if err := store.Set(ctx, "k", token, resp, 0); !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("ttl=0: got %v, want ErrInvalidTTL", err)
	}
	if err := store.Set(ctx, "k", token, resp, -time.Second); !errors.Is(err, ErrInvalidTTL) {
		t.Errorf("ttl=-1s: got %v, want ErrInvalidTTL", err)
	}
}

func TestMemoryStore_Set_RejectsInvalidCachedResponse(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	token, _, ok, err := store.TryLock(ctx, "k", []byte("fp"), time.Minute)
	if err != nil || !ok {
		t.Fatalf("setup: TryLock failed: ok=%v err=%v", ok, err)
	}

	err = store.Set(ctx, "k", token, CachedResponse{StatusCode: 99}, time.Minute)
	if !errors.Is(err, ErrInvalidCachedResponse) {
		t.Fatalf("Set invalid response = %v, want ErrInvalidCachedResponse", err)
	}
}

func TestMemoryStore_TryLock_PositiveTTLSucceeds(t *testing.T) {
	// Sanity check that the guard didn't break the happy path.
	store := NewMemoryStore()
	token, mismatch, ok, err := store.TryLock(context.Background(), "k", []byte("fp"), time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mismatch || !ok || token == "" {
		t.Errorf("got token=%q mismatch=%v ok=%v; want acquired", token, mismatch, ok)
	}
}

func TestMemoryStore_TryLock_ReturnsRandomFailure(t *testing.T) {
	prev := tokenRandReader
	tokenRandReader = failingReader{}
	t.Cleanup(func() { tokenRandReader = prev })

	store := NewMemoryStore()
	token, mismatch, ok, err := store.TryLock(context.Background(), "k", []byte("fp"), time.Minute)
	if err == nil {
		t.Fatal("expected random failure")
	}
	if token != "" || mismatch || ok {
		t.Fatalf("TryLock = token=%q mismatch=%v ok=%v, want empty false false", token, mismatch, ok)
	}
	if !strings.Contains(err.Error(), "generate lock token") {
		t.Fatalf("error = %v, want generate lock token context", err)
	}
}

func TestWithMemoryStoreClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	_ = WithMemoryStoreClock(nil)
}

func TestNewMemoryStore_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil option")
		}
	}()
	_ = NewMemoryStore(nil)
}

// TestMemoryStore_SubSecondTTL_NotImmediatelyExpired guards the contract
// documented at the package level: a positive TTL — even sub-second — MUST
// produce a row that's reachable for at least a moment. MemoryStore stores
// nanosecond expiry so this is the easy case; the pgstore variant of the
// same property lives next to its second-precision rounding helper.
func TestMemoryStore_SubSecondTTL_NotImmediatelyExpired(t *testing.T) {
	store := NewMemoryStore()
	ctx := context.Background()
	token, _, ok, err := store.TryLock(ctx, "k", []byte("fp"), 500*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("TryLock(500ms): ok=%v err=%v", ok, err)
	}
	resp := CachedResponse{StatusCode: 200, Body: []byte("ok")}
	if err := store.Set(ctx, "k", token, resp, 500*time.Millisecond); err != nil {
		t.Fatalf("Set(500ms): %v", err)
	}
	got, _, err := store.Get(ctx, "k", []byte("fp"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got == nil {
		t.Fatal("entry expired immediately for 500ms TTL")
	}
}
