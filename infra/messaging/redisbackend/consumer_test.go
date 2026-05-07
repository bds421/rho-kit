package redisbackend

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewConsumer_PanicsOnNilConsumer(t *testing.T) {
	assert.Panics(t, func() {
		NewConsumer(nil, slog.Default())
	})
}
