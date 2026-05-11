package revocation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

var _ jwtutil.RevocationChecker = (*Store)(nil)

type fakeCache struct {
	mu      sync.Mutex
	items   map[string][]byte
	lastKey string
	lastTTL time.Duration
	err     error
}

func newFakeCache() *fakeCache {
	return &fakeCache{items: make(map[string][]byte)}
}

func (c *fakeCache) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	if c.err != nil {
		return c.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = append([]byte(nil), value...)
	c.lastKey = key
	c.lastTTL = ttl
	return nil
}

func (c *fakeCache) Delete(_ context.Context, key string) error {
	if c.err != nil {
		return c.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
	return nil
}

func (c *fakeCache) Exists(_ context.Context, key string) (bool, error) {
	if c.err != nil {
		return false, c.err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.items[key]
	return ok, nil
}

func TestStore_RevokeAndCheck(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	cache := newFakeCache()
	store := New(cache, WithClock(func() time.Time { return now }))

	claims := &jwtutil.Claims{
		ID:        "token-123",
		Issuer:    "https://issuer.example",
		ExpiresAt: now.Add(10 * time.Minute).Unix(),
	}
	if err := store.Revoke(context.Background(), claims); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	wantKey := "jwt-revoked:22:https://issuer.example:9:token-123"
	if cache.lastKey != wantKey {
		t.Fatalf("key = %q, want %q", cache.lastKey, wantKey)
	}
	if cache.lastTTL != 10*time.Minute {
		t.Fatalf("ttl = %s, want 10m", cache.lastTTL)
	}

	revoked, err := store.IsRevoked(context.Background(), claims)
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if !revoked {
		t.Fatal("expected revoked=true")
	}
}

func TestStore_LengthPrefixesIssuerAndID(t *testing.T) {
	cache := newFakeCache()
	store := New(cache, WithClock(func() time.Time {
		return time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	}))
	expiresAt := time.Date(2026, 5, 10, 12, 30, 0, 0, time.UTC)

	if err := store.RevokeID(context.Background(), "a:b", "c", expiresAt); err != nil {
		t.Fatalf("RevokeID a:b/c: %v", err)
	}
	first := cache.lastKey
	if err := store.RevokeID(context.Background(), "a", "b:c", expiresAt); err != nil {
		t.Fatalf("RevokeID a/b:c: %v", err)
	}
	second := cache.lastKey
	if first == second {
		t.Fatalf("length-prefixed keys collided: %q", first)
	}
}

func TestStore_RequiresTokenID(t *testing.T) {
	store := New(newFakeCache())
	_, err := store.IsRevoked(context.Background(), &jwtutil.Claims{Issuer: "issuer"})
	if !errors.Is(err, ErrMissingTokenID) {
		t.Fatalf("IsRevoked error = %v, want ErrMissingTokenID", err)
	}
	if !errors.Is(err, jwtutil.ErrMissingTokenID) {
		t.Fatalf("IsRevoked error = %v, want jwtutil.ErrMissingTokenID", err)
	}
}

func TestStore_RejectsInvalidKeyParts(t *testing.T) {
	store := New(newFakeCache())
	expiresAt := time.Now().Add(time.Minute)

	cases := []struct {
		name   string
		issuer string
		id     string
	}{
		{name: "issuer newline", issuer: "issuer\n", id: "token"},
		{name: "issuer space", issuer: "issuer one", id: "token"},
		{name: "issuer tab", issuer: "issuer\tone", id: "token"},
		{name: "issuer invalid utf8", issuer: string([]byte{'i', 0xff}), id: "token"},
		{name: "id newline", issuer: "issuer", id: "token\n"},
		{name: "id space", issuer: "issuer", id: "token one"},
		{name: "id tab", issuer: "issuer", id: "token\tone"},
		{name: "id invalid utf8", issuer: "issuer", id: string([]byte{'t', 0xff})},
		{name: "combined key too long", issuer: strings.Repeat("i", 600), id: strings.Repeat("t", 600)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := store.RevokeID(context.Background(), tc.issuer, tc.id, expiresAt)
			if !errors.Is(err, ErrInvalidKey) {
				t.Fatalf("RevokeID error = %v, want ErrInvalidKey", err)
			}
		})
	}
}

func TestStore_RejectsExpiredToken(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	store := New(newFakeCache(), WithClock(func() time.Time { return now }))

	err := store.RevokeID(context.Background(), "issuer", "token", now)
	if !errors.Is(err, ErrInvalidExpiry) {
		t.Fatalf("RevokeID error = %v, want ErrInvalidExpiry", err)
	}
}

func TestStore_ForgetID(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	cache := newFakeCache()
	store := New(cache, WithClock(func() time.Time { return now }))
	ctx := context.Background()

	if err := store.RevokeID(ctx, "issuer", "token", now.Add(time.Minute)); err != nil {
		t.Fatalf("RevokeID: %v", err)
	}
	if err := store.ForgetID(ctx, "issuer", "token"); err != nil {
		t.Fatalf("ForgetID: %v", err)
	}
	revoked, err := store.IsRevokedID(ctx, "issuer", "token")
	if err != nil {
		t.Fatalf("IsRevokedID: %v", err)
	}
	if revoked {
		t.Fatal("expected revoked=false after ForgetID")
	}
}

func TestStore_PropagatesBackendError(t *testing.T) {
	want := errors.New("redis down")
	store := New(&fakeCache{items: make(map[string][]byte), err: want})
	_, err := store.IsRevokedID(context.Background(), "issuer", "token")
	if !errors.Is(err, want) {
		t.Fatalf("IsRevokedID error = %v, want %v", err, want)
	}
}

func TestNew_PanicsOnNilCache(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	_ = New(nil)
}

func TestWithKeyPrefix_PanicsOnInvalid(t *testing.T) {
	cases := map[string]string{
		"empty":        "",
		"newline":      "tenant\n",
		"carriage":     "tenant\r",
		"space":        "tenant key:",
		"tab":          "tenant\tkey:",
		"null":         "tenant\x00",
		"invalid utf8": string([]byte{'p', 0xff}),
		"too long":     strings.Repeat("a", maxPrefixLen+1),
	}
	for name, prefix := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("expected panic for prefix %q", prefix)
				}
			}()
			_ = WithKeyPrefix(prefix)
		})
	}
}
