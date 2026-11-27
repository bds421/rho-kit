package postgres

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithNotifySetsTheChannel(t *testing.T) {
	t.Parallel()
	s := &Store{}
	WithNotify("outbox_wake")(s)
	assert.Equal(t, "outbox_wake", s.notifyChannel)
	for _, name := range []string{"", strings.Repeat("c", 64), "a\x00b"} {
		assert.Panics(t, func() { WithNotify(name) }, "%q", name)
	}
	assert.Panics(t, func() { WithListenRetry(0) })
}

func TestListenRefusesMissingArguments(t *testing.T) {
	t.Parallel()
	require.Error(t, Listen(t.Context(), nil, "c", func() {}))
}
