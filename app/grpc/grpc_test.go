package grpc

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	googrpc "google.golang.org/grpc"

	"github.com/bds421/rho-kit/app/v2"
	kitgrpcx "github.com/bds421/rho-kit/grpcx/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
)

func TestModule_PanicsOnNilRegistrar(t *testing.T) {
	assert.Panics(t, func() {
		_ = Module(nil, ":50051")
	})
}

func TestModule_PanicsOnEmptyAddr(t *testing.T) {
	assert.Panics(t, func() {
		_ = Module(func(_ *googrpc.Server) {}, "")
	})
}

func TestModule_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		_ = Module(func(_ *googrpc.Server) {}, ":50051", nil)
	})
}

func TestModule_Name(t *testing.T) {
	m := Module(func(_ *googrpc.Server) {}, ":50051")
	assert.Equal(t, "grpc", m.Name())
}

func TestModule_ClonesOptions(t *testing.T) {
	opts := []kitgrpcx.ServerOption{kitgrpcx.WithDefaultDeadline(time.Second)}
	m := Module(func(_ *googrpc.Server) {}, ":50051", ServerOption(opts...)).(*grpcModule)
	opts[0] = nil
	require.Len(t, m.opts, 1)
	assert.NotNil(t, m.opts[0])
}

func TestModule_InitCallsRegistrar(t *testing.T) {
	var registrarCalled atomic.Bool
	m := Module(func(_ *googrpc.Server) {
		registrarCalled.Store(true)
	}, ":50051").(*grpcModule)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mc := app.ModuleContext{
		Logger: logger,
		Runner: lifecycle.NewRunner(logger),
	}

	err := m.Init(context.Background(), mc)
	require.NoError(t, err)
	assert.True(t, registrarCalled.Load(), "registrar should have been called")
	assert.NotNil(t, m.server, "server should be initialized")
}

func TestModule_PopulateSetsResource(t *testing.T) {
	m := Module(func(_ *googrpc.Server) {}, ":50051").(*grpcModule)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	mc := app.ModuleContext{
		Logger: logger,
		Runner: lifecycle.NewRunner(logger),
	}
	require.NoError(t, m.Init(context.Background(), mc))

	infra := app.TestInfrastructure()
	m.Populate(&infra)
	srv := Server(infra)
	assert.NotNil(t, srv)
	assert.Same(t, m.server, srv)
}

func TestModule_HealthChecksEmpty(t *testing.T) {
	m := Module(func(_ *googrpc.Server) {}, ":50051").(*grpcModule)
	assert.Nil(t, m.HealthChecks())
}

func TestModule_StopBeforeInit(t *testing.T) {
	m := Module(func(_ *googrpc.Server) {}, ":50051").(*grpcModule)
	assert.NoError(t, m.Stop(context.Background()))
}

func TestModule_StartBeforeInitReturnsError(t *testing.T) {
	m := Module(func(_ *googrpc.Server) {}, ":50051").(*grpcModule)
	err := m.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

func TestModule_StopRejectsNilContext(t *testing.T) {
	m := Module(func(_ *googrpc.Server) {}, ":50051").(*grpcModule)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	require.NoError(t, m.Init(context.Background(), app.ModuleContext{
		Logger: logger,
		Runner: lifecycle.NewRunner(logger),
	}))

	err := m.Stop(nil) //nolint:staticcheck // exercising the explicit nil-context guard
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestModule_ServeListenErrorDoesNotReflectAddress(t *testing.T) {
	m := &grpcModule{
		addr:   "secret-token.invalid:-1",
		server: googrpc.NewServer(),
		logger: slog.Default(),
	}
	err := m.serve()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gRPC listen failed")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestModule_ImplementsCapabilityHooks(t *testing.T) {
	var _ app.ServerTLSReceiver = (*grpcModule)(nil)
	var _ app.HealthCheckerReceiver = (*grpcModule)(nil)
	var _ app.InternalHandlerWrapper = (*grpcModule)(nil)
	var _ app.RunnerAttacher = (*grpcModule)(nil)
}

func TestWithInternalGRPCHealth_PanicsOnNilBase(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r)
	}()
	withInternalGRPCHealth(nil, &health.Checker{})
}

func TestWithInternalGRPCHealth_NilCheckerReturnsBase(t *testing.T) {
	base := http.NotFoundHandler()
	got := withInternalGRPCHealth(base, nil)
	assert.NotNil(t, got)
}

func TestWithInternalGRPCHealth_NonGRPCRequestsPassThrough(t *testing.T) {
	checker := &health.Checker{Version: "test"}
	var fellThrough atomic.Bool
	base := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fellThrough.Store(true)
		w.WriteHeader(http.StatusOK)
	})
	handler := withInternalGRPCHealth(base, checker)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	handler.ServeHTTP(rec, req)
	assert.True(t, fellThrough.Load())
}

func TestInternalGRPCHealthRequest_RejectsHTTP1(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.ProtoMajor = 1
	req.Header.Set("Content-Type", "application/grpc")
	assert.False(t, internalGRPCHealthRequest(req))
}

func TestInternalGRPCHealthRequest_AcceptsHTTP2GRPC(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.ProtoMajor = 2
	req.Header.Set("Content-Type", "application/grpc")
	assert.True(t, internalGRPCHealthRequest(req))
}

func TestInternalGRPCHealthRequest_AcceptsContentTypeWithSubtype(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.ProtoMajor = 2
	req.Header.Set("Content-Type", "application/grpc+proto")
	assert.True(t, internalGRPCHealthRequest(req))
}

func TestInternalGRPCHealthRequest_RejectsMissingContentType(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.ProtoMajor = 2
	assert.False(t, internalGRPCHealthRequest(req))
}
