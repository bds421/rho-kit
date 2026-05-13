package tracing

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/observability/v2/tracing"
)

// Module returns an [app.Module] that initializes an OpenTelemetry
// TracerProvider for the service. Pass to [app.Builder.With].
//
// The Module always-on validates the configuration: an OTel sample rate
// above 0.1 panics because full sampling is a collector-cost foot-gun in
// production. To override use [tracing.Config.SampleRate]<=0.1.
func Module(cfg tracing.Config) app.Module {
	if err := cfg.Validate(); err != nil {
		panic(fmt.Sprintf("tracing: invalid config: %v", err))
	}
	if cfg.SampleRate > 0.1 {
		panic(fmt.Sprintf("tracing: SampleRate=%.2f exceeds 0.1; lower the rate and override per-trace via the OTel SDK if you need bursts", cfg.SampleRate))
	}
	return &tracingModule{cfg: cfg}
}

// tracingModule implements the Module interface for OpenTelemetry tracing.
type tracingModule struct {
	app.BaseModule

	cfg tracing.Config

	provider      *tracing.Provider
	active        bool
	healthChecks_ []health.DependencyCheck
	logger        *slog.Logger
}

func (m *tracingModule) Name() string { return "tracing" }

// TracingActive implements [app.TracingProvider]. The app/v2 HTTP-client
// module queries this to decide whether to install OTel instrumentation
// on its transport.
func (m *tracingModule) TracingActive() bool { return m.active }

func (m *tracingModule) Init(ctx context.Context, mc app.ModuleContext) error {
	m.logger = mc.Logger

	tp, err := tracing.Init(ctx, m.cfg)
	if err != nil {
		mc.Logger.Warn("tracing init failed, continuing without tracing", slog.Any("error", err))
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
	mc.Logger.Info("tracing enabled", "endpoint_configured", m.cfg.Endpoint != "")
	return nil
}

func (m *tracingModule) HealthChecks() []health.DependencyCheck {
	if m.healthChecks_ == nil {
		return nil
	}
	return append([]health.DependencyCheck(nil), m.healthChecks_...)
}

func (m *tracingModule) Stop(ctx context.Context) error {
	if m == nil || m.provider == nil {
		return nil
	}
	provider := m.provider
	m.provider = nil
	shutdownCtx, cancel := detachedTimeoutContext(ctx, 5*time.Second)
	defer cancel()
	if err := provider.Stop(shutdownCtx); err != nil {
		m.logger.Warn("tracing shutdown error", slog.Any("error", err))
		return fmt.Errorf("tracing shutdown: %w", err)
	}
	return nil
}

func detachedTimeoutContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}
