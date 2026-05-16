package etcd

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

// fakeSession satisfies the unexported [session] interface for unit
// tests. Tests trigger leader loss by calling Expire().
type fakeSession struct {
	done   chan struct{}
	closed atomic.Bool
}

func newFakeSession() *fakeSession {
	return &fakeSession{done: make(chan struct{})}
}

func (s *fakeSession) Done() <-chan struct{} { return s.done }

func (s *fakeSession) Close() error {
	if s.closed.CompareAndSwap(false, true) {
		select {
		case <-s.done:
		default:
			close(s.done)
		}
	}
	return nil
}

// Expire simulates an etcd lease loss by closing the Done channel.
// Safe to call multiple times.
func (s *fakeSession) Expire() {
	if s.closed.CompareAndSwap(false, true) {
		close(s.done)
	}
}

// fakeElection satisfies the unexported [election] interface.
type fakeElection struct {
	campaignBlock chan struct{} // when set, Campaign blocks until receive
	campaignErr   error
	resignErr     error
	campaignedVal atomic.Pointer[string]
	resigned      atomic.Bool
}

func (e *fakeElection) Campaign(ctx context.Context, val string) error {
	e.campaignedVal.Store(&val)
	if e.campaignBlock != nil {
		select {
		case <-e.campaignBlock:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return e.campaignErr
}

func (e *fakeElection) Resign(_ context.Context) error {
	e.resigned.Store(true)
	return e.resignErr
}

// recordingFactory returns a sessionFactory that hands out the
// supplied (session, election) pair on first call and then errors on
// subsequent calls — for tests that exercise exactly one term.
type recordingFactory struct {
	sessions  []*fakeSession
	elections []*fakeElection
	calls     atomic.Int32
}

func (r *recordingFactory) factory() sessionFactory {
	return func(ctx context.Context, _ int, _ string) (session, election, error) {
		idx := int(r.calls.Add(1)) - 1
		if idx >= len(r.sessions) {
			return nil, nil, errors.New("recordingFactory: no more sessions configured")
		}
		return r.sessions[idx], r.elections[idx], nil
	}
}

func TestNew_PanicsOnNilClient(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil client")
		}
	}()
	New(nil, "/kit/test", "id-1")
}

func TestNew_PanicsOnEmptyElectionKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty election key")
		}
	}()
	New(&clientv3.Client{}, "", "id-1")
}

func TestNew_PanicsOnElectionKeyMissingSlash(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on election key without leading /")
		}
	}()
	New(&clientv3.Client{}, "kit/test", "id-1")
}

func TestNew_PanicsOnElectionKeyTooLong(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on oversized election key")
		}
	}()
	New(&clientv3.Client{}, "/"+strings.Repeat("x", maxElectionKeyLen), "id-1")
}

func TestNew_PanicsOnElectionKeyControlBytes(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on election key with control bytes")
		}
	}()
	New(&clientv3.Client{}, "/kit/\x01test", "id-1")
}

func TestNew_PanicsOnEmptyIdentity(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty identity")
		}
	}()
	New(&clientv3.Client{}, "/kit/test", "")
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	New(&clientv3.Client{}, "/kit/test", "id-1", nil)
}

func TestNew_OptionPanics(t *testing.T) {
	cases := []struct {
		name string
		fn   func()
	}{
		{"WithLeaseTTLSeconds(0)", func() { WithLeaseTTLSeconds(0) }},
		{"WithLeaseTTLSeconds(-1)", func() { WithLeaseTTLSeconds(-1) }},
		{"WithReacquireBackoff(0)", func() { WithReacquireBackoff(0) }},
		{"WithCallbackDrainWarnInterval(0)", func() { WithCallbackDrainWarnInterval(0) }},
		{"WithCallbackDrainTimeout(0)", func() { WithCallbackDrainTimeout(0) }},
		{"WithMetrics(nil)", func() { WithMetrics(nil) }},
		{"WithRegisterer(nil)", func() { WithRegisterer(nil) }},
		{"NewMetrics(nil-option)", func() { NewMetrics(nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Fatalf("expected panic from %s", tc.name)
				}
			}()
			tc.fn()
		})
	}
}

func TestRun_RejectsNilContext(t *testing.T) {
	e := newElectorForTest(nil)
	// Deliberate nil-context test: callers must not pass nil — Run
	// rejects with an explicit error rather than dereferencing.
	// staticcheck would normally flag this; the //nolint pragma keeps
	// the lint surface clean without weakening the assertion.
	//nolint:staticcheck // SA1012: testing the nil-ctx rejection path on purpose.
	err := e.Run(nil, leaderelection.Callbacks{})
	if err == nil {
		t.Fatal("expected error for nil context")
	}
}

func TestRun_RejectsDoubleInvocation(t *testing.T) {
	e := newElectorForTest(nil)
	e.started.Store(true) // simulate prior Run
	err := e.Run(context.Background(), leaderelection.Callbacks{})
	if err == nil || !strings.Contains(err.Error(), "Run already invoked") {
		t.Fatalf("expected double-Run guard, got %v", err)
	}
}

// TestRun_AcquireHoldRelease verifies the happy path: Campaign
// succeeds, OnAcquired is invoked with a non-nil ctx, the leader
// flag toggles, OnLost runs after OnAcquired returns, and Run exits
// when the caller ctx is cancelled.
func TestRun_AcquireHoldRelease(t *testing.T) {
	sess := newFakeSession()
	elect := &fakeElection{}
	rf := &recordingFactory{sessions: []*fakeSession{sess}, elections: []*fakeElection{elect}}

	e := newElectorForTest(rf.factory())

	acquired := make(chan struct{})
	lost := make(chan struct{})
	var capturedCtx atomic.Pointer[context.Context]

	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			capturedCtx.Store(&ctx)
			close(acquired)
			<-ctx.Done()
		},
		OnLost: func() {
			close(lost)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, cb) }()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("OnAcquired never invoked")
	}

	if !e.IsLeader() {
		t.Fatal("IsLeader must report true during acquired term")
	}

	cancel()

	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatal("OnLost never invoked after ctx cancel")
	}

	if e.IsLeader() {
		t.Fatal("IsLeader must report false after term ended")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after ctx cancel")
	}

	if !elect.resigned.Load() {
		t.Fatal("Resign must be called on planned shutdown so peers do not wait the lease TTL")
	}
}

