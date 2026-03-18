package redisbackend

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewConsumer_NotNil(t *testing.T) {
	c := NewConsumer(nil, slog.Default())
	assert.NotNil(t, c)
}
