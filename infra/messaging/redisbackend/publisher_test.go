package redisbackend

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewPublisher_PanicsOnNilProducer(t *testing.T) {
	assert.Panics(t, func() {
		NewPublisher(nil)
	})
}
