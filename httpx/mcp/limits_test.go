package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/httpx/v2/mcp"
	"github.com/bds421/rho-kit/httpx/v2/middleware/ratelimit"
)

type chargesOut struct {
	Made int    `json:"made"`
	Err  string `json:"err"`
}

// chargingServer registers a tool that makes up to n operations, each
// charged, and says how many it made.
func chargingServer(t *testing.T, n int, opts ...mcp.ServerOption) http.Handler {
	t.Helper()
	s := mcp.NewServer(append([]mcp.ServerOption{mcp.WithBestEffortAuditOnMissingTenant()}, opts...)...)
	require.NoError(t, mcp.Register(s, "work", func(ctx context.Context, _ echoIn) (chargesOut, error) {
		out := chargesOut{}
		for range n {
			if err := mcp.Charge(ctx); err != nil {
				out.Err = err.Error()
				var limited *mcp.CallLimitedError
				if errors.As(err, &limited) && errors.Is(err, mcp.ErrCallLimited) && apperror.IsRateLimit(err) && limited.RetryAfter > 0 {
					out.Err = "limited"
				}
				return out, nil
			}
			out.Made++
		}
		return out, nil
	}))
	return s.HTTP()
}

func work(t *testing.T, h http.Handler, key string) chargesOut {
	t.Helper()
	res, rpc := callTool(t, h, "work", map[string]any{"message": "x"}, func(r *http.Request) { r.Header.Set("X-Key", key) })
	require.Nil(t, rpc.Error)
	var out chargesOut
	require.NoError(t, json.Unmarshal(res.StructuredContent, &out))
	return out
}

func TestWithCallLimits_EachOperationCountsPerKey(t *testing.T) {
	keyed := ratelimit.NewKeyedLimiter(3, time.Minute)
	h := chargingServer(t, 5, mcp.WithCallLimits(mcp.LimitByKey(keyed, func(r *http.Request) (string, bool) {
		return r.Header.Get("X-Key"), r.Header.Get("X-Key") != ""
	})))
	assert.Equal(t, chargesOut{Made: 3, Err: "limited"}, work(t, h, "ada"), "the fourth operation of one call is refused")
	assert.Equal(t, chargesOut{Made: 3, Err: "limited"}, work(t, h, "bob"), "another key has its own limit")
	assert.Equal(t, chargesOut{Made: 0, Err: "limited"}, work(t, h, "ada"))
	assert.NotEmpty(t, work(t, h, "").Err, "a request without a key is refused")
}

func TestWithCallLimits_EachOperationCountsPerIP(t *testing.T) {
	h := chargingServer(t, 3, mcp.WithCallLimits(mcp.LimitByIP(ratelimit.NewLimiter(2, time.Minute))))
	assert.Equal(t, chargesOut{Made: 2, Err: "limited"}, work(t, h, "ada"))
}

func TestCharge_WithoutLimitsAllows(t *testing.T) {
	assert.Equal(t, chargesOut{Made: 4}, work(t, chargingServer(t, 4), "ada"))
	require.NoError(t, mcp.Charge(t.Context()))
	assert.Panics(t, func() { mcp.WithCallLimits(nil) })
	assert.Panics(t, func() { mcp.LimitByIP(nil) })
	assert.Panics(t, func() { mcp.LimitByKey(nil, nil) })
}
