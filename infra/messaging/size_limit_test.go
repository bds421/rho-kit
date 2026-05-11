package messaging_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestMessageSizeLimiter_DefaultZeroValueIsSafe(t *testing.T) {
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"` + strings.Repeat("x", messaging.DefaultMaxMessageBytes) + `"`),
	}

	var limiter messaging.MessageSizeLimiter
	err := limiter.Check("events", "large.event", msg)

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
}

func TestMessageSizeLimiter_RouteOverride(t *testing.T) {
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"` + strings.Repeat("x", 64) + `"`),
	}
	limiter := messaging.NewMessageSizeLimiter(32).
		WithRouteMaxBytes("events", "large.event", 512)

	assert.NoError(t, limiter.Check("events", "large.event", msg))
	err := limiter.Check("events", "other.event", msg)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
}

func TestMessageSizeLimiter_UnlimitedDefaultKeepsRouteOverrides(t *testing.T) {
	msg := messaging.Message{
		ID:      "msg-1",
		Type:    "large.event",
		Payload: json.RawMessage(`"` + strings.Repeat("x", 64) + `"`),
	}
	limiter := messaging.UnlimitedMessageSizeLimiter().
		WithRouteMaxBytes("events", "small.event", 32)

	assert.NoError(t, limiter.Check("events", "large.event", msg))
	err := limiter.Check("events", "small.event", msg)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
}

func TestEstimateMessageBytes_IncludesHeaders(t *testing.T) {
	base := messaging.Message{ID: "msg-1", Type: "event", Payload: json.RawMessage(`{}`)}
	withHeader := base.WithHeader("large-header", strings.Repeat("x", 128))

	baseSize, err := messaging.EstimateMessageBytes(base)
	require.NoError(t, err)
	headerSize, err := messaging.EstimateMessageBytes(withHeader)
	require.NoError(t, err)

	assert.Greater(t, headerSize, baseSize+128)
}

func TestMessageSizeLimiter_InvalidConfigPanics(t *testing.T) {
	assert.Panics(t, func() { messaging.NewMessageSizeLimiter(-1) })
	assert.Panics(t, func() { messaging.DefaultMessageSizeLimiter().WithDefaultMaxBytes(0) })
	assert.Panics(t, func() { messaging.DefaultMessageSizeLimiter().WithRouteMaxBytes("", "rk", 1) })
	assert.Panics(t, func() { messaging.DefaultMessageSizeLimiter().WithRouteMaxBytes("events\nprod", "rk", 1) })
	assert.Panics(t, func() { messaging.DefaultMessageSizeLimiter().WithRouteMaxBytes("events", "bad key", 1) })
	assert.Panics(t, func() { messaging.DefaultMessageSizeLimiter().WithRouteMaxBytes("events", "rk", 0) })
}

func TestMessageTooLargeError_UnwrapsSentinel(t *testing.T) {
	err := &messaging.MessageTooLargeError{Size: 20, Limit: 10}

	assert.True(t, errors.Is(err, messaging.ErrMessageTooLarge))
}

func TestMessageTooLargeError_DoesNotReflectRoute(t *testing.T) {
	err := &messaging.MessageTooLargeError{
		Exchange:   "secret.exchange",
		RoutingKey: "secret.routing",
		Size:       20,
		Limit:      10,
	}

	assert.Contains(t, err.Error(), "message size 20 exceeds max 10")
	assert.NotContains(t, err.Error(), "secret.exchange")
	assert.NotContains(t, err.Error(), "secret.routing")
}
