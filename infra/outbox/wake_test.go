package outbox_test

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bds421/rho-kit/infra/v2/outbox"
)

type observedWakeStore struct {
	*fakeStore
	fetched chan struct{}
}

func (s *observedWakeStore) FetchPending(ctx context.Context, limit int) ([]outbox.Entry, error) {
	entries, err := s.fakeStore.FetchPending(ctx, limit)
	select {
	case s.fetched <- struct{}{}:
	default:
	}
	return entries, err
}

func waitWakePoll(t *testing.T, s *observedWakeStore) {
	t.Helper()
	select {
	case <-s.fetched:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not poll")
	}
}

func runWakeRelay(t *testing.T, s *observedWakeStore, interval time.Duration) *outbox.Relay {
	t.Helper()
	relay := outbox.NewRelay(s, &fakePublisher{}, slog.New(slog.NewTextHandler(io.Discard, nil)), outbox.WithPollInterval(interval))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- relay.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("relay failed to stop")
		}
	})
	waitWakePoll(t, s)
	return relay
}

func TestRelayWakePollsBeforeLongTimerAndDoesNotBlockCallers(t *testing.T) {
	store := &observedWakeStore{fakeStore: &fakeStore{}, fetched: make(chan struct{}, 1000)}
	relay := runWakeRelay(t, store, time.Hour)
	var callers sync.WaitGroup
	for range 20 {
		callers.Go(func() {
			for range 100 {
				relay.Wake()
			}
		})
	}
	callers.Wait()
	waitWakePoll(t, store)
}

func TestRelayPeriodicPollRecoversWithoutWake(t *testing.T) {
	store := &observedWakeStore{fakeStore: &fakeStore{}, fetched: make(chan struct{}, 10)}
	runWakeRelay(t, store, 20*time.Millisecond)
	waitWakePoll(t, store)
}

func TestRelayWakeBeforeStartAndAfterStopDoesNotPerformIO(t *testing.T) {
	store := &observedWakeStore{fakeStore: &fakeStore{}, fetched: make(chan struct{}, 10)}
	relay := outbox.NewRelay(store, &fakePublisher{}, nil)
	for range 100 {
		relay.Wake()
	}
	if len(store.fetched) != 0 {
		t.Fatal("Wake performed I/O before Start")
	}
	if err := relay.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		relay.Wake()
	}
	if len(store.fetched) != 0 {
		t.Fatal("Wake performed I/O after Stop")
	}
}

type heldWakeStore struct {
	*fakeStore
	entered chan int
	release chan struct{}
	polls   int
}

func (s *heldWakeStore) FetchPending(ctx context.Context, limit int) ([]outbox.Entry, error) {
	s.polls++
	s.entered <- s.polls
	select {
	case <-s.release:
		return s.fakeStore.FetchPending(ctx, limit)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRelayWakeDuringPollCoalescesIntoOneDeferredPoll(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &heldWakeStore{fakeStore: &fakeStore{}, entered: make(chan int, 3), release: make(chan struct{})}
		relay := outbox.NewRelay(store, &fakePublisher{}, slog.New(slog.NewTextHandler(io.Discard, nil)), outbox.WithPollInterval(time.Hour))
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- relay.Start(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		if poll := <-store.entered; poll != 1 {
			t.Fatal("unexpected initial poll", poll)
		}
		// The first poll is held inside the store: hints must return without
		// starting parallel I/O, and a burst must retain only one later poll.
		for range 1000 {
			relay.Wake()
		}
		select {
		case poll := <-store.entered:
			t.Fatal("hint started a concurrent poll", poll)
		default:
		}
		store.release <- struct{}{}
		select {
		case poll := <-store.entered:
			if poll != 2 {
				t.Fatal("unexpected deferred poll", poll)
			}
		case <-time.After(time.Second):
			t.Fatal("hint during a poll was lost")
		}
		store.release <- struct{}{}
		synctest.Wait()
		select {
		case poll := <-store.entered:
			t.Fatal("burst scheduled more than one deferred poll", poll)
		default:
		}
	})
}
