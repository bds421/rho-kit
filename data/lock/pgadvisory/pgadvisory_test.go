package pgadvisory

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/lock"
)

// fakeDriver is a minimal database/sql driver that simulates the parts of
// Postgres advisory-lock behaviour the Locker depends on. It is intentionally
// in-process and dependency-free so the package's resource-leak invariants can
// be tested without a real Postgres.
//
// The single source of truth is *fakeState, shared across all physical
// connections produced by one driver instance, so a test can assert whether an
// advisory lock outlived the connection that took it.
type fakeDriver struct {
	state *fakeState
}

type fakeState struct {
	mu sync.Mutex
	// held maps advisory-lock id -> number of live sessions holding it.
	held map[int64]int
	// openConns counts physical connections currently checked out from the
	// driver (i.e. not yet driver-closed).
	openConns int32
}

func newFakeState() *fakeState {
	return &fakeState{held: make(map[int64]int)}
}

func (s *fakeState) lockCount(id int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.held[id]
}

func (s *fakeState) openConnCount() int32 {
	return atomic.LoadInt32(&s.openConns)
}

func (d *fakeDriver) Open(string) (driver.Conn, error) {
	atomic.AddInt32(&d.state.openConns, 1)
	return &fakeConn{state: d.state}, nil
}

// fakeConnBehavior lets a test inject query-time failures that mimic a context
// cancellation landing between the server executing the statement and the
// client reading the result.
type fakeConnBehavior struct {
	// failNextQueryAfterEffect, when true, applies the advisory-lock side
	// effect (as if the server ran it) but then returns an error to the
	// client, simulating a lost response / mid-flight cancellation.
	failNextQueryAfterEffect bool
}

type fakeConn struct {
	state    *fakeState
	behavior *fakeConnBehavior
	// locks tracks the advisory-lock ids this specific session holds, so
	// physical Close can release them the way a real Postgres session does
	// when its backend connection drops.
	locks  []int64
	closed bool
}

var _ driver.QueryerContext = (*fakeConn)(nil)

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("fakeConn: Prepare not supported")
}

func (c *fakeConn) Begin() (driver.Tx, error) {
	return nil, errors.New("fakeConn: Begin not supported")
}

func (c *fakeConn) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	atomic.AddInt32(&c.state.openConns, -1)
	// A dropped session releases every advisory lock it held — this is the
	// real-Postgres behaviour the leak fix relies on.
	c.state.mu.Lock()
	for _, id := range c.locks {
		c.state.held[id]--
		if c.state.held[id] == 0 {
			delete(c.state.held, id)
		}
	}
	c.state.mu.Unlock()
	c.locks = nil
	return nil
}

func (c *fakeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	id := int64(0)
	if len(args) > 0 {
		if v, ok := args[0].Value.(int64); ok {
			id = v
		}
	}

	switch query {
	case "SELECT pg_try_advisory_lock($1)":
		// Side effect first: the server grants the lock.
		c.state.mu.Lock()
		c.state.held[id]++
		c.state.mu.Unlock()
		c.locks = append(c.locks, id)
		if c.behavior != nil && c.behavior.failNextQueryAfterEffect {
			c.behavior.failNextQueryAfterEffect = false
			// Server granted, client never sees it.
			return nil, errors.New("fakeConn: simulated mid-flight failure")
		}
		return &boolRows{val: true}, nil

	case "SELECT pg_advisory_unlock($1)":
		if c.behavior != nil && c.behavior.failNextQueryAfterEffect {
			c.behavior.failNextQueryAfterEffect = false
			// Simulate the unlock never executing on the server (e.g. the
			// context was cancelled before the round trip completed). The
			// lock stays held.
			return nil, errors.New("fakeConn: simulated mid-flight failure")
		}
		c.state.mu.Lock()
		ok := c.state.held[id] > 0
		if ok {
			c.state.held[id]--
			if c.state.held[id] == 0 {
				delete(c.state.held, id)
			}
		}
		c.state.mu.Unlock()
		// Drop the id from this session's tracked locks.
		for i, held := range c.locks {
			if held == id {
				c.locks = append(c.locks[:i], c.locks[i+1:]...)
				break
			}
		}
		return &boolRows{val: ok}, nil

	default:
		return nil, errors.New("fakeConn: unexpected query: " + query)
	}
}

// boolRows yields a single row with a single boolean column.
type boolRows struct {
	val  bool
	done bool
}

func (r *boolRows) Columns() []string { return []string{"result"} }
func (r *boolRows) Close() error      { return nil }
func (r *boolRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = r.val
	return nil
}

