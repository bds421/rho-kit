package messaging_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestValidatePublishRoute(t *testing.T) {
	valid := []struct {
		exchange   string
		routingKey string
	}{
		{"orders", "order.created"},
		{"orders.v1", "created"},
		{"fanout", ""},
		{strings.Repeat("x", messaging.MaxRouteNameBytes), strings.Repeat("y", messaging.MaxRouteNameBytes)},
	}
	for _, tt := range valid {
		t.Run("valid "+tt.exchange+"/"+tt.routingKey, func(t *testing.T) {
			assert.NoError(t, messaging.ValidatePublishRoute(tt.exchange, tt.routingKey))
		})
	}

	invalid := []struct {
		name       string
		exchange   string
		routingKey string
	}{
		{name: "empty exchange", exchange: "", routingKey: "rk"},
		{name: "exchange newline", exchange: "orders\nprod", routingKey: "rk"},
		{name: "exchange space", exchange: "orders prod", routingKey: "rk"},
		{name: "exchange invalid utf8", exchange: string([]byte{0xff, 0xfe}), routingKey: "rk"},
		{name: "exchange too long", exchange: strings.Repeat("x", messaging.MaxRouteNameBytes+1), routingKey: "rk"},
		{name: "routing key newline", exchange: "orders", routingKey: "created\nnext"},
		{name: "routing key space", exchange: "orders", routingKey: "created next"},
		{name: "routing key invalid utf8", exchange: "orders", routingKey: string([]byte{0xff})},
		{name: "routing key too long", exchange: "orders", routingKey: strings.Repeat("x", messaging.MaxRouteNameBytes+1)},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			err := messaging.ValidatePublishRoute(tt.exchange, tt.routingKey)
			assert.ErrorIs(t, err, messaging.ErrInvalidRoute)
			if strings.Contains(tt.name, "too long") {
				assert.NotContains(t, err.Error(), "255")
				assert.NotContains(t, err.Error(), "256")
			}
		})
	}
}

func TestValidatePublishContext(t *testing.T) {
	assert.NoError(t, messaging.ValidatePublishContext(context.Background()))
	var nilCtx context.Context
	assert.ErrorIs(t, messaging.ValidatePublishContext(nilCtx), messaging.ErrInvalidPublishContext)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := messaging.ValidatePublishContext(ctx)
	assert.True(t, errors.Is(err, context.Canceled))
}
