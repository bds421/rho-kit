package messaging_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestSentinels_Distinct(t *testing.T) {
	assert.NotErrorIs(t, messaging.ErrInvalidPublisher, messaging.ErrInvalidConsumer)
	assert.NotErrorIs(t, messaging.ErrInvalidConsumer, messaging.ErrInvalidPublisher)
}
