package config

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatchable_GetReturnsInitialValue(t *testing.T) {
	w := NewWatchable("hello")
	assert.Equal(t, "hello", w.Get())
}

func TestWatchable_SetUpdatesValue(t *testing.T) {
	w := NewWatchable(42)
	w.Set(99)
	assert.Equal(t, 99, w.Get())
}

func TestWatchable_OnChangeCallbackFires(t *testing.T) {
	w := NewWatchable("old")

	var gotOld, gotNew string
	w.OnChange(func(old, new string) {
		gotOld = old
		gotNew = new
	})

	w.Set("new")

	assert.Equal(t, "old", gotOld)
	assert.Equal(t, "new", gotNew)
}

func TestWatchable_MultipleSubscribersAllCalled(t *testing.T) {
	w := NewWatchable(0)

	var calls [3]bool
	for i := range 3 {
		idx := i
		w.OnChange(func(_, _ int) {
			calls[idx] = true
		})
	}

	w.Set(1)

	for i, called := range calls {
		assert.True(t, called, "subscriber %d was not called", i)
	}
}

func TestWatchable_SubscriberReceivesCorrectValues(t *testing.T) {
	type cfg struct {
		Port int
		Host string
	}

	initial := cfg{Port: 8080, Host: "localhost"}
	w := NewWatchable(initial)

	var captured []cfg
	w.OnChange(func(old, new cfg) {
		captured = append(captured, old, new)
	})

	updated := cfg{Port: 9090, Host: "example.com"}
	w.Set(updated)

	require.Len(t, captured, 2)
	assert.Equal(t, initial, captured[0])
	assert.Equal(t, updated, captured[1])
}

func TestWatchable_ConcurrentGetSet(t *testing.T) {
	w := NewWatchable(0)

	const goroutines = 100
	const iterations = 1000

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	// Writers
	for range goroutines {
		go func() {
			defer wg.Done()
			for j := range iterations {
				w.Set(j)
			}
		}()
	}

	// Readers
	var readCount atomic.Int64
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				_ = w.Get()
				readCount.Add(1)
			}
		}()
	}

	wg.Wait()
	assert.Equal(t, int64(goroutines*iterations), readCount.Load())
}

func TestWatchable_OnChangeRegisteredDuringSet(t *testing.T) {
	// Verify that subscribers added during a Set call do not cause a
	// data race. The new subscriber should not be called for the
	// current Set since it was not registered yet.
	w := NewWatchable(0)

	var innerCalled atomic.Bool
	w.OnChange(func(_, _ int) {
		// Register another subscriber mid-notification.
		w.OnChange(func(_, _ int) {
			innerCalled.Store(true)
		})
	})

	w.Set(1)
	// The inner subscriber was registered during Set(1), so it should
	// NOT have been called for that Set. It should fire on the next Set.
	assert.False(t, innerCalled.Load(), "inner subscriber should not fire during the Set that registered it")

	w.Set(2)
	assert.True(t, innerCalled.Load(), "inner subscriber should fire on subsequent Set")
}
