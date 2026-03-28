package grpcx_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/bds421/rho-kit/grpcx"
)

func TestWithTracingStatsHandler_ReturnsOption(t *testing.T) {
	opt := grpcx.WithTracingStatsHandler()
	assert.NotNil(t, opt)
}

func TestNewServer_WithTracing(t *testing.T) {
	srv := grpcx.NewServer(
		grpcx.WithTracingStatsHandler(),
	)
	assert.NotNil(t, srv)
	srv.Stop()
}
