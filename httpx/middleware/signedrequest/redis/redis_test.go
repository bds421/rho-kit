package redis_test

import (
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	signedredis "github.com/bds421/rho-kit/httpx/middleware/signedrequest/redis"
)

// redisAddr resolves the Redis address for the test run. REDIS_ADDR
// overrides; absent that we fall back to the conventional localhost.
// The actual reachability check is done by ping in newTestClient.
func redisAddr() string {
	if a := os.Getenv("REDIS_ADDR"); a != "" {
		return a
	}
	return "127.0.0.1:6379"
}

// newClientFor returns a fresh client against the test Redis. Used
// by tests that need a second client to prove cross-client state
// sharing. Caller is responsible for Close.
//
// MaxRetries is set to -1 (disabled) so an unreachable Redis fails
// the ping fast rather than retrying through five back-offs and
// stretching the skip into multiple seconds per test.
func newClientFor(_ *testing.T) goredis.UniversalClient {
	return goredis.NewClient(&goredis.Options{
		Addr:        redisAddr(),
		Password:    os.Getenv("REDIS_PASSWORD"),
		DB:          15,
		MaxRetries:  -1,
		DialTimeout: 500 * time.Millisecond,
	})
}

// newTestClient returns a Redis client backed by a real server. If
// the server is unreachable the test is skipped with a clear message.
// The DB is selected (default 15) to keep test traffic away from
// anything else sharing the box, and FLUSHDB-ed before/after the test
// so per-test state is isolated.
func newTestClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	client := newClientFor(t)
	pingCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		t.Skipf("redis not reachable at %s (set REDIS_ADDR/REDIS_PASSWORD to override): %v", redisAddr(), err)
	}
	// Isolate test data.
	flushCtx, flushCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer flushCancel()
	if err := client.FlushDB(flushCtx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("flushdb: %v", err)
	}
	t.Cleanup(func() {
		fctx, fcancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer fcancel()
		_ = client.FlushDB(fctx).Err()
		_ = client.Close()
	})
	return client
}

// uniquePrefix lets parallel test runs against the same Redis avoid
// collisions even if FLUSHDB is unavailable to them.
func uniquePrefix(t *testing.T) string {
	t.Helper()
	return "sr-test:" + t.Name() + ":" + time.Now().Format("150405.000000000") + ":"
}

func TestNew_PanicsOnNilClient(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil client")
		}
	}()
	signedredis.New(nil, time.Minute)
}

func TestNew_PanicsOnZeroTTL(t *testing.T) {
	client := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero ttl")
		}
	}()
	signedredis.New(client, 0)
}

func TestSeenOrStore_FirstTimeThenReplay(t *testing.T) {
	client := newTestClient(t)
	store := signedredis.New(client, time.Minute, signedredis.WithKeyPrefix(uniquePrefix(t)))

	first, err := store.SeenOrStore("nonce-A")
	if err != nil {
		t.Fatalf("first SeenOrStore: %v", err)
	}
	if !first {
		t.Fatal("expected first observation to return true")
	}

	second, err := store.SeenOrStore("nonce-A")
	if err != nil {
		t.Fatalf("second SeenOrStore: %v", err)
	}
	if second {
		t.Fatal("expected replayed nonce to return false")
	}
}

func TestSeenOrStore_DistinctNoncesIndependent(t *testing.T) {
	client := newTestClient(t)
	store := signedredis.New(client, time.Minute, signedredis.WithKeyPrefix(uniquePrefix(t)))

	for i, n := range []string{"a", "b", "c", "d"} {
		ok, err := store.SeenOrStore(n)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("call %d: expected true for fresh nonce %q", i, n)
		}
	}
}

