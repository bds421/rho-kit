package netutil_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/security/v2/netutil"
)

func TestIsLoopbackHost_Literals(t *testing.T) {
	assert.True(t, netutil.IsLoopbackHost(""))
	assert.True(t, netutil.IsLoopbackHost("127.0.0.1"))
	assert.True(t, netutil.IsLoopbackHost("::1"))
	assert.True(t, netutil.IsLoopbackHost("[::1]"))
	assert.True(t, netutil.IsLoopbackHost("localhost"))
	assert.True(t, netutil.IsLoopbackHost("LOCALHOST"))

	assert.False(t, netutil.IsLoopbackHost("[]"))
	assert.False(t, netutil.IsLoopbackHost("["))
	assert.False(t, netutil.IsLoopbackHost("]"))
	assert.False(t, netutil.IsLoopbackHost("0.0.0.0"))
	assert.False(t, netutil.IsLoopbackHost("10.0.0.1"))
	assert.False(t, netutil.IsLoopbackHost("::"))
}

func TestIsLoopbackHostLiteral_NoDNS(t *testing.T) {
	assert.True(t, netutil.IsLoopbackHostLiteral("localhost"))
	assert.True(t, netutil.IsLoopbackHostLiteral("127.0.0.1"))
	// Unresolvable / non-literal hostnames must not be treated as loopback
	// without DNS in the literal path.
	assert.False(t, netutil.IsLoopbackHostLiteral("example.invalid.loopback-test"))
}

func TestIsLoopbackAddr(t *testing.T) {
	assert.True(t, netutil.IsLoopbackAddr(""))
	assert.True(t, netutil.IsLoopbackAddr("localhost:6379"))
	assert.True(t, netutil.IsLoopbackAddr("127.0.0.1:6379"))
	assert.True(t, netutil.IsLoopbackAddr("[::1]:6379"))
	assert.True(t, netutil.IsLoopbackAddr("localhost"))
	assert.False(t, netutil.IsLoopbackAddr("redis.internal:6379"))
	assert.False(t, netutil.IsLoopbackAddr("10.0.0.5:6379"))
}
