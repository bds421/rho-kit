package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/observability/health"
	"github.com/bds421/rho-kit/observability/tracing"
)

// tracingModule implements the Module interface for OpenTelemetry tracing.
// It initializes the TracerProvider and exposes whether tracing is active
// so downstream modules (e.g., httpClientModule) can configure instrumented
// transports.
type tracingModule struct {
	BaseModule

	cfg tracing.Config

	// initialized during Init
	provider      *tracing.Provider
	active        bool
	healthChecks_ []health.DependencyCheck
	logger        *slog.Logger
}

// newTracingModule creates a tracing module from the given config.
func newTracingModule(cfg tracing.Config) *tracingModule {
	return &tracingModule{
		BaseModule: NewBaseModule("tracing"),
		cfg:        cfg,
	}
}

func (m *tracingModule) Init(ctx context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	tp, err := tracing.Init(ctx, m.cfg)
	if err != nil {
		mc.Logger.Warn("tracing init failed, continuing without tracing", "error", err)
		m.healthChecks_ = []health.DependencyCheck{
			{
				Name: "tracing",
				Check: func(_ context.Context) string {
					return health.StatusDegraded
				},
			},
		}
		return nil
	}

	m.provider = tp
	m.active = true
	mc.Logger.Info("tracing enabled", "endpoint", m.cfg.Endpoint)
	return nil
}

func (m *tracingModule) HealthChecks() []health.DependencyCheck {
	return m.healthChecks_
}

func (m *tracingModule) Close(_ context.Context) error {
	if m.provider == nil {
		return nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.provider.Shutdown(shutdownCtx); err != nil {
		m.logger.Warn("tracing shutdown error", "error", err)
		return fmt.Errorf("tracing shutdown: %w", err)
	}
	return nil
}

// Active reports whether tracing was successfully initialized.
func (m *tracingModule) Active() bool {
	return m.active
}