func TestSeenOrStore_TTLExpiryAllowsReuse(t *testing.T) {
	client := newTestClient(t)
	// Short TTL so the test runs quickly. 1s is the smallest unit
	// SET EX accepts.
	store := signedredis.New(client, time.Second, signedredis.WithKeyPrefix(uniquePrefix(t)))

	first, err := store.SeenOrStore("ttl-nonce")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if !first {
		t.Fatal("expected first observation to return true")
	}

	// Replay inside TTL window — must reject.
	second, err := store.SeenOrStore("ttl-nonce")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second {
		t.Fatal("expected replay inside TTL to return false")
	}

	// Wait for expiry. 1.5s is enough margin for Redis's resolution.
	time.Sleep(1500 * time.Millisecond)

	third, err := store.SeenOrStore("ttl-nonce")
	if err != nil {
		t.Fatalf("third: %v", err)
	}
	if !third {
		t.Fatal("expected post-TTL probe to be admitted as fresh")
	}
}

func TestSeenOrStore_EmptyNonceRejected(t *testing.T) {
	client := newTestClient(t)
	store := signedredis.New(client, time.Minute, signedredis.WithKeyPrefix(uniquePrefix(t)))

	ok, err := store.SeenOrStore("")
	if err == nil {
		t.Fatal("expected error on empty nonce")
	}
	if ok {
		t.Fatal("empty nonce must not be admitted")
	}
}

func TestSeenOrStore_ConcurrentSameNonceExactlyOneWins(t *testing.T) {
	client := newTestClient(t)
	store := signedredis.New(client, time.Minute, signedredis.WithKeyPrefix(uniquePrefix(t)))

	const callers = 64
	var wg sync.WaitGroup
	var winners atomic.Int64
	var errs atomic.Int64

	start := make(chan struct{})
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			<-start
			ok, err := store.SeenOrStore("contended-nonce")
			if err != nil {
				errs.Add(1)
				return
			}
			if ok {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if errs.Load() != 0 {
		t.Fatalf("unexpected errors during contention: %d", errs.Load())
	}
	if got := winners.Load(); got != 1 {
		t.Fatalf("exactly one caller must win the SET NX race; got %d", got)
	}
}

func TestSeenOrStore_AcrossClientsSharesState(t *testing.T) {
	// The whole point of the redis backend: a nonce admitted by
	// replica A is rejected by replica B.
	clientA := newTestClient(t)
	clientB := newClientFor(t)
	t.Cleanup(func() { _ = clientB.Close() })

	prefix := uniquePrefix(t)
	a := signedredis.New(clientA, time.Minute, signedredis.WithKeyPrefix(prefix))
	b := signedredis.New(clientB, time.Minute, signedredis.WithKeyPrefix(prefix))

	first, err := a.SeenOrStore("shared-nonce")
	if err != nil {
		t.Fatalf("a.SeenOrStore: %v", err)
	}
	if !first {
		t.Fatal("first replica must admit fresh nonce")
	}

	second, err := b.SeenOrStore("shared-nonce")
	if err != nil {
		t.Fatalf("b.SeenOrStore: %v", err)
	}
	if second {
		t.Fatal("second replica must reject the same nonce")
	}
}

func TestSeenOrStore_KeyPrefixIsolatesAudiences(t *testing.T) {
	client := newTestClient(t)
	a := signedredis.New(client, time.Minute, signedredis.WithKeyPrefix("audA:"+uniquePrefix(t)))
	b := signedredis.New(client, time.Minute, signedredis.WithKeyPrefix("audB:"+uniquePrefix(t)))

	okA, err := a.SeenOrStore("nonce-shared-text")
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	okB, err := b.SeenOrStore("nonce-shared-text")
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if !okA || !okB {
		t.Fatalf("two prefixed stores must observe independent state, got a=%v b=%v", okA, okB)
	}
}

func TestWithCallTimeout_Compiles(t *testing.T) {
	client := newTestClient(t)
	store := signedredis.New(client, time.Minute,
		signedredis.WithKeyPrefix(uniquePrefix(t)),
		signedredis.WithCallTimeout(500*time.Millisecond),
	)
	ok, err := store.SeenOrStore("smoke-nonce")
	if err != nil {
		t.Fatalf("smoke: %v", err)
	}
	if !ok {
		t.Fatal("smoke: expected admit")
	}
}
