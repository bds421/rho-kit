package amqpbackend

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestDeclareExchanges_ValidatesBeforeOpeningChannel(t *testing.T) {
	err := DeclareExchanges(noopConnector{}, messaging.ExchangeSpec{
		Exchange:     "events\nprod",
		ExchangeType: messaging.ExchangeDirect,
	})

	assert.ErrorIs(t, err, messaging.ErrInvalidRoute)
	assert.NotContains(t, err.Error(), "noop")
}

func TestDeclareExchanges_RejectsUnsupportedExchangeTypeBeforeOpeningChannel(t *testing.T) {
	err := DeclareExchanges(noopConnector{}, messaging.ExchangeSpec{
		Exchange:     "events",
		ExchangeType: "custom",
	})

	assert.Contains(t, err.Error(), "unsupported exchange type")
	assert.NotContains(t, err.Error(), "custom")
	assert.NotContains(t, err.Error(), "noop")
}

func TestDeclareAll_DoesNotMutateCallerSpecsBeforeOpeningChannel(t *testing.T) {
	specs := []messaging.BindingSpec{{
		Exchange:     "events",
		ExchangeType: messaging.ExchangeDirect,
		Queue:        "q",
		RoutingKey:   "rk",
	}}

	_, err := DeclareAll(noopConnector{}, specs...)
	assert.Error(t, err)
	assert.Nil(t, specs[0].Retry)
}
