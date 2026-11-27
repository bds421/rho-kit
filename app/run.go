package app

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/healthhttp"
	"github.com/bds421/rho-kit/httpx/v2/middleware/stack"
	"github.com/bds421/rho-kit/httpx/v2/slohttp"
	"github.com/bds421/rho-kit/observability/v2/health"
	"github.com/bds421/rho-kit/runtime/v2/lifecycle"
	"github.com/bds421/rho-kit/security/v2/netutil"
)

// appRun is the state of one [Builder.RunContext], from module ordering to
// the runner's hand-off.
type appRun struct {
	b       *Builder
	logger  *slog.Logger
	modules []Module
	httpCfg resolvedHTTPConfig
	runner  *lifecycle.Runner

	serverTLS    *tls.Config
	tlsSource    netutil.CertificateSource
	tlsReloadSrc *netutil.FilesCertificateSource
	// closeTLS closes the reloading TLS source while RunContext still owns
	// it; runner.Run takes it over through a shutdown hook.
	closeTLS func()
}

// orderedModules puts modules from With*() config before user-registered
// ones, except that a module implementing [TracingProvider] initializes
// before the builtin HTTP-client module, so the client can observe
// TracingActive() and wrap its transport (TestHTTPClient_PicksUpTracingFromUserModule).
func (b *Builder) orderedModules() []Module {
	builtinModules := b.buildIntegrationModules()
	allModules := make([]Module, 0, len(builtinModules)+len(b.modules))
	var deferredUserModules []Module
	for _, m := range b.modules {
		if _, ok := m.(TracingProvider); ok {
			allModules = append(allModules, m)
		} else {
			deferredUserModules = append(deferredUserModules, m)
		}
	}
	allModules = append(allModules, builtinModules...)
	return append(allModules, deferredUserModules...)
}

func (r *appRun) closeUnownedTLS() {
	if r.closeTLS != nil {
		r.closeTLS()
	}
}

// setupServerTLS resolves the public server's TLS. With ReloadingTLS the
// public server, the default outbound HTTP client and every consumer of
// infra.TLSCertificateSource share one poll loop, closed on shutdown.
func (r *appRun) setupServerTLS() error {
	b := r.b
	if !r.httpCfg.reloadingTLSActive {
		serverTLS, err := b.cfg.TLS.ServerTLS(b.serverTLSOptions()...)
		if err != nil {
			return redact.WrapSafe("build server TLS config", err)
		}
		r.serverTLS = serverTLS
		return nil
	}
	src, err := b.cfg.TLS.Reloading(r.httpCfg.reloadingTLSOpts...)
	if err != nil {
		return redact.WrapSafe("build reloading TLS source", err)
	}
	r.closeTLS = func() { _ = src.Close() }
	// The same server TLS options as the static path, so enabling reload
	// does not flip VerifyClientCertIfGiven back to RequireAndVerifyClientCert.
	r.serverTLS = netutil.ReloadingServerTLS(src, b.serverTLSOptions()...)
	r.tlsSource = src
	r.tlsReloadSrc = src
	logger := r.logger
	b.shutdownHooks = append(b.shutdownHooks, func(context.Context) {
		if err := src.Close(); err != nil {
			logger.Warn("tls-reload source close failed", "error", err)
		}
	})
	return nil
}

// newRunner builds the runner whose shutdown hooks fire after ctx
// cancellation but before any component's Stop, so hooks still see live
// DB, broker and cache connections.
func (r *appRun) newRunner() {
	b, logger := r.b, r.logger
	r.runner = lifecycle.NewRunner(logger, lifecycle.WithBeforeStop(func(ctx context.Context) {
		runShutdownHooks(ctx, b.shutdownHooks, logger)
	}))
}

// addTLSReloadSignal reloads the TLS source on the configured signals
// (Validate requires ReloadingTLS with them), as a component so its
// goroutine ends on shutdown even though the source closes later.
func (r *appRun) addTLSReloadSignal() {
	if len(r.httpCfg.tlsReloadSignals) == 0 || r.tlsReloadSrc == nil {
		return
	}
	signals := append([]os.Signal(nil), r.httpCfg.tlsReloadSignals...)
	src, logger := r.tlsReloadSrc, r.logger
	r.runner.AddFunc("tls-reload-signal", func(ctx context.Context) error {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, signals...)
		defer signal.Stop(sigCh)
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-sigCh:
				if err := src.Reload(); err != nil {
					logger.Warn("tls-reload-on-signal reload failed, keeping previous snapshot",
						"error", err)
				}
			}
		}
	})
}

