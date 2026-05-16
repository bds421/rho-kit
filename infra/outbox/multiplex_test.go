package outbox

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingPublisher struct {
	name  string
	calls atomic.Int64
	err   error
}

func (r *recordingPublisher) Publish(_ context.Context, _ Entry) error {
	r.calls.Add(1)
	return r.err
}

func TestMultiplex_RoutesByLongestPrefix(t *testing.T) {
	mux := NewMultiplex()
	short := &recordingPublisher{name: "short"}
	long := &recordingPublisher{name: "long"}
	mux.Register("orders.", short)
	mux.Register("orders.priority.", long)

	require.NoError(t, mux.Publish(context.Background(), Entry{Topic: "orders.priority.large"}))
	assert.Equal(t, int64(1), long.calls.Load())
	assert.Equal(t, int64(0), short.calls.Load())

	require.NoError(t, mux.Publish(context.Background(), Entry{Topic: "orders.regular"}))
	assert.Equal(t, int64(1), short.calls.Load())
}

func TestMultiplex_FallbackUsedWhenNoMatch(t *testing.T) {
	mux := NewMultiplex()
	primary := &recordingPublisher{name: "primary"}
	fallback := &recordingPublisher{name: "fallback"}
	mux.Register("orders.", primary)
	mux.SetFallback(fallback)

	require.NoError(t, mux.Publish(context.Background(), Entry{Topic: "billing.invoice"}))
	assert.Equal(t, int64(1), fallback.calls.Load())
	assert.Equal(t, int64(0), primary.calls.Load())
}

func TestMultiplex_NoRouteWithoutFallback(t *testing.T) {
	mux := NewMultiplex()
	mux.Register("orders.", &recordingPublisher{name: "primary"})

	err := mux.Publish(context.Background(), Entry{Topic: "billing.invoice"})
	assert.ErrorIs(t, err, ErrNoRoute)
}

func TestMultiplex_WrapsRouteErrorWithPrefix(t *testing.T) {
	mux := NewMultiplex()
	mux.Register("orders.", &recordingPublisher{name: "primary", err: errors.New("kafka down")})

	err := mux.Publish(context.Background(), Entry{Topic: "orders.created"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `route "orders."`,
		"route prefix must appear so retry metrics show which side is failing")
}

func TestMultiplex_RegisterPanicsOnEmptyPrefix(t *testing.T) {
	mux := NewMultiplex()
	assert.Panics(t, func() { mux.Register("", &recordingPublisher{}) })
}

func TestMultiplex_RegisterPanicsOnNilPublisher(t *testing.T) {
	mux := NewMultiplex()
	assert.Panics(t, func() { mux.Register("orders.", nil) })
}

func TestMultiplex_SetFallbackNilClears(t *testing.T) {
	mux := NewMultiplex()
	mux.SetFallback(&recordingPublisher{name: "fallback"})
	mux.SetFallback(nil)

	err := mux.Publish(context.Background(), Entry{Topic: "billing.invoice"})
	assert.ErrorIs(t, err, ErrNoRoute, "clearing fallback must restore ErrNoRoute")
}

func TestMultiplex_ReRegistrationReplaces(t *testing.T) {
	mux := NewMultiplex()
	first := &recordingPublisher{name: "first"}
	second := &recordingPublisher{name: "second"}
	mux.Register("orders.", first)
	mux.Register("orders.", second)

	require.NoError(t, mux.Publish(context.Background(), Entry{Topic: "orders.regular"}))
	assert.Equal(t, int64(0), first.calls.Load())
	assert.Equal(t, int64(1), second.calls.Load(),
		"re-registration replaces the previous publisher")
}
