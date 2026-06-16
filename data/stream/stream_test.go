package stream_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/stream"
)

func TestSentinels(t *testing.T) {
	// The package exposes a single sentinel; assert it is non-nil and carries
	// its documented message so the assertion can fail if the sentinel is
	// accidentally cleared or its contract message changes.
	require.Error(t, stream.ErrInvalidStream)
	assert.EqualError(t, stream.ErrInvalidStream, "stream: stream is not initialized")
}