// initModules hands the server TLS to the modules that take it, then
// initializes every module in registration order within the startup
// timeout. The returned cleanup closes them in reverse.
func (r *appRun) initModules(ctx context.Context) (func(), error) {
	b := r.b
	if r.serverTLS != nil {
		for _, m := range r.modules {
			if recv, ok := m.(ServerTLSReceiver); ok {
				recv.SetServerTLS(r.serverTLS)
			}
		}
	}
	if len(r.modules) == 0 {
		return func() {}, nil
	}
	startupTimeout := b.startupTimeout
	if startupTimeout <= 0 {
		startupTimeout = defaultStartupTimeout
	}
	initCtx, initCancel := context.WithTimeout(ctx, startupTimeout)
	moduleCleanup, err := initModules(initCtx, r.modules, b.name, r.logger, r.runner, b.cfg, r.tlsSource)
	initCancel()
	if err != nil {
		return nil, err
	}
	return func() {
		cleanupCtx, cancel := detachedTimeoutContext(ctx, 10*time.Second)
		defer cancel()
		moduleCleanup(cleanupCtx)
	}, nil
}

func (r *appRun) collectModuleHealthChecks() error {
	for _, m := range r.modules {
		checks := m.HealthChecks()
		for _, check := range checks {
			if err := health.ValidateDependencyCheck(check); err != nil {
				return redact.WrapSafe("module health check invalid", err)
			}
		}
		r.b.healthChecks = append(r.b.healthChecks, checks...)
	}
	return nil
}

// routerRegistration holds what RouterFunc registers through
// [Infrastructure]; it is frozen once RouterFunc returns, since later
// background goroutines would be lost.
type routerRegistration struct {
	mu     sync.Mutex
	frozen bool
	bgs    []bgSpec
}

func (g *routerRegistration) add(method string, register func()) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.frozen {
		panic("app: RunContext " + method + "() must only be called synchronously within RouterFunc")
	}
	register()
}

func (g *routerRegistration) freeze() []bgSpec {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.frozen = true
	return g.bgs
}

func (r *appRun) infrastructure(reg *routerRegistration) Infrastructure {
	b := r.b
	return Infrastructure{
		Logger:        r.logger,
		ServerTLS:     r.serverTLS,
		TLSCertSource: r.tlsSource,
		Config:        b.cfg,
		Background: func(name string, fn func(ctx context.Context) error) {
			validateBackgroundSpec(name, fn)
			reg.add("Background", func() { reg.bgs = append(reg.bgs, bgSpec{name: name, fn: fn}) })
		},
		SetCustomReadiness: func(h http.Handler) {
			if h == nil {
				panic("app: SetCustomReadiness requires a non-nil handler")
			}
			reg.add("SetCustomReadiness", func() { b.customReadiness = h })
		},
		AddHealthCheck: func(check health.DependencyCheck) {
			validateDependencyCheck(check, "Infrastructure.AddHealthCheck")
			reg.add("AddHealthCheck", func() { b.healthChecks = append(b.healthChecks, check) })
		},
	}
}

// publicHandler lets the modules populate the infrastructure, runs the
// RouterFunc and starts what it registered in the background. Worker roles
// (WithoutPublicServer) get no handler.
func (r *appRun) publicHandler() http.Handler {
	b := r.b
	reg := &routerRegistration{}
	infra := r.infrastructure(reg)
	for _, m := range r.modules {
		m.Populate(&infra)
	}
	var handler http.Handler
	switch {
	case b.noPublicServer:
	case b.routerFn != nil:
		handler = b.routerFn(infra)
	default:
		handler = http.NotFoundHandler()
	}
	if !b.noPublicServer {
		handler = r.wrapPublicMiddleware(handler)
	}
	for _, bg := range reg.freeze() {
		r.runner.AddFunc(bg.name, bg.untilShutdown())
	}
	return handler
}

// wrapPublicMiddleware composes the inbound chain. Bridge modules declare
// their phase through [MiddlewareInstaller]; the order lives in the
// [MiddlewarePhase] constants, and the Builder stays the one owner of it.
func (r *appRun) wrapPublicMiddleware(handler http.Handler) http.Handler {
	handler = applyPhasedMiddleware(handler, r.modules)
	if r.httpCfg.disableDefaultStack {
		return handler
	}
	return stack.Default(handler, r.logger, r.httpCfg.stackOpts...)
}

func (r *appRun) healthChecker() *health.Checker {
	checker := &health.Checker{
		Version: health.ResolveVersion(r.b.version),
		Checks:  r.b.healthChecks,
	}
	for _, m := range r.modules {
		if recv, ok := m.(HealthCheckerReceiver); ok {
			recv.SetHealthChecker(checker)
		}
	}
	return checker
}

