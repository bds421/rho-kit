package messaging_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/messaging"
)

func TestNewVersionedHandler_DispatchByVersion(t *testing.T) {
	called := -1

	handlers := map[messaging.SchemaVersion]messaging.Handler{
		0: func(_ context.Context, _ messaging.Delivery) error {
			called = 0
			return nil
		},
		1: func(_ context.Context, _ messaging.Delivery) error {
			called = 1
			return nil
		},
		2: func(_ context.Context, _ messaging.Delivery) error {
			called = 2
			return nil
		},
	}

	h := messaging.NewVersionedHandler(handlers)
	ctx := context.Background()

	for _, version := range []int{0, 1, 2} {
		called = -1
		d := messaging.Delivery{
			SchemaVersion: version,
			Message: messaging.Message{
				ID:            "msg-1",
				Type:          "test.event",
				Payload:       json.RawMessage(`{}`),
				SchemaVersion: version,
			},
		}
		err := h(ctx, d)
		require.NoError(t, err)
		assert.Equal(t, version, called)
	}
}

func TestNewVersionedHandler_UnknownVersionError(t *testing.T) {
	handlers := map[messaging.SchemaVersion]messaging.Handler{
		1: func(_ context.Context, _ messaging.Delivery) error {
			return nil
		},
	}

	h := messaging.NewVersionedHandler(handlers)

	d := messaging.Delivery{
		SchemaVersion: 99,
		Message: messaging.Message{
			ID:   "msg-1",
			Type: "test.event",
		},
	}

	err := h(context.Background(), d)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no handler registered for schema version 99")
	assert.Contains(t, err.Error(), "test.event")
	assert.Contains(t, err.Error(), "msg-1")
}

func TestNewVersionedHandler_PropagatesHandlerError(t *testing.T) {
	handlerErr := errors.New("processing failed")
	handlers := map[messaging.SchemaVersion]messaging.Handler{
		1: func(_ context.Context, _ messaging.Delivery) error {
			return handlerErr
		},
	}

	h := messaging.NewVersionedHandler(handlers)

	d := messaging.Delivery{
		SchemaVersion: 1,
		Message: messaging.Message{
			ID:            "msg-1",
			Type:          "test.event",
			SchemaVersion: 1,
		},
	}

	err := h(context.Background(), d)
	assert.ErrorIs(t, err, handlerErr)
}

func TestNewVersionedHandler_V0HandlesUnversionedMessages(t *testing.T) {
	var received bool
	handlers := map[messaging.SchemaVersion]messaging.Handler{
		0: func(_ context.Context, _ messaging.Delivery) error {
			received = true
			return nil
		},
		1: func(_ context.Context, _ messaging.Delivery) error {
			return errors.New("should not be called")
		},
	}

	h := messaging.NewVersionedHandler(handlers)

	// Unversioned message has SchemaVersion == 0 (zero value).
	d := messaging.Delivery{
		Message: messaging.Message{
			ID:   "msg-old",
			Type: "legacy.event",
		},
	}

	err := h(context.Background(), d)
	require.NoError(t, err)
	assert.True(t, received)
}

func TestNewVersionedHandler_PanicOnNilHandlers(t *testing.T) {
	assert.Panics(t, func() {
		messaging.NewVersionedHandler(nil)
	})
}

func TestNewVersionedHandler_PanicOnEmptyHandlers(t *testing.T) {
	assert.Panics(t, func() {
		messaging.NewVersionedHandler(map[messaging.SchemaVersion]messaging.Handler{})
	})
}

func TestNewVersionedHandler_PanicOnNilHandlerValue(t *testing.T) {
	assert.Panics(t, func() {
		messaging.NewVersionedHandler(map[messaging.SchemaVersion]messaging.Handler{
			1: nil,
		})
	})
}

func TestNewVersionedHandler_MapImmutability(t *testing.T) {
	var called bool
	handlers := map[messaging.SchemaVersion]messaging.Handler{
		1: func(_ context.Context, _ messaging.Delivery) error {
			called = true
			return nil
		},
	}

	h := messaging.NewVersionedHandler(handlers)

	// Mutate the original map after creation.
	delete(handlers, 1)

	d := messaging.Delivery{
		SchemaVersion: 1,
		Message: messaging.Message{
			ID:            "msg-1",
			Type:          "test.event",
			SchemaVersion: 1,
		},
	}

	err := h(context.Background(), d)
	require.NoError(t, err)
	assert.True(t, called, "handler should still work after original map mutation")
}