// openFakeDB wires a *sql.DB backed by a single physical connection so tests
// can deterministically reason about whether that connection was returned to
// the pool or discarded.
func openFakeDB(t *testing.T) (*sql.DB, *fakeState, *fakeConnBehavior) {
	t.Helper()
	state := newFakeState()
	behavior := &fakeConnBehavior{}
	connector := &fakeConnector{driver: &fakeDriver{state: state}, behavior: behavior}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, state, behavior
}

// fakeConnector tags every connection it opens with a shared behavior knob.
type fakeConnector struct {
	driver   *fakeDriver
	behavior *fakeConnBehavior
}

func (c *fakeConnector) Connect(context.Context) (driver.Conn, error) {
	atomic.AddInt32(&c.driver.state.openConns, 1)
	return &fakeConn{state: c.driver.state, behavior: c.behavior}, nil
}

func (c *fakeConnector) Driver() driver.Driver { return c.driver }

// TestRelease_UnlockFailureDoesNotLeakHeldLockToPool reproduces the high-sev
// finding: when pg_advisory_unlock never executes (e.g. the Release ctx was
// cancelled mid-flight), returning the session connection to the pool leaves
// the advisory lock permanently held. The fix must discard the connection so
// Postgres terminates the session and frees the lock.
func TestRelease_UnlockFailureDoesNotLeakHeldLockToPool(t *testing.T) {
	db, state, behavior := openFakeDB(t)
	l := New(db)

	lk, ok, err := l.Acquire(context.Background(), "leaky-key")
	require.NoError(t, err)
	require.True(t, ok)

	id := keyToInt64("leaky-key")
	require.Equal(t, 1, state.lockCount(id), "lock should be held after Acquire")

	// Make the unlock round trip fail as if the response was lost.
	behavior.failNextQueryAfterEffect = true
	relErr := lk.Release(context.Background())
	require.Error(t, relErr, "Release should surface the unlock failure")

	// The bug: the connection (and therefore the advisory lock) is returned
	// to the pool with the lock still held. After the fix the session must be
	// torn down so the lock is freed.
	assert.Equal(t, 0, state.lockCount(id),
		"advisory lock must not survive a failed Release (would leak forever, no TTL)")
	assert.Equal(t, int32(0), state.openConnCount(),
		"the wedged session connection must be discarded, not pooled")
}

// TestAcquire_ScanFailureDoesNotLeakGrantedLockToPool reproduces the medium-sev
// finding: pg_try_advisory_lock succeeds server-side but the client read fails;
// returning the connection to the pool leaves the granted lock held.
func TestAcquire_ScanFailureDoesNotLeakGrantedLockToPool(t *testing.T) {
	db, state, behavior := openFakeDB(t)
	l := New(db)

	id := keyToInt64("acquire-leak")

	behavior.failNextQueryAfterEffect = true
	lk, ok, err := l.Acquire(context.Background(), "acquire-leak")
	require.Error(t, err, "Acquire should surface the scan failure")
	assert.Nil(t, lk)
	assert.False(t, ok)

	assert.Equal(t, 0, state.lockCount(id),
		"a lock granted server-side but lost client-side must not leak into the pool")
}

// TestRelease_SuccessReturnsConnToPool guards the happy path: a clean Release
// must release the lock and keep the connection reusable (not discard it).
func TestRelease_SuccessReturnsConnToPool(t *testing.T) {
	db, state, _ := openFakeDB(t)
	l := New(db)

	lk, ok, err := l.Acquire(context.Background(), "clean-key")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, lk.Release(context.Background()))

	id := keyToInt64("clean-key")
	assert.Equal(t, 0, state.lockCount(id), "lock released on clean Release")

	// The connection should still be usable: a follow-up Acquire must succeed
	// using the pooled connection.
	lk2, ok2, err2 := l.Acquire(context.Background(), "clean-key")
	require.NoError(t, err2)
	require.True(t, ok2)
	require.NoError(t, lk2.Release(context.Background()))
}

// TestRelease_DoubleReleaseReturnsErrLockLost guards the existing contract: a
// second Release returns ErrLockLost (conn already closed) and must not panic.
func TestRelease_DoubleReleaseReturnsErrLockLost(t *testing.T) {
	db, _, _ := openFakeDB(t)
	l := New(db)

	lk, ok, err := l.Acquire(context.Background(), "double")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, lk.Release(context.Background()))
	err2 := lk.Release(context.Background())
	assert.ErrorIs(t, err2, lock.ErrLockLost)
}
