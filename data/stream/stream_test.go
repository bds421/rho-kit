package stream_test

import (
	"fmt"
	"strings"
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

func TestValidateName(t *testing.T) {
	assert.NoError(t, stream.ValidateName("orders.events"))
	assert.ErrorIs(t, stream.ValidateName(""), stream.ErrInvalidName)
	assert.ErrorIs(t, stream.ValidateName("bad name"), stream.ErrInvalidName)
	assert.ErrorIs(t, stream.ValidateName("bad\nname"), stream.ErrInvalidName)
	assert.ErrorIs(t, stream.ValidateName(strings.Repeat("a", stream.MaxNameBytes+1)), stream.ErrInvalidName)
}

func TestValidatePayload(t *testing.T) {
	assert.NoError(t, stream.ValidatePayload(nil))
	assert.NoError(t, stream.ValidatePayload(map[string]string{"k": "v"}))
	assert.ErrorIs(t, stream.ValidatePayload(map[string]string{"": "v"}), stream.ErrInvalidPayload)
	assert.ErrorIs(t, stream.ValidatePayload(map[string]string{"bad key": "v"}), stream.ErrInvalidPayload)

	big := make(map[string]string, stream.MaxPayloadFields+1)
	for i := 0; i < stream.MaxPayloadFields+1; i++ {
		big[fmt.Sprintf("k%d", i)] = "v"
	}
	assert.ErrorIs(t, stream.ValidatePayload(big), stream.ErrInvalidPayload)
}
