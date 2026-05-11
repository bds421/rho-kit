package stream_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/data/v2/stream"
)

func TestSentinels(t *testing.T) {
	assert.ErrorIs(t, stream.ErrInvalidStream, stream.ErrInvalidStream)
}