// readinessHandler prefers SetCustomReadiness from RouterFunc over the
// startup-time app/http.Module(http.CustomReadiness(...)).
func (r *appRun) readinessHandler(checker *health.Checker) http.Handler {
	switch {
	case r.b.customReadiness != nil:
		return r.b.customReadiness
	case r.httpCfg.customReadiness != nil:
		return r.httpCfg.customReadiness
	default:
		return healthhttp.Handler(checker)
	}
}

func (r *appRun) internalHandler(checker *health.Checker) http.Handler {
	var opts []healthhttp.InternalHandlerOption
	for _, m := range r.modules {
		if sp, ok := m.(SLOCheckerProvider); ok {
			if slo := sp.SLOChecker(); slo != nil {
				opts = append(opts, healthhttp.WithSLOHandler(slohttp.Handler(slo)))
				break
			}
		}
	}
	handler := healthhttp.NewInternalHandler(r.b.version, r.readinessHandler(checker), opts...)
	for _, m := range r.modules {
		if wrap, ok := m.(InternalHandlerWrapper); ok {
			handler = wrap.WrapInternalHandler(handler, checker)
		}
	}
	return handler
}

// internalServer serves readiness, health and metrics. It runs outside the
// Runner so it outlives the workers during drain.
func (r *appRun) internalServer() lifecycle.Component {
	opts := []httpx.ServerOption{serverErrorLogOption(r.logger)}
	if internalTLS := r.httpCfg.internalTLS; internalTLS != nil {
		opts = append(opts, httpx.WithTLSConfig(internalTLS))
	} else if r.serverTLS != nil {
		opts = append(opts, httpx.WithTLSConfig(r.serverTLS))
	}
	srv := httpx.NewServer(r.internalAddr(), r.internalHandler(r.healthChecker()), opts...)
	for _, m := range r.modules {
		if cfg, ok := m.(InternalServerConfigurator); ok {
			cfg.ConfigureInternalServer(srv)
		}
	}
	return serverComponent(srv, r.httpCfg.internalListener)
}

func (r *appRun) internalAddr() string {
	return listenAddr(r.b.cfg.Internal.Addr(), r.httpCfg.internalListener)
}

func startInternalServer(ctx context.Context, server lifecycle.Component) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		if err := server.Start(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	return errCh
}

func (r *appRun) stopInternalServer(ctx context.Context, server lifecycle.Component) {
	shutdownCtx, cancel := detachedTimeoutContext(ctx, 5*time.Second)
	defer cancel()
	if err := server.Stop(shutdownCtx); err != nil {
		r.logger.Warn("internal server shutdown error", redact.Error(err))
	}
}

// watchInternalServer waits briefly for a bind failure, then shuts the
// whole service down should the internal server die later: running
// without health checks or metrics is unsafe in production.
func (r *appRun) watchInternalServer(errCh <-chan error) error {
	select {
	case err := <-errCh:
		return redact.WrapSafe("internal server failed to start", err)
	case <-time.After(50 * time.Millisecond):
	}
	r.logger.Info("internal server started", slog.String("addr", r.internalAddr()))
	r.runner.AddFunc("internal-server-monitor", func(ctx context.Context) error {
		select {
		case err := <-errCh:
			return redact.WrapSafe("internal server failed", err)
		case <-ctx.Done():
			return nil
		}
	})
	return nil
}

// attachAdapters lets adapter-owned components (the gRPC server of
// app/grpc) join the Runner before the public server, so they stop after it.
func (r *appRun) attachAdapters() {
	for _, m := range r.modules {
		if attacher, ok := m.(RunnerAttacher); ok {
			attacher.AttachToRunner(r.runner)
		}
	}
}

// addPublicServer adds the public server last so it stops first. Caller
// options go first and the kit's hardened defaults after them, so
// app/http.WithServerOption cannot relax TLS or the error log.
func (r *appRun) addPublicServer(handler http.Handler) {
	srvOpts := make([]httpx.ServerOption, 0, len(r.httpCfg.serverOpts)+2)
	srvOpts = append(srvOpts, r.httpCfg.serverOpts...)
	srvOpts = append(srvOpts, serverErrorLogOption(r.logger))
	if r.serverTLS != nil {
		srvOpts = append(srvOpts, httpx.WithTLSConfig(r.serverTLS))
	}
	srv := httpx.NewServer(listenAddr(r.b.cfg.Server.Addr(), r.httpCfg.publicListener), handler, srvOpts...)
	r.runner.Add("public-server", serverComponent(srv, r.httpCfg.publicListener))
}
