package redisstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/data/v2/idempotency"
)

// TestNew_NilClientPanics verifies the constructor fails fast rather
// than letting a miswired store dereference nil on first use.
func TestNew_NilClientPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil client")
		}
	}()
	_ = New(nil)
}

func TestNew_NilOptionPanics(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for nil option")
		}
	}()
	_ = New(client, nil)
}

func TestWithKeyPrefix_PanicsOnInvalid(t *testing.T) {
	for _, prefix := range []string{
		"",
		strings.Repeat("x", maxKeyPrefixLen+1),
		"bad\nprefix",
		"bad\x00prefix",
		"bad prefix",
		"bad\tprefix",
		string([]byte{0xff, 0xfe}),
	} {
		t.Run("invalid", func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatal("expected panic for invalid prefix")
				}
				if len(prefix) > maxKeyPrefixLen {
					msg, _ := r.(string)
					if strings.Contains(msg, "128") || strings.Contains(msg, "129") {
						t.Fatalf("panic leaked prefix lengths: %q", msg)
					}
				}
			}()
			_ = WithKeyPrefix(prefix)
		})
	}
}

func TestStore_InvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()

	for name, store := range map[string]*Store{
		"nil":  nil,
		"zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			resp, mismatch, err := store.Get(ctx, "k", nil)
			if resp != nil || mismatch || !errors.Is(err, idempotency.ErrInvalidStore) {
				t.Fatalf("Get = resp=%v mismatch=%v err=%v, want ErrInvalidStore", resp, mismatch, err)
			}

			token, mismatch, ok, err := store.TryLock(ctx, "k", nil, time.Minute)
			if token != "" || mismatch || ok || !errors.Is(err, idempotency.ErrInvalidStore) {
				t.Fatalf("TryLock = token=%q mismatch=%v ok=%v err=%v, want ErrInvalidStore", token, mismatch, ok, err)
			}

			if err := store.Set(ctx, "k", "t", idempotency.CachedResponse{}, time.Minute); !errors.Is(err, idempotency.ErrInvalidStore) {
				t.Fatalf("Set = %v, want ErrInvalidStore", err)
			}

			if err := store.Unlock(ctx, "k", "t"); !errors.Is(err, idempotency.ErrInvalidStore) {
				t.Fatalf("Unlock = %v, want ErrInvalidStore", err)
			}
		})
	}
}

func TestStore_SetRejectsInvalidCachedResponseBeforeRedisUse(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	store := New(client)

	err := store.Set(context.Background(), "k", "token", idempotency.CachedResponse{StatusCode: 99}, time.Minute)
	if !errors.Is(err, idempotency.ErrInvalidCachedResponse) {
		t.Fatalf("Set invalid response = %v, want ErrInvalidCachedResponse", err)
	}
}

// TestTTLMillisRoundUp guards the TTL precision fix: Redis SET PX accepts
// integer milliseconds. Truncating a sub-millisecond duration with
// ttl.Milliseconds() used to round to 0 and cause Redis to reject the
// command, breaking the lock acquisition.
func TestTTLMillisRoundUp(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want int64
	}{
		{1 * time.Nanosecond, 1},
		{500 * time.Microsecond, 1},
		{1 * time.Millisecond, 1},
		{500 * time.Millisecond, 500},
		{999*time.Millisecond + 1*time.Microsecond, 1000},
		{1 * time.Second, 1000},
		{60 * time.Second, 60_000},
	}
	for _, c := range cases {
		if got := ttlMillisRoundUp(c.in); got != c.want {
			t.Errorf("ttlMillisRoundUp(%s) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestTTLRoundUp_PreservesAtLeastOneMilli(t *testing.T) {
	if got := ttlRoundUp(1 * time.Nanosecond); got != time.Millisecond {
		t.Errorf("ttlRoundUp(1ns) = %s, want 1ms", got)
	}
	if got := ttlRoundUp(2*time.Second + 250*time.Microsecond); got != 2001*time.Millisecond {
		t.Errorf("ttlRoundUp(2s 250µs) = %s, want 2001ms", got)
	}
}