// TestRun_SessionExpiryCancelsLeaderCtx verifies the etcd-specific
// teardown path: when the session's Done channel fires (lease lost),
// the leader ctx is cancelled so OnAcquired drains promptly.
func TestRun_SessionExpiryCancelsLeaderCtx(t *testing.T) {
	sess := newFakeSession()
	elect := &fakeElection{}
	rf := &recordingFactory{sessions: []*fakeSession{sess}, elections: []*fakeElection{elect}}

	e := newElectorForTest(rf.factory())
	e.reacquireBackoff = 50 * time.Millisecond

	leaderCtxObserved := make(chan struct{})
	lost := make(chan struct{})
	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			<-ctx.Done()
			close(leaderCtxObserved)
		},
		OnLost: func() { close(lost) },
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = e.Run(ctx, cb) }()

	// Wait briefly for the elector to enter the term, then simulate
	// lease loss.
	time.Sleep(100 * time.Millisecond)
	sess.Expire()

	select {
	case <-leaderCtxObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("leader ctx was not cancelled when session expired")
	}

	select {
	case <-lost:
	case <-time.After(2 * time.Second):
		t.Fatal("OnLost did not run after session expiry")
	}
}

// TestRun_OnAcquiredPanic_IsCaptured asserts that a panic in
// OnAcquired does not crash the elector goroutine; the panic value is
// folded into the joined term error.
func TestRun_OnAcquiredPanic_IsCaptured(t *testing.T) {
	sess := newFakeSession()
	elect := &fakeElection{}
	rf := &recordingFactory{sessions: []*fakeSession{sess}, elections: []*fakeElection{elect}}

	e := newElectorForTest(rf.factory())

	cb := leaderelection.Callbacks{
		OnAcquired: func(_ context.Context) {
			panic("simulated user panic")
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		// Cancel after a brief window so the elector exits the
		// retry loop.
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	// Run should return without crashing the goroutine.
	_ = e.Run(ctx, cb)
}

// TestRun_DrainTimeout_ReturnsSentinel verifies WithCallbackDrainTimeout
// surfaces ErrCallbackDrainTimeout when OnAcquired hangs.
func TestRun_DrainTimeout_ReturnsSentinel(t *testing.T) {
	sess := newFakeSession()
	elect := &fakeElection{}
	rf := &recordingFactory{sessions: []*fakeSession{sess}, elections: []*fakeElection{elect}}

	e := newElectorForTest(rf.factory())
	e.drainTimeout = 100 * time.Millisecond
	e.drainWarnTick = 50 * time.Millisecond

	hang := make(chan struct{})
	cb := leaderelection.Callbacks{
		OnAcquired: func(_ context.Context) {
			<-hang // never closes
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, cb) }()

	// Cancel ctx after the elector is leader so the loop transitions
	// to the drain phase.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrCallbackDrainTimeout) {
			t.Fatalf("expected ErrCallbackDrainTimeout in chain, got %v", err)
		}
	case <-time.After(2 * time.Second):
		close(hang) // release the orphan
		t.Fatal("Run did not return within drain-timeout budget")
	}
	close(hang)
}

// TestRun_ContextCancelDuringCampaign exits cleanly when the caller
// ctx is cancelled while Campaign is still blocking.
func TestRun_ContextCancelDuringCampaign(t *testing.T) {
	sess := newFakeSession()
	elect := &fakeElection{campaignBlock: make(chan struct{})}
	rf := &recordingFactory{sessions: []*fakeSession{sess}, elections: []*fakeElection{elect}}

	e := newElectorForTest(rf.factory())

	cb := leaderelection.Callbacks{
		OnAcquired: func(_ context.Context) {
			t.Fatal("OnAcquired must not run when ctx cancels before Campaign returns")
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, cb) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled in chain, got %v", err)
		}
	case <-time.After(2 * time.Second):
		close(elect.campaignBlock)
		t.Fatal("Run did not return within budget after ctx cancel")
	}
	close(elect.campaignBlock)
}

func TestRun_OnLostPanic_DoesNotCrash(t *testing.T) {
	sess := newFakeSession()
	elect := &fakeElection{}
	rf := &recordingFactory{sessions: []*fakeSession{sess}, elections: []*fakeElection{elect}}

	e := newElectorForTest(rf.factory())

	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) { <-ctx.Done() },
		OnLost:     func() { panic("simulated OnLost panic") },
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	// Run should return without crashing; the OnLost panic is
	// folded into the term error.
	_ = e.Run(ctx, cb)
}

// newElectorForTest builds an Elector configured to use a supplied
// (test-injected) sessionFactory. Skips the New() validation chain
// because the production client is not exercised in unit tests.
func newElectorForTest(f sessionFactory) *Elector {
	return &Elector{
		client:           nil,
		electionKey:      "/kit/test/leader",
		identity:         "unit-test",
		leaseTTLSeconds:  defaultLeaseTTLSeconds,
		reacquireBackoff: 50 * time.Millisecond,
		drainWarnTick:    defaultDrainWarnTick,
		logger:           slog.Default(),
		sessionFactory:   f,
	}
}
